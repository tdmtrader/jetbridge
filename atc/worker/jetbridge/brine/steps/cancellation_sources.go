package steps

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func CancellationSourcesDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineCheck[RunOutputRuntime]("its real worker binds the producer execution", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			row, err := in.Start.DB.PersistNamedWorker("capture-execution")
			if err != nil {
				return err
			}
			factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
			keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
			worker := jetbridge.NewWorker(row, in.Client, in.Config, jetbridge.WorkerDeps{
				ExecutionPreparer: &runs.ExecutionStarter{Conn: in.Start.DB.Conn, Factory: factory, Source: in.source(), Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys},
			})
			build := in.Start.Creation.EntryBuilds[0]
			spec := in.Spec
			spec.TeamID, spec.Type, spec.ExecutionControl = build.TeamID(), db.ContainerTypeTask, in.Control
			_, _, err = worker.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(build.ID(), "capture-step", build.TeamID()), db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}, spec, nil)
			if err != nil {
				return err
			}
			var matched bool
			if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_executions WHERE build_id=$1 AND plan_id='capture-step' AND handoff_id=$2)`, build.ID(), string(in.Control.Capture.HandoffID)).Scan(&matched); err != nil {
				return err
			}
			if !matched {
				return fmt.Errorf("worker did not bind the selected output execution")
			}
			return nil
		}),
		CheckThat[CancellationLeaseResult]("its producer execution is closed without a fabricated process outcome", func(in CancellationLeaseResult) error { return in.Err }),
		brine.DefineMap[RunOutputRuntime, CancellationLeaseResult]("cancellation encounters a deferred policy refusal and retries after it clears", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseCancellationCommitRefusal(in)}, nil
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("cancellation encounters its {string}", func(in RunOutputRuntime, p brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			mode, _ := p.GetString(0)
			in.Err = exerciseCancellationSourceRefusal(in, mode)
			return in, in.Err
		}),
		CheckThat[RunOutputRuntime]("cancellation preserves the unresolved source", func(in RunOutputRuntime) error { return in.Err }),
		brine.DefineMap[RunOutputRuntime, CancellationLeaseResult]("cancellation workers settle its never-started source", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			err := exerciseCancellationSources(in)
			if err == nil {
				var executions, outcomes int
				var closed bool
				err = in.Start.DB.Conn.QueryRow(`SELECT run_execution_closed($1),
 (SELECT count(*) FROM pipeline_run_executions WHERE build_id=$1),
 (SELECT count(*) FROM pipeline_run_execution_closures c JOIN pipeline_run_executions e USING(execution_id,execution_fence) WHERE e.build_id=$1)`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&closed, &executions, &outcomes)
				if err == nil && (!closed || outcomes != 0) {
					err = fmt.Errorf("released never-started producer has closed=%t, %d executions and %d fabricated outcomes", closed, executions, outcomes)
				}
			}
			return CancellationLeaseResult{Err: err}, nil
		}),
		CheckThat[CancellationLeaseResult]("the worker has retained closure and the daemon has released the source", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseCancellationCommitRefusal(in RunOutputRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	in.Start.Record, err = in.readSource()
	if err != nil {
		return err
	}
	// A real deferred PostgreSQL refusal reaches the coordinator at COMMIT,
	// after its repository statements have all succeeded.
	if _, err := in.Start.DB.Conn.ExecContext(ctx, `CREATE FUNCTION brine_cancel_policy() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'source policy is at risk' USING ERRCODE='JB002'; END $$;
 CREATE CONSTRAINT TRIGGER brine_cancel_policy AFTER INSERT ON hangar_pre_reservation_cancel_dispositions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION brine_cancel_policy()`); err != nil {
		return err
	}
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationSourceWorker(in)
	var refused bool
	for pass := 0; pass < 8; pass++ {
		err := worker.Run(ctx)
		if errors.Is(err, output.ErrAtRisk) {
			refused = true
			break
		}
		if err != nil && !onlyExternalCancellationWork(err) {
			return fmt.Errorf("deferred policy refusal lost its type: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	if !refused {
		return fmt.Errorf("cancellation never reached its deferred policy check")
	}
	var facts int
	if err := in.Start.DB.Conn.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM hangar_pre_reservation_cancel_dispositions WHERE handoff_id=$1)+
 (SELECT count(*) FROM pipeline_run_output_releases WHERE handoff_id=$1)`, string(in.Start.Record.HandoffID)).Scan(&facts); err != nil {
		return err
	}
	if facts != 0 {
		return fmt.Errorf("refused commit left %d disposition or release facts", facts)
	}
	if _, err := in.Start.DB.Conn.ExecContext(ctx, `DROP TRIGGER brine_cancel_policy ON hangar_pre_reservation_cancel_dispositions; DROP FUNCTION brine_cancel_policy()`); err != nil {
		return err
	}
	return exerciseCancellationSources(in)
}

func exerciseCancellationSourceRefusal(in RunOutputRuntime, mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	in.Start.Record, err = in.readSource()
	if err != nil {
		return err
	}
	if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationSourceWorker(in)
	err = worker.Run(ctx)
	if mode == "replacement node" {
		if !errors.Is(err, output.ErrInvalidIdentity) {
			return fmt.Errorf("replacement node was not refused: %v", err)
		}
	} else if err != nil && !onlyExternalCancellationWork(err) {
		return err
	}
	var facts int
	if err = in.Start.DB.Conn.QueryRow(`SELECT
	 (SELECT count(*) FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=$1)+
	 (SELECT count(*) FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1)+
	 (SELECT count(*) FROM pipeline_run_output_finishes WHERE handoff_id=$1)+
	 (SELECT count(*) FROM pipeline_run_output_releases WHERE handoff_id=$1)`, string(in.Start.Record.HandoffID)).Scan(&facts); err != nil {
		return err
	}
	if facts != 0 {
		return fmt.Errorf("unresolved source acquired %d cleanup facts", facts)
	}
	return nil
}

func exerciseCancellationSources(in RunOutputRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	in.Start.Record, err = in.readSource()
	if err != nil {
		return err
	}
	if _, err = acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	worker := cancellationSourceWorker(in)
	handoff := string(in.Start.Record.HandoffID)
	for pass := 0; pass < 12; pass++ {
		if err = worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		var classified, closed, released bool
		if err = in.Start.DB.Conn.QueryRow(`SELECT
   EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=$1),
   EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1 AND classification='never_started'),
   EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1)`, handoff).Scan(&classified, &closed, &released); err != nil {
			return err
		}
		if !classified && pass > 0 {
			return fmt.Errorf("real cancellation worker left the source unclassified")
		}
		if closed && released {
			record, err := in.readSource()
			if err != nil {
				return err
			}
			if !record.ReleaseAcknowledged {
				return fmt.Errorf("release did not settle the generic handoff")
			}
			client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, record.ActivationEpoch)
			observed, err := client.Classify(ctx, record.Execution)
			if err != nil {
				return err
			}
			if observed.Classification != executioncontrol.ClassificationNeverStarted || observed.Acknowledgement != nil {
				return fmt.Errorf("worker fabricated a process outcome")
			}
			_, err = client.RecordStart(ctx, record.Execution, "delayed-pod", "delayed-supervisor")
			if err == nil {
				return fmt.Errorf("released source still allowed delayed execution")
			}
			var status string
			if err = in.Start.DB.Conn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, in.Start.Creation.Run.ID()).Scan(&status); err != nil {
				return err
			}
			if status != "running" {
				return fmt.Errorf("source closure alone terminalized the Run")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
		// A fresh observer uses the retained queue and source facts, not memory
		// from the preceding pass. No transaction spans any daemon call; this
		// fixture has one real database connection.
		worker = cancellationSourceWorker(in)
	}
	return fmt.Errorf("real cancellation worker never settled its source")
}

func cancellationSourceWorker(in RunOutputRuntime) runs.CancellationWorker {
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	sources := &runs.CancellationSources{Conn: in.Start.DB.Conn, Factory: factory, Repository: (RunOutputFinish{Start: in.Start}).repository(), Source: in.source(), Coordinator: &hangaroutput.Coordinator{OwnerID: "source-worker", HoldVerifier: keys}, Verifier: keys}
	executions := &runs.CancellationExecutions{Conn: in.Start.DB.Conn, Factory: factory, Source: in.source(), Verifier: keys}
	return runs.CancellationWorker{Conn: in.Start.DB.Conn, Factory: factory, OwnerID: "source-worker", Actions: runs.CancellationActionSet{factory, sources, executions}}
}
