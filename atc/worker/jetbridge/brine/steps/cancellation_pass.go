package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
)

func CancellationPassDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, CancellationLeaseResult]("the cancellation worker exercises {string} with real scheduler actions", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseCancellationPass(in, mode)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the worker advances only owned and claimed scheduling work", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseCancellationPass(in RunOutputStart, mode string) error {
	factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
	actions, ok := any(factory).(runs.CancellationActions)
	if !ok {
		return fmt.Errorf("Run factory has no real cancellation scheduler action")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var before int
	query := `SELECT count(*) FROM jobs j JOIN pipelines p ON p.id=j.pipeline_id WHERE p.pipeline_run_id=$1 AND j.schedule_requested>j.last_scheduled`
	if err := in.DB.Conn.QueryRow(query, in.Creation.Run.ID()).Scan(&before); err != nil {
		return err
	}
	if before < 1 {
		return fmt.Errorf("fixture has no actual scheduler debt")
	}
	if mode == "over 100 operations" {
		job, err := cancellationJob(in)
		if err != nil {
			return err
		}
		for i := 0; i < 120; i++ {
			if _, err := job.CreateBuild("bounded-pass"); err != nil {
				return err
			}
		}
	}
	var lastRun int
	if mode == "over 50 Runs" {
		team, found, err := in.DB.TeamFactory.FindTeam("output-start")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing team")
		}
		template, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing template")
		}
		for i := 0; i < 51; i++ {
			tx, err := in.DB.Conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "bounded-pass", db.RunCreationOpts{ActivationEpoch: int64(hangarEpoch)})
			if err == nil {
				_, err = factory.AcceptRunCancellation(ctx, tx, creation.Run.ID(), "owner", nil)
			}
			if err != nil {
				db.Rollback(tx)
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			lastRun = creation.Run.ID()
		}
	}
	if mode != "ordinary Run" {
		if _, err := acceptRunCancellation(in, "owner", nil, false); err != nil {
			return err
		}
	}
	if mode == "a competing worker" {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		_, found, err := factory.ClaimRunCancellationLease(ctx, tx, "other-owner", time.Minute)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("fixture owner was not admitted")
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if mode == "one connection" {
		in.DB.Conn.SetMaxOpenConns(1)
	}
	worker := runs.CancellationWorker{Conn: in.DB.Conn, Factory: factory, OwnerID: "scheduler-worker", Actions: actions}
	err := worker.Run(ctx)
	// The real database action explicitly refuses operations that require the
	// external executor/capture handlers. This test supplies no substitute for
	// those handlers and checks that their work remains uncompleted.
	if err != nil && !onlyExternalCancellationWork(err) {
		return err
	}
	if mode == "over 50 Runs" {
		var selected int
		if err := in.DB.Conn.QueryRow(`SELECT count(DISTINCT run_id) FROM pipeline_run_cancellation_operations WHERE attempt_count>0`).Scan(&selected); err != nil {
			return err
		}
		if selected != db.RunCancellationRunLimit {
			return fmt.Errorf("worker pass examined %d Runs", selected)
		}
		if err := worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
		var later bool
		if err := in.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND attempt_count>0)`, lastRun).Scan(&later); err != nil {
			return err
		}
		if !later {
			return fmt.Errorf("later Run starved after the next bounded pass")
		}
		return nil
	}
	if mode == "over 100 operations" {
		var attempts int
		if err := in.DB.Conn.QueryRow(`SELECT sum(attempt_count) FROM pipeline_run_cancellation_operations WHERE run_id=$1`, in.Creation.Run.ID()).Scan(&attempts); err != nil {
			return err
		}
		if attempts != db.RunCancellationOperationLimit {
			return fmt.Errorf("worker attempted %d operations in one pass", attempts)
		}
	}
	if mode == "repeated pass" {
		worker.Factory = db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
		if err := worker.Run(ctx); err != nil && !onlyExternalCancellationWork(err) {
			return err
		}
	}
	var after, completed, untouched int
	if err := in.DB.Conn.QueryRow(query, in.Creation.Run.ID()).Scan(&after); err != nil {
		return err
	}
	if mode == "ordinary Run" || mode == "a competing worker" {
		if after != before {
			return fmt.Errorf("unowned worker closed scheduler debt")
		}
		return nil
	}
	if after != 0 {
		return fmt.Errorf("worker left %d owned scheduler requests", after)
	}
	if err := in.DB.Conn.QueryRow(`SELECT count(*) FILTER(WHERE kind='scheduler_debt_close' AND completed_at IS NOT NULL),count(*) FILTER(WHERE kind<>'scheduler_debt_close' AND completed_at IS NULL) FROM pipeline_run_cancellation_operations WHERE run_id=$1`, in.Creation.Run.ID()).Scan(&completed, &untouched); err != nil {
		return err
	}
	if completed != before || untouched < 1 {
		return fmt.Errorf("worker completed unsupported work or lost scheduler progress: %d, %d", completed, untouched)
	}
	var status string
	if err := in.DB.Conn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, in.Creation.Run.ID()).Scan(&status); err != nil {
		return err
	}
	if status != "running" {
		return fmt.Errorf("bookkeeping published a terminal outcome")
	}
	return nil
}

func onlyExternalCancellationWork(err error) bool {
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range many.Unwrap() {
			if !onlyExternalCancellationWork(child) {
				return false
			}
		}
		return true
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		return onlyExternalCancellationWork(one.Unwrap())
	}
	return errors.Is(err, db.ErrRunCancellationExternalWork)
}
