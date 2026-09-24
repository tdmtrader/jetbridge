package steps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func RunExecutionRuntimeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, RunOutputRuntime]("a Run with resource checks and a ready output node", []string{"jetbridge-db", "real-cluster"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, error) {
			return newRunOutputRuntime(rec, res, true)
		}),
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("its real worker admits a {string} execution", []string{"task-workspace"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			kind, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseRunExecutionRuntime(in, kind, false, rec, res.Get("task-workspace").(TaskWorkspace).Dir)}, nil
		}),
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("its real worker starts a prepared container after cancellation", []string{"task-workspace"}, func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseRunExecutionRuntime(in, "task", true, rec, res.Get("task-workspace").(TaskWorkspace).Dir)}, nil
		}),
		CheckThat[CancellationLeaseResult]("that worker preserves exact execution ownership and placement", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("that worker refuses the late process without creating a Pod", func(in CancellationLeaseResult) error { return in.Err }),
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("its real worker attempts {string} after cancellation", []string{"task-workspace"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			operation, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseRunExecutionRuntime(in, operation, false, rec, res.Get("task-workspace").(TaskWorkspace).Dir)}, nil
		}),
		CheckThat[CancellationLeaseResult]("that worker refuses an unadmitted command", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("that worker retains its signed execution witnesses", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("cancellation closes only its exact execution", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("cancellation preserves an unresolved execution", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseRunExecutionRuntime(in RunOutputRuntime, kind string, cancelFirst bool, rec *brine.Recorder, workspace string) error {
	operation := kind
	daemonLoss := strings.HasPrefix(operation, "cancel daemon loss")
	daemonFault := strings.TrimPrefix(strings.TrimPrefix(operation, "cancel daemon loss"), " with ")
	kind = strings.TrimPrefix(kind, "witness ")
	if kind == "finish commit failure" || kind == "untrusted signer" {
		kind = "task"
	}
	if strings.HasPrefix(kind, "cancel ") {
		kind = "task"
	}
	if operation == "cancel check completed" {
		kind = "check"
	}
	failedFinish := operation == "finish commit failure" || operation == "cancel unrecorded finish"
	if kind == "late launch" || kind == "intercept" {
		kind = "task"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	row, err := in.Start.DB.PersistNamedWorker("run-execution")
	if err != nil {
		return err
	}
	config := in.Config
	config.OutputPlaneEnabled = true
	config.OutputActivationEpoch = int64(hangarEpoch)
	executor := runWitnessExecutor{localExecutor: localExecutor{client: in.Client, supervisorRoot: workspace}, conn: in.Start.DB.Conn}
	if daemonLoss {
		executor.afterCommand = func(ctx context.Context, namespace, pod string) error {
			if err := (lostOutcomeExecutor{localExecutor: executor.localExecutor, fault: daemonFault}).loseJournal(ctx, namespace, pod); err != nil {
				return err
			}
			return in.Start.Daemon.Output.crash()
		}
		in.OutcomeReader = executor.localExecutor
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	if operation == "untrusted signer" {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		keys.Keys[0].PublicKey = base64.StdEncoding.EncodeToString(public)
	}
	w := jetbridge.NewWorker(row, in.Client, config, jetbridge.WorkerDeps{
		Executor:          executor,
		OutputControls:    jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)),
		ExecutionPreparer: &runs.ExecutionStarter{Conn: in.Start.DB.Conn, Factory: factory, Source: jetbridge.NewOutputSource(in.Client, config, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)), Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys},
	})
	build := in.Start.Creation.EntryBuilds[0]
	if kind == "check" {
		check, err := requestRunCheck(in.Start, "persisted")
		if err != nil {
			return err
		}
		if check.Err != nil {
			return check.Err
		}
		if check.Build == nil {
			return fmt.Errorf("no persisted check")
		}
		build = check.Build
	}
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerType(kind)}
	spec := runtime.ContainerSpec{TeamID: build.TeamID(), Type: db.ContainerType(kind), ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}
	owner := db.NewBuildStepContainerOwner(build.ID(), "runtime-step", build.TeamID())
	c, _, err := w.FindOrCreateContainer(ctx, owner, metadata, spec, nil)
	if err != nil {
		return err
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	a, found, err := factory.RunExecution(ctx, tx, build.ID(), "runtime-step")
	db.Rollback(tx)
	if err != nil {
		return err
	}
	if !found || a.RunID != in.Start.Creation.Run.ID() || a.NodeUID != string(in.Node.UID) || a.NodeName != in.Node.Name {
		return fmt.Errorf("worker did not retain its exact node and Run ownership")
	}
	if a.Kind != db.ContainerType(kind) {
		return fmt.Errorf("worker lost execution kind")
	}
	if operation == "cancel prepared" {
		return exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationNeverStarted)
	}
	name := jetbridge.GeneratePodName(metadata, c.DBContainer().Handle())
	TrackDisposer(rec, "the run execution pod "+name, func() error {
		return releasedIfGone(in.Client.CoreV1().Pods(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}))
	})
	if cancelFirst {
		if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
			return err
		}
	}
	marker := filepath.Join(workspace, "run-command")
	command := runtime.ProcessSpec{ID: "run-owned-command", Path: "sh", Args: []string{"-c", `printf x >> "$1"`, "brine", marker}}
	activeStop := strings.HasPrefix(operation, "cancel active")
	if activeStop {
		in.OutcomeReader = executor.localExecutor
		script := `printf x >> "$1"; sleep 60 & C=$!; printf '%s\n' "$C" > "$1.child"; wait "$C"`
		if operation == "cancel active ignores TERM" {
			script = "trap '' TERM; " + script
		}
		command.Args[1] = script
	}
	io := runtime.ProcessIO{Stdout: new(strings.Builder), Stderr: new(strings.Builder)}
	process, err := c.Run(ctx, command, io)
	if cancelFirst {
		if !errors.Is(err, db.ErrPipelineRunCancelling) {
			return fmt.Errorf("prepared container bypassed cancellation: %v", err)
		}
		_, err = in.Client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("late start created a Pod: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	pod, err := in.Client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil || pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return fmt.Errorf("controlled Pod is not pinned to its admitted node")
	}
	for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		pinned := false
		for _, expr := range term.MatchFields {
			if expr.Key == "metadata.name" && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) == 1 && expr.Values[0] == in.Node.Name {
				pinned = true
			}
		}
		if !pinned {
			return fmt.Errorf("a scheduling branch can place the execution on another node")
		}
	}
	volumes := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, mount := range container.VolumeMounts {
			if !volumes[mount.Name] {
				return fmt.Errorf("Pod mount %q has no volume", mount.Name)
			}
		}
	}
	if err = in.Client.CoreV1().Pods(config.Namespace).Bind(ctx, &corev1.Binding{ObjectMeta: metav1.ObjectMeta{Name: name}, Target: corev1.ObjectReference{Kind: "Node", Name: in.Node.Name}}, metav1.CreateOptions{}); err != nil {
		return err
	}
	pod, err = in.Client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, err = in.Client.CoreV1().Pods(config.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	if operation == "cancel unretained start" || operation == "cancel aborted unretained start" {
		in.OutcomeReader = executor.localExecutor
		return exerciseUnretainedStart(ctx, in, a, operation == "cancel aborted unretained start", process, client, marker, build)
	}
	if operation == "late launch" || operation == "intercept" {
		if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
			return err
		}
		if operation == "intercept" {
			lookedUp, found, err := w.LookupContainer(ctx, c.DBContainer().Handle())
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("container disappeared")
			}
			_, err = lookedUp.Run(ctx, command, io)
			if err == nil {
				return fmt.Errorf("interception admitted an untracked process in a cancelled Run")
			}
		} else {
			_, err = process.Wait(ctx)
			if !errors.Is(err, db.ErrPipelineRunCancelling) {
				return fmt.Errorf("late command bypassed cancellation: %v", err)
			}
		}
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			return fmt.Errorf("late operation executed the command: %v", err)
		}
		classified, err := client.Classify(ctx, a.Identity)
		if err != nil {
			return err
		}
		if classified.Classification != executioncontrol.ClassificationNeverStarted {
			return fmt.Errorf("refused command still started")
		}
		return nil
	}
	if failedFinish {
		_, err = in.Start.DB.Conn.Exec(`CREATE FUNCTION brine_reject_execution_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'brine interrupted finish transaction'; END; $$;
 CREATE TRIGGER brine_reject_execution_finish BEFORE INSERT ON pipeline_run_execution_closures FOR EACH ROW EXECUTE FUNCTION brine_reject_execution_finish()`)
		if err != nil {
			return err
		}
	}
	if activeStop {
		done := make(chan error, 1)
		go func() { _, err := process.Wait(ctx); done <- err }()
		defer func() { cancel(); <-done }()
		var child int
		for child == 0 {
			data, err := os.ReadFile(marker + ".child")
			if err == nil {
				child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("controlled command never started its real child")
			case <-time.After(10 * time.Millisecond):
			}
		}
		defer syscall.Kill(child, syscall.SIGKILL)
		if err := exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish); err != nil {
			return err
		}
		// The child must be gone or reaped to a zombie, never still executing.
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child)); err == nil {
			_, rest, ok := strings.Cut(string(stat), ") ")
			if !ok || !strings.HasPrefix(rest, "Z ") {
				return fmt.Errorf("cancellation left a running descendant")
			}
		}
		current, err := in.Client.CoreV1().Pods(config.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil || current.UID != pod.UID || current.DeletionTimestamp != nil {
			return fmt.Errorf("active interruption destroyed the original Pod before source disposition: %v", err)
		}
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != "x" {
			return fmt.Errorf("active interruption lost source data or repeated the command: %v", err)
		}
		return nil
	}
	result, err := process.Wait(ctx)
	if daemonLoss {
		if err == nil {
			return fmt.Errorf("runtime reported completion while the node ledger was unavailable")
		}
		if err = in.Start.Daemon.Output.restart(ctx, in.Start.Daemon.HTTP); err != nil {
			return err
		}
		classified, err := client.Classify(ctx, a.Identity)
		if err != nil {
			return err
		}
		if classified.Classification != executioncontrol.ClassificationExecuting {
			return fmt.Errorf("daemon interruption did not leave an unrecorded outcome")
		}
		if daemonFault == "" {
			err = exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish)
		} else {
			err = exerciseUnresolvedCancellationOutcome(in, a)
		}
		if err != nil {
			return err
		}
		executed, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if string(executed) != "x" {
			return fmt.Errorf("cancellation recovery repeated a completed command")
		}
		return nil
	}
	if operation == "untrusted signer" {
		if err == nil {
			return fmt.Errorf("an untrusted node signed the command start")
		}
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			return fmt.Errorf("unverified start reached command execution")
		}
		var facts int
		if err = in.Start.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_execution_starts)+(SELECT count(*) FROM pipeline_run_execution_closures)`).Scan(&facts); err != nil {
			return err
		}
		if facts != 0 {
			return fmt.Errorf("unverified node evidence was retained")
		}
		return nil
	}
	if failedFinish {
		if err == nil {
			return fmt.Errorf("runtime exposed an outcome after its witness commit failed")
		}
		observed, observationErr := client.Classify(ctx, a.Identity)
		if observationErr != nil {
			return observationErr
		}
		if observed.Classification != executioncontrol.ClassificationAuthoritativeFinish {
			return fmt.Errorf("fault occurred before the real command finished")
		}
		if err = build.Finish(db.BuildStatusSucceeded); !errors.Is(err, atc.ErrRunOutputPending) {
			return fmt.Errorf("build became terminal without its Run witness: %v", err)
		}
		if _, err = in.Start.DB.Conn.Exec(`DROP TRIGGER brine_reject_execution_finish ON pipeline_run_execution_closures; DROP FUNCTION brine_reject_execution_finish()`); err != nil {
			return err
		}
		if operation == "cancel unrecorded finish" {
			if err = exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish); err != nil {
				return err
			}
			executed, err := os.ReadFile(marker)
			if err != nil {
				return err
			}
			if string(executed) != "x" {
				return fmt.Errorf("cancellation recovery repeated the completed command")
			}
			return nil
		}
		c, _, err = w.FindOrCreateContainer(ctx, owner, metadata, spec, nil)
		if err != nil {
			return err
		}
		process, err = c.Run(ctx, command, io)
		if err != nil {
			return err
		}
		result, err = process.Wait(ctx)
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	if result.ExitStatus != 0 {
		return fmt.Errorf("controlled command failed: %d", result.ExitStatus)
	}
	classified, err := client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classified.Classification != executioncontrol.ClassificationAuthoritativeFinish || classified.Acknowledgement == nil || classified.Acknowledgement.PodUID != executioncontrol.PodUID(pod.UID) {
		return fmt.Errorf("Run command exposed an outcome without an exact node witness")
	}
	if operation == "cancel completed" || operation == "cancel check completed" {
		return exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish)
	}
	if strings.HasPrefix(operation, "witness ") || operation == "finish commit failure" {
		var startJSON, finishJSON []byte
		if err = in.Start.DB.Conn.QueryRow(`SELECT s.witness,c.observation FROM pipeline_run_execution_starts s
 JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE s.execution_id=$1 AND s.execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&startJSON, &finishJSON); err != nil {
			return fmt.Errorf("Run did not retain its executor witnesses: %w", err)
		}
		var start executioncontrol.Acknowledgement
		var finish db.RunOutputCancellationEvidence
		if err = json.Unmarshal(startJSON, &start); err != nil {
			return err
		}
		if err = json.Unmarshal(finishJSON, &finish); err != nil {
			return err
		}
		if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != a.Identity || start.PodUID != executioncontrol.PodUID(pod.UID) {
			return fmt.Errorf("retained start names another exact process")
		}
		// Discover the journal written by the real supervisor or resource
		// session, independently of production's naming, and check that the
		// signed locator names it: cancellation interrupts and recovers
		// through that locator alone.
		journals, err := filepath.Glob(filepath.Join(workspace, supervisorStateDirectory, "*", "exit"))
		if err != nil || len(journals) != 1 {
			return fmt.Errorf("expected one completed %s journal, got %v: %v", kind, journals, err)
		}
		state := filepath.Dir(journals[0])
		if _, err = os.Stat(filepath.Join(state, "start")); err != nil {
			return fmt.Errorf("signed %s has no start journal: %w", kind, err)
		}
		locator := "resource-v1:/tmp/"
		if kind == "task" {
			locator = "supervisor-v1:/tmp/"
		}
		if string(start.ProcessIdentity) != locator+filepath.Base(state) {
			return fmt.Errorf("retained start does not name the actual %s journal", kind)
		}
		if finish.Execution.Acknowledgement == nil || finish.Execution.Acknowledgement.Signature != classified.Acknowledgement.Signature || finish.Execution.Acknowledgement.LedgerSequence <= start.LedgerSequence || finish.Execution.Acknowledgement.ProcessIdentity != start.ProcessIdentity || finish.Execution.Acknowledgement.PodUID != start.PodUID || finish.Execution.Acknowledgement.NodeUID != start.NodeUID || finish.Execution.Acknowledgement.ActivationEpoch != start.ActivationEpoch {
			return fmt.Errorf("Run finish is not the node's matching durable witness")
		}
	}
	// Reconnecting uses the immutable admission even though the spec supplied
	// to the worker did not carry any control token.
	c, _, err = w.FindOrCreateContainer(ctx, owner, metadata, spec, nil)
	if err != nil {
		return err
	}
	process, err = c.Run(ctx, command, io)
	if err != nil {
		return err
	}
	result, err = process.Wait(ctx)
	if err != nil {
		return err
	}
	if result.ExitStatus != 0 {
		return fmt.Errorf("replay lost its command result")
	}
	executed, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	if string(executed) != "x" {
		return fmt.Errorf("worker replay repeated its command: %q", executed)
	}
	if operation == "finish commit failure" {
		return build.Finish(db.BuildStatusSucceeded)
	}
	return nil
}

func exerciseUnresolvedCancellationOutcome(in RunOutputRuntime, a db.RunExecutionAdmission) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationFinalityWorker(in)
	for pass := 0; pass < 2; pass++ {
		if err := worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	var attempted, closed, changed bool
	err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND subject=$3 AND attempt_count>0),
 EXISTS(SELECT 1 FROM pipeline_run_execution_closures WHERE execution_id=$4 AND execution_fence=$5),
 EXISTS(SELECT 1 FROM builds WHERE id=$6 AND (completed OR aborted))`, a.RunID, string(db.CancelExecution), fmt.Sprintf("%s/%d", a.Identity.ExecutionID, a.Identity.Fence), string(a.Identity.ExecutionID), int64(a.Identity.Fence), a.BuildID).Scan(&attempted, &closed, &changed)
	if err != nil {
		return err
	}
	if !attempted || closed || changed {
		return fmt.Errorf("unprovable outcome: attempted=%t, closed=%t, build changed=%t", attempted, closed, changed)
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	if _, found, err := factory.TerminalResult(ctx, a.RunID); err != nil || found {
		return fmt.Errorf("unprovable outcome acquired a terminal Run result: found=%t, error=%v", found, err)
	}
	return nil
}

func exerciseBaseExecutionCancellation(in RunOutputRuntime, a db.RunExecutionAdmission, want executioncontrol.Classification) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationSourceWorker(in)
	for pass := 0; pass < 8; pass++ {
		if err := worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		var closed, done bool
		if err := in.Start.DB.Conn.QueryRow(`SELECT
 EXISTS(SELECT 1 FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2 AND classification=$3),
 EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$4 AND kind=$5 AND subject=$6 AND completed_at IS NOT NULL)`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), string(want), a.RunID, string(db.CancelExecution), fmt.Sprintf("%s/%d", a.Identity.ExecutionID, a.Identity.Fence)).Scan(&closed, &done); err != nil {
			return err
		}
		if closed && done {
			client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
			if want == executioncontrol.ClassificationNeverStarted {
				if _, err := client.RecordStart(ctx, a.Identity, "late-pod", "late-process"); err == nil {
					return fmt.Errorf("closed execution admitted a delayed first start")
				}
			}
			classified, err := client.Classify(ctx, a.Identity)
			if err != nil {
				return err
			}
			if classified.Classification != want {
				return fmt.Errorf("cancellation invented or changed the node outcome")
			}
			var status string
			if err = in.Start.DB.Conn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, a.RunID).Scan(&status); err != nil {
				return err
			}
			if status != "running" {
				return fmt.Errorf("one execution closure prematurely terminalized the Run")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	return fmt.Errorf("cancellation worker left the exact base execution unsettled")
}

// Observe the actual database before delegating to the real OS executor. This
// catches recording a start after command delivery without substituting either
// the command result or the node's acknowledgement.
type runWitnessExecutor struct {
	localExecutor
	conn         db.DbConn
	afterCommand func(context.Context, string, string) error
}

func (e runWitnessExecutor) ExecInPod(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool, attrs jetbridge.ExecAttrs) error {
	if attrs.Purpose == "step-command" {
		p, err := e.client.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return err
		}
		var witnessed bool
		if err = e.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_execution_starts WHERE witness->>'pod_uid'=$1)`, string(p.UID)).Scan(&witnessed); err != nil {
			return err
		}
		if !witnessed {
			return fmt.Errorf("Run command reached the executor before its start witness committed")
		}
	}
	err := e.localExecutor.ExecInPod(ctx, namespace, pod, container, command, stdin, stdout, stderr, tty, attrs)
	if err == nil && attrs.Purpose == "step-command" && e.afterCommand != nil {
		return e.afterCommand(ctx, namespace, pod)
	}
	return err
}

// A start the node committed and the Run never retained -- its witness insert
// is refused here, as a Run database that is down refuses it -- is delivered
// only as a stop, so the Pod's journal says 143 and the node still says
// executing. Nothing the Run holds names it, and cancellation used to leave it
// pending for ever. It must read the start from the node, retain it, and close
// the execution on the journal's evidence, never running the command. An
// aborted build that cannot finish over it stays unfinished and never asks
// for its Run's cancellation: aborting a build is scoped to that build.
func exerciseUnretainedStart(ctx context.Context, in RunOutputRuntime, a db.RunExecutionAdmission, aborted bool, process runtime.Process, client *jetbridge.OutputControlClient, marker string, build db.Build) error {
	if _, err := in.Start.DB.Conn.Exec(`CREATE FUNCTION brine_reject_execution_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'brine unavailable start witness'; END; $$;
 CREATE TRIGGER brine_reject_execution_start BEFORE INSERT ON pipeline_run_execution_starts FOR EACH ROW EXECUTE FUNCTION brine_reject_execution_start()`); err != nil {
		return err
	}
	_, err := process.Wait(ctx)
	if _, dropErr := in.Start.DB.Conn.Exec(`DROP TRIGGER brine_reject_execution_start ON pipeline_run_execution_starts; DROP FUNCTION brine_reject_execution_start()`); dropErr != nil {
		return dropErr
	}
	if err == nil || !strings.Contains(err.Error(), "brine unavailable start witness") {
		return fmt.Errorf("an unretained start was reported without its witness failure: %v", err)
	}
	classified, err := client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return fmt.Errorf("an outcome was recorded before the Run retained its start: %s", classified.Classification)
	}
	var retained int
	if err = in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_execution_starts WHERE execution_id=$1 AND execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return fmt.Errorf("the refused start witness was retained anyway")
	}
	if aborted {
		if err = build.MarkAsAborted(); err != nil {
			return err
		}
		if err = build.Finish(db.BuildStatusAborted); !errors.Is(err, atc.ErrRunOutputPending) {
			return fmt.Errorf("an aborted build finished over an unclosed execution: %v", err)
		}
		var by string
		if err = in.Start.DB.Conn.QueryRow(`SELECT coalesce(cancel_requested_by,'') FROM pipeline_runs WHERE id=$1`, a.RunID).Scan(&by); err != nil {
			return err
		}
		if by != "" {
			return fmt.Errorf("aborting one build cancelled its whole Run (requested by %q)", by)
		}
	}
	if err = exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish); err != nil {
		return err
	}
	classified, err = client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classified.Acknowledgement == nil || classified.Acknowledgement.Outcome.ExitCode != 143 {
		return fmt.Errorf("the closed execution is not the journal's stopped exit: %+v", classified)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		return fmt.Errorf("closing an unretained start ran the command: %v", err)
	}
	return nil
}
