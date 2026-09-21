package steps

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/concourse/concourse/hangar/output"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	atcexec "github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runs"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func RunTaskEngineDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunInputAdmission, RunInputAdmission]("its real task engine prepares inputs with {string}", []string{"real-cluster"}, func(in RunInputAdmission, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunInputAdmission, error) {
		mode, _ := p.GetString(0)
		return in, exerciseRunTaskEngine(in, mode, rec, res)
	})}
}

func exerciseRunTaskEngine(in RunInputAdmission, mode string, rec *brine.Recorder, res brine.Resources) (resultErr error) {
	if in.Err != nil || in.Source.Candidate == nil {
		return fmt.Errorf("input admission failed: %v", in.Err)
	}
	jdb := in.Source.Start.DB
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	definition, found, err := factory.Definition(in.Run.ID)
	if err != nil || !found {
		return fmt.Errorf("missing consuming definition: %v", err)
	}
	task := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	plan := atc.Plan{ID: "engine-input", Task: &atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: append([]atc.RunInput(nil), task.RunInputs...), RunResult: task.RunResult, Config: task.Config}}
	// A literal rootfs reference requires no image resolver or model call.
	configCopy := *plan.Task.Config
	configCopy.RootfsURI = "docker:///busybox"
	plan.Task.Config = &configCopy
	var buildID int
	if err = jdb.Conn.QueryRow(`SELECT id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, in.Run.ID, definition.Materialized.Jobs[0].Name).Scan(&buildID); err != nil {
		return err
	}
	build, found, err := jdb.BuildFactory.Build(buildID)
	if err != nil || !found {
		return fmt.Errorf("missing task build: %v", err)
	}
	if started, err := build.Start(plan); err != nil || !started {
		return fmt.Errorf("start task build: started=%v err=%v", started, err)
	}
	build, found, err = jdb.BuildFactory.Build(buildID)
	if err != nil || !found {
		return fmt.Errorf("reload task build: %v", err)
	}
	_, signer, config, err := configureRunReadPlane(in.Source, rec, res)
	if err != nil {
		return err
	}
	config.OutputPlaneEnabled = true
	config.HangarEnabled = true
	config.OutputActivationEpoch = int64(hangarEpoch)
	config.ArtifactDaemonHostPath = in.Source.Start.Daemon.Output.Root
	client := in.Source.Candidate.Runtime.Client
	source := jetbridge.NewOutputSource(client, config, in.Source.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Source.Start.Daemon.ControlPublic)}}}
	starter := &runs.ExecutionStarter{Conn: jdb.Conn, Factory: factory, Source: source, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys}
	starter.SetInputReadMinter(signer)
	starter.Output = runs.NewOutputStarter(jdb.Conn, factory, source, int64(hangarEpoch), time.Hour)
	if _, err = jdb.PersistNamedWorker("task-engine-input"); err != nil {
		return err
	}
	workerDB := atcworker.DB{WorkerFactory: jdb.WorkerFactory, TeamFactory: jdb.TeamFactory, VolumeRepo: jdb.VolumeRepository, LockFactory: jdb.LockFactory}
	cluster, err := getRealCluster(res)
	if err != nil {
		return err
	}
	runtimeFactory := atcworker.DefaultFactory{DB: workerDB, K8sClientset: client, K8sConfig: &config, K8sExecutor: jetbridge.NewSPDYExecutor(client, cluster.env.Config), K8sExecutionPreparer: starter, K8sOutputControls: jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(client), in.Source.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))}
	pool := atcworker.NewPool(runtimeFactory, workerDB)
	var options []engine.CoreStepFactoryOption
	if mode != "missing admission port" {
		options = append(options, engine.WithCoreRunTaskPreparer(starter))
	}
	switch mode {
	case "changed route":
		plan.Task.RunInputs[0].Name = "other"
	case "changed task":
		plan.Task.TaskID = freshUUID()
	case "aborted build":
		if err = build.MarkAsAborted(); err != nil {
			return err
		}
	}
	core := engine.NewCoreStepFactory(pool, atcworker.NewStreamer(compression.NewGzipCompression()), jdb.LockFactory, jdb.TeamFactory, jdb.BuildFactory, nil, nil, atc.ContainerLimits{}, atc.ContainerLimits{}, 0, 0, 0, 0, options...)
	stepper, err := engine.NewStepperFactory(core, "http://jetbridge.test", nil, policy.NoopChecker{}, jdb.WorkerFactory, jdb.LockFactory, nil, nil, nil).StepperForBuild(build)
	if err != nil {
		return err
	}
	state := atcexec.NewRunState(stepper, vars.StaticVariables{})
	ctx, cancel := context.WithTimeout(execLogger("run-task-engine"), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := stepper(plan).Run(ctx, state); done <- err }()
	// Join the actual task step before fixture cleanup, including its stop path.
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(35 * time.Second):
			resultErr = fmt.Errorf("task engine did not stop after fixture cancellation")
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var pod *corev1.Pod
	var handle string
	for pod == nil {
		select {
		case err := <-done:
			done <- err
			if mode != "live" {
				expected := error(atc.ErrInvalidRunInputs)
				if mode == "missing admission port" {
					expected = atc.ErrRunResultsUnavailable
				}
				if mode == "aborted build" {
					expected = output.ErrConflict
				}
				if !errors.Is(err, expected) {
					return fmt.Errorf("task engine refused %s for the wrong reason: %v", mode, err)
				}
				var count int
				if err := jdb.Conn.QueryRow(`SELECT count(*) FROM containers WHERE build_id=$1`, buildID).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("refused task left a container")
				}
				return nil
			}
			return fmt.Errorf("task engine returned before creating its managed-input Pod: %v", err)
		case <-ctx.Done():
			return fmt.Errorf("task engine produced no input Pod: %w", ctx.Err())
		case <-ticker.C:
			list, err := client.CoreV1().Pods(config.Namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			for i := range list.Items {
				candidate := &list.Items[i]
				// Match the actual database-owned container rather than an arbitrary Pod.
				rows, err := jdb.Conn.Query(`SELECT handle FROM containers WHERE build_id=$1 AND plan_id=$2`, buildID, string(plan.ID))
				if err != nil {
					return err
				}
				for rows.Next() {
					var h string
					if err = rows.Scan(&h); err != nil {
						rows.Close()
						return err
					}
					if candidate.Labels["concourse.ci/handle"] == h {
						pod = candidate.DeepCopy()
						handle = h
					}
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
			}
		}
	}
	TrackDisposer(rec, "the task-engine pod "+pod.Name, func() error {
		return releasedIfGone(client.CoreV1().Pods(config.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}))
	})
	if mode != "live" {
		return fmt.Errorf("task engine created a Pod for %s", mode)
	}
	if task.RunResult != nil {
		tx, err := jdb.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		selected, found, err := factory.OutputTask(ctx, tx, buildID, task.TaskID)
		db.Rollback(tx)
		if err != nil || !found || !selected.Record.Source.Reserved() {
			return fmt.Errorf("engine task did not retain its reserved result source: %v", err)
		}
		expected := filepath.Join(config.ArtifactDaemonHostPath, "steps", selected.Record.Source.Directory)
		count := 0
		for _, volume := range pod.Spec.Volumes {
			if volume.HostPath != nil && volume.HostPath.Path == expected {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("engine task does not mount its one reserved result source")
		}
	}
	return verifyRunInputPod(ctx, in, handle, pod, task)
}
