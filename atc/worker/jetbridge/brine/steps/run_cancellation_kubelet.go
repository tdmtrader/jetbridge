package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeletRun is one Run whose steps execute on the disposable K3s node: the
// real kubelet runs the Pods, the real output daemon answers for the node.
type KubeletRun struct {
	RunOutputRuntime
	Executor jetbridge.PodExecutor
}

// Durable Run cancellation's Linux/K3s evidence (durable_run_cancellation_control
// T7). Every scenario acquires the one disposable node hack/test-run-kubelet
// marks; none substitutes envtest or a host process for the kubelet.
func RunCancellationKubeletDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, KubeletRun]("a Run on a disposable kubelet", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (KubeletRun, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			in, executor, err := disposableKubeletRuntime(ctx, rec, res, "brine.dev/run-kubelet", "brine-run-", true)
			if err != nil {
				return KubeletRun{}, err
			}
			in.Spec = runtime.ContainerSpec{Outputs: runtime.OutputPaths{"result": "/workspace/result"}}
			return KubeletRun{RunOutputRuntime: in, Executor: executor}, nil
		}),
		brine.DefineMap[KubeletRun, CancellationLeaseResult]("the kubelet worker starts a prepared {string} after the fence", func(in KubeletRun, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			kind, _ := p.GetString(0)
			return CancellationLeaseResult{Err: kubeletStartAfterFence(in, kind)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the kubelet ran no Pod and the node recorded no start", func(in CancellationLeaseResult) error { return in.Err }),
		brine.DefineMap[KubeletRun, CancellationLeaseResult]("cancellation interrupts a running {string} command on the kubelet", func(in KubeletRun, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			command, _ := p.GetString(0)
			if command != "ordinary" && command != "TERM-resistant" {
				return CancellationLeaseResult{}, fmt.Errorf("unknown command %q", command)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			return CancellationLeaseResult{Err: kubeletActiveCancellation(ctx, in.RunOutputRuntime, in.Executor, command == "TERM-resistant")}, nil
		}),
		CheckThat[CancellationLeaseResult]("the command stops with an exact acknowledgement and keeps its Pod", func(in CancellationLeaseResult) error { return in.Err }),
		brine.DefineMap[KubeletRun, CancellationLeaseResult]("cancellation meets a selected-output producer whose output daemon is down", func(in KubeletRun, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: kubeletProvisionalSourceSurvives(in)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the provisional source and its Pod survive until the node answers", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func (in KubeletRun) worker(name string) (*jetbridge.Worker, db.PipelineRunFactory, error) {
	return kubeletWorker(in.RunOutputRuntime, in.Executor, name)
}

func (in KubeletRun) execution(ctx context.Context, factory db.PipelineRunFactory, buildID int, planID string) (db.RunExecutionAdmission, error) {
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return db.RunExecutionAdmission{}, err
	}
	defer db.Rollback(tx)
	a, found, err := factory.RunExecution(ctx, tx, buildID, atc.PlanID(planID))
	if err == nil && !found {
		err = fmt.Errorf("the worker retained no execution for build %d", buildID)
	}
	return a, err
}

// A container the worker admitted before the fence may still be started
// afterwards by a slow step. The fence must refuse the process before any Pod
// exists, and the node must still say the exact execution never started.
func kubeletStartAfterFence(in KubeletRun, kind string) error {
	if kind != "task" && kind != "check" {
		return fmt.Errorf("unknown kind %q", kind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	w, factory, err := in.worker("run-kubelet-late")
	if err != nil {
		return err
	}
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
			return fmt.Errorf("no Run-owned check was admitted before the fence")
		}
		build = check.Build
	}
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerType(kind)}
	spec := runtime.ContainerSpec{TeamID: build.TeamID(), Type: db.ContainerType(kind), ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37"}}
	c, _, err := w.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(build.ID(), "late-step", build.TeamID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	a, err := in.execution(ctx, factory, build.ID(), "late-step")
	if err != nil {
		return err
	}
	if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	_, err = c.Run(ctx, runtime.ProcessSpec{ID: "late-command", Path: "sh", Args: []string{"-c", "true"}}, runtime.ProcessIO{Stdout: new(strings.Builder), Stderr: new(strings.Builder)})
	if !errors.Is(err, db.ErrPipelineRunCancelling) {
		return fmt.Errorf("a %s admitted before the fence started after it: %v", kind, err)
	}
	name := jetbridge.GeneratePodName(metadata, c.DBContainer().Handle())
	if _, err = in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("the kubelet was given a Pod after the fence: %v", err)
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter)
	classified, err := client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classified.Classification != executioncontrol.ClassificationNeverStarted {
		return fmt.Errorf("the node recorded a start after the fence: %s", classified.Classification)
	}
	return nil
}

// A selected-output producer is running on the node, so its output is a
// provisional source behind a pre-start hold. With the node's daemon gone,
// cancellation can prove nothing about the process or Stage 2; it must not
// classify, release, finish, stop or delete anything. The daemon's own ledger
// must still name the exact execution as executing once it answers again.
func kubeletProvisionalSourceSurvives(in KubeletRun) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var err error
	if in.Control, err = in.prepare(); err != nil {
		return err
	}
	if in.Control == nil || in.Control.Capture == nil {
		return fmt.Errorf("the producer holds no provisional source")
	}
	w, factory, err := in.worker("run-kubelet-producer")
	if err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	spec := in.Spec
	spec.TeamID, spec.Type, spec.ExecutionControl = build.TeamID(), db.ContainerTypeTask, in.Control
	spec.ImageSpec = runtime.ImageSpec{ImageURL: "busybox:1.37"}
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}
	c, _, err := w.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(build.ID(), "capture-step", build.TeamID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	a, err := in.execution(ctx, factory, build.ID(), "capture-step")
	if err != nil {
		return err
	}
	process, err := c.Run(ctx, runtime.ProcessSpec{ID: "provisional-producer", Path: "sh", Args: []string{"-c", `printf provisional > /workspace/result/data; sleep 180`}}, runtime.ProcessIO{})
	if err != nil {
		return err
	}
	waitCtx, stopWait := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := process.Wait(waitCtx); done <- err }()
	defer func() {
		stopWait()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	name := jetbridge.GeneratePodName(metadata, c.DBContainer().Handle())
	if err = waitKubeletProbe(ctx, in.Executor, in.Config.Namespace, name, "test -s /workspace/result/data"); err != nil {
		return err
	}
	pod, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	daemon := in.Start.Daemon.Output
	if err = daemon.crash(); err != nil {
		return err
	}
	if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationSourceWorker(in.RunOutputRuntime)
	// The node cannot be reached, so a pass that tries it must say so. A
	// failed operation is backed off, so later passes may find nothing due;
	// what must hold is that some pass failed.
	failed := false
	for pass := 0; pass < 3; pass++ {
		if err := worker.Run(ctx); err != nil {
			failed = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	if !failed {
		return fmt.Errorf("no cancellation pass reported the unreachable node daemon")
	}
	// Without this the facts below would also hold for a worker that never
	// reached the node: the execution's closure must have been tried against
	// it and left as open, retryable debt. The capture itself is the
	// database's to discard and asks the node nothing.
	var tried bool
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations
 WHERE run_id=$1 AND kind=$2 AND subject=$3 AND attempt_count>0 AND completed_at IS NULL AND debt IN ('unavailable','timeout'))`,
		a.RunID, string(db.CancelExecution), executionSubject(a.Identity)).Scan(&tried); err != nil {
		return err
	}
	if !tried {
		return fmt.Errorf("cancellation never tried to close the producer's execution against the node")
	}
	var closed bool
	var status string
	if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2),
 (SELECT status FROM pipeline_runs WHERE id=$3)`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), a.RunID).Scan(&closed, &status); err != nil {
		return err
	}
	if closed || status != "running" {
		return fmt.Errorf("an unreachable node yielded conclusions: execution closed=%t, Run %s", closed, status)
	}
	current, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || current.UID != pod.UID || current.DeletionTimestamp != nil {
		return fmt.Errorf("cancellation destroyed the producer Pod while its source was unresolved: %v", err)
	}
	if err = kubeletProbe(ctx, in.Executor, in.Config.Namespace, name, `test "$(cat /workspace/result/data)" = provisional`); err != nil {
		return fmt.Errorf("the provisional source did not survive: %w", err)
	}

	if err = daemon.restart(ctx, in.Start.Daemon.HTTP); err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(daemon.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter)
	classified, err := client.Classify(ctx, a.Identity)
	if err != nil {
		return err
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return fmt.Errorf("the node lost the exact execution across its restart: %s", classified.Classification)
	}
	return kubeletProbe(ctx, in.Executor, in.Config.Namespace, name, `test "$(cat /workspace/result/data)" = provisional`)
}
