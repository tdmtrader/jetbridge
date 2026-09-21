package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/output"
)

func RunCancellationFinalityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputRuntime, CancellationLeaseResult]("the periodic cancellation component recovers a lost notification", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseRunCancellationPoll(in)}, nil
		}),
		brine.DefineMap[RunOutputCandidate, CancellationLeaseResult]("cancellation recovers from a refused publication commit", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseCancelledPublicationRollback(in)}, nil
		}),
		brine.DefineMap[RunOutputRuntime, CancellationLeaseResult]("cancellation workers settle its remaining {string}", func(in RunOutputRuntime, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseRunCancellationFinality(in, mode)}, nil
		}),
		brine.DefineMap[RunOutputCandidate, CancellationLeaseResult]("cancellation workers settle its hidden review", func(in RunOutputCandidate, _ brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			err := exerciseRunCancellationFinality(in.Runtime, "hidden candidate")
			if err == nil {
				claims, _, readErr := in.claims()
				err = readErr
				if err == nil && (len(claims) != 1 || claims[0].Active()) {
					err = fmt.Errorf("aborted publication did not release the retained hidden claim")
				}
			}
			return CancellationLeaseResult{Err: err}, nil
		}),
		CheckThat[CancellationLeaseResult]("its aborted Run is immutable and has no public results", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("its unresolved source prevents an aborted publication", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseRunCancellationPoll(in RunOutputRuntime) error {
	if runs.CancellationPollInterval <= 0 || runs.CancellationPollInterval > 30*time.Second {
		return fmt.Errorf("cancellation has no bounded periodic fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	conn := in.Start.DB.Conn
	row, err := db.NewComponentFactory(conn).CreateOrUpdate(atc.Component{Name: atc.ComponentRunCancellation})
	if err != nil {
		return err
	}
	worker := cancellationFinalityWorker(in)
	passes := make(chan error, 4)
	runner := component.Runner{
		Logger: lager.NewLogger("cancellation-brine"), Interval: runs.CancellationPollInterval, Component: row, Bus: conn.Bus(),
		Schedulable: &component.Coordinator{Locker: in.Start.DB.LockFactory, Component: row, Runnable: component.RunFunc(func(ctx context.Context) error {
			err := worker.Run(ctx)
			select {
			case passes <- err:
			default:
			}
			return err
		})},
	}
	signals, ready, done := make(chan os.Signal, 1), make(chan struct{}), make(chan error, 1)
	go func() { done <- runner.Run(signals, ready) }()
	defer func() {
		signals <- os.Interrupt
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-passes:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// Install a valid accepted fence without issuing NOTIFY. This is deliberate
	// notification-loss injection against real PostgreSQL, after the runner's
	// initial pass; startup execution cannot satisfy the periodic assertion.
	accepted := time.Now()
	if _, err = conn.ExecContext(ctx, `UPDATE pipeline_runs SET cancel_requested_at=clock_timestamp(),cancel_requested_by='owner' WHERE id=$1`, in.Start.Creation.Run.ID()); err != nil {
		return err
	}
	factory := db.NewPipelineRunFactory(conn, in.Start.DB.LockFactory)
	for {
		select {
		case err := <-passes:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		if time.Since(accepted) < runs.CancellationPollInterval-time.Second {
			return fmt.Errorf("startup or a notification satisfied the periodic test")
		}
		result, found, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
		if err != nil {
			return err
		}
		if found {
			if result.Status != atc.RunStatusAborted || result.Results == nil || len(result.Results) != 0 {
				return fmt.Errorf("periodic worker exposed an invalid aborted result")
			}
			return nil
		}
	}
}

func cancellationFinalityWorker(in RunOutputRuntime) runs.CancellationWorker {
	worker := cancellationSourceWorker(in)
	worker.Actions = append(worker.Actions.(runs.CancellationActionSet), runs.CancellationActionFunc(worker.Factory.ExecuteCancellationFinality))
	return worker
}

func exerciseRunCancellationFinality(in RunOutputRuntime, mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if mode == "owned check" {
		check, err := requestRunCheck(in.Start, "persisted")
		if err != nil {
			return err
		}
		if check.Err != nil {
			return check.Err
		}
		if check.Build == nil {
			return fmt.Errorf("no owned check was created")
		}
	}
	var finishedID int
	if mode == "naturally completed build" {
		build := in.Start.Creation.EntryBuilds[0]
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return err
		}
		finishedID = build.ID()
	}
	if _, err := acceptRunCancellation(in.Start, "owner", nil, false); err != nil {
		return err
	}
	if mode == "expired worker" {
		worker := cancellationFinalityWorker(in)
		actions := worker.Actions
		fired := false
		worker.Actions = runs.CancellationActionFunc(func(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
			if op.Kind == db.CancelBuild && !fired {
				// Expire the real claimed lease before its real owner writes the
				// build. The final commit check must roll every build fact back.
				fired = true
				if _, err := in.Start.DB.Conn.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
					return db.CancellationUnavailable, err
				}
			}
			return actions.ExecuteCancellationOperation(ctx, lease, op)
		})
		err := worker.Run(ctx)
		if !fired || !errors.Is(err, db.ErrRunCancellationLeaseLost) {
			return fmt.Errorf("expired worker was not fenced at its build commit: %v", err)
		}
		var changed bool
		if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=$1 AND (aborted OR completed))`, in.Start.Creation.Run.ID()).Scan(&changed); err != nil {
			return err
		}
		if changed {
			return fmt.Errorf("expired worker retained an abort or completion")
		}
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	// An abandoned claim keeps its original ten-second operation deadline;
	// the terminal operation also retains its independently bounded backoff.
	for pass := 0; pass < 30; pass++ {
		worker := cancellationFinalityWorker(in)
		if err := worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		result, found, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
		if err != nil {
			return err
		}
		if mode == "unresolved source" {
			if found {
				return fmt.Errorf("ambiguous source acquired a terminal Run result")
			}
			var changed bool
			if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM builds WHERE id=$1 AND (aborted OR completed))`, in.Start.Creation.EntryBuilds[0].ID()).Scan(&changed); err != nil {
				return err
			}
			if changed {
				return fmt.Errorf("build was aborted before its unresolved source settled")
			}
			if pass == 3 {
				return nil
			}
		} else if found {
			if result.Status != atc.RunStatusAborted || result.CompletedAt.IsZero() || result.Version == "" || result.Results == nil || len(result.Results) != 0 {
				return fmt.Errorf("cancellation published an incomplete or nonempty result")
			}
			var open, unchanged bool
			if err = in.Start.DB.Conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=$1 AND (NOT completed OR status IN ('pending','started'))),
 NOT EXISTS(SELECT 1 FROM builds WHERE id=$2 AND status<>'succeeded')`, in.Start.Creation.Run.ID(), finishedID).Scan(&open, &unchanged); err != nil {
				return err
			}
			if open || !unchanged {
				return fmt.Errorf("publication left open builds or changed a completed outcome")
			}
			before, _ := json.Marshal(result)
			worker = cancellationFinalityWorker(in)
			if err = worker.Run(ctx); err != nil {
				return err
			}
			outcome, err := acceptRunCancellation(in.Start, "second-owner", nil, false)
			if err != nil {
				return err
			}
			if outcome != atc.RunCancelAlreadyRequested {
				return fmt.Errorf("terminal retry lost cancellation request precedence")
			}
			again, found, err := factory.TerminalResult(ctx, in.Start.Creation.Run.ID())
			if err != nil {
				return err
			}
			after, _ := json.Marshal(again)
			if !found || string(before) != string(after) {
				return fmt.Errorf("cancellation replay changed immutable publication")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	return fmt.Errorf("complete cancellation worker did not publish its quiescent aborted Run")
}

func exerciseCancelledPublicationRollback(in RunOutputCandidate) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := in.Runtime.Start.DB.Conn
	if _, err := conn.ExecContext(ctx, `CREATE FUNCTION brine_refuse_cancel_publication() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.status='aborted' THEN RAISE EXCEPTION 'refused terminal commit' USING ERRCODE='JB004'; END IF; RETURN NULL; END $$;
 CREATE CONSTRAINT TRIGGER brine_refuse_cancel_publication AFTER UPDATE ON pipeline_runs DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION brine_refuse_cancel_publication()`); err != nil {
		return err
	}
	if _, err := acceptRunCancellation(in.Runtime.Start, "owner", nil, false); err != nil {
		return err
	}
	found := false
	for pass := 0; pass < 8; pass++ {
		worker := cancellationFinalityWorker(in.Runtime)
		err := worker.Run(ctx)
		if errors.Is(err, output.ErrIncomplete) {
			found = true
			break
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
	}
	if !found {
		return fmt.Errorf("terminal publication never reached its deferred commit check")
	}
	var visible bool
	if err := conn.QueryRowContext(ctx, `SELECT status<>'running' OR result_manifest IS NOT NULL OR completed_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, in.Runtime.Start.Creation.Run.ID()).Scan(&visible); err != nil {
		return err
	}
	claims, _, err := in.claims()
	if err != nil {
		return err
	}
	if visible || len(claims) != 1 || !claims[0].Active() {
		return fmt.Errorf("refused publication exposed a terminal result or released its candidate")
	}
	if _, err = conn.ExecContext(ctx, `DROP TRIGGER brine_refuse_cancel_publication ON pipeline_runs; DROP FUNCTION brine_refuse_cancel_publication()`); err != nil {
		return err
	}
	if err = exerciseRunCancellationFinality(in.Runtime, "recovered commit"); err != nil {
		return err
	}
	claims, _, err = in.claims()
	if err != nil {
		return err
	}
	if len(claims) != 1 || claims[0].Active() {
		return fmt.Errorf("recovered aborted publication did not release its candidate")
	}
	return nil
}
