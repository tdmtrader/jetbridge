package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/db"
)

const CancellationPollInterval = 15 * time.Second

// CancellationActions performs exactly one claimed operation. The worker has
// committed its claim and holds no database transaction when it invokes an action.
// Actions return only closed retry codes; external error text is never stored.
type CancellationActions interface {
	ExecuteCancellationOperation(context.Context, db.RunCancellationLease, db.RunCancellationOperation) (db.RunCancellationDebt, error)
}

// CancellationWorker advances a bounded fair pass. OwnerID is stable for one
// process and changes on restart. Ownership and all cursors live in PostgreSQL.
// Activation wiring supplies the complete action set; nil actions cannot run.
type CancellationWorker struct {
	Conn    db.DbConn
	Factory db.PipelineRunFactory
	OwnerID string
	Actions CancellationActions
}

func (w *CancellationWorker) Run(parent context.Context) error {
	if w.Conn == nil || w.Factory == nil || w.Actions == nil || w.OwnerID == "" {
		return fmt.Errorf("incomplete cancellation worker")
	}
	ctx, cancel := context.WithTimeout(parent, db.RunCancellationPassLimit)
	defer cancel()
	var lease db.RunCancellationLease
	var owned bool
	if err := w.transaction(ctx, func(tx db.Tx) error {
		var err error
		lease, owned, err = w.Factory.ClaimRunCancellationLease(ctx, tx, w.OwnerID, db.RunCancellationLeaseTerm)
		return err
	}); err != nil {
		return err
	}
	if !owned {
		return nil
	}
	seen := map[int]bool{}
	idle := map[int]bool{}
	var errs []error
	// Selecting one Run at a time advances the durable Run cursor before an
	// operation may time out. Advancing a whole 50-Run page up front would
	// repeatedly starve its tail when the first few operations use the budget.
	for visits := 0; visits < db.RunCancellationOperationLimit && len(seen) < db.RunCancellationRunLimit; visits++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		var ids []int
		if err := w.transaction(ctx, func(tx db.Tx) error {
			var err error
			ids, err = w.Factory.PendingRunCancellations(ctx, tx, lease, 1)
			return err
		}); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if len(ids) == 0 {
			break
		}
		runID := ids[0]
		if idle[runID] {
			break
		}
		seen[runID] = true
		if err := w.transaction(ctx, func(tx db.Tx) error {
			_, err := w.Factory.DiscoverRunCancellation(ctx, tx, lease, runID, db.RunCancellationOperationLimit)
			return err
		}); err != nil {
			errs = append(errs, fmt.Errorf("discover cancellation for Run %d: %w", runID, err))
			idle[runID] = true
			continue
		}
		claimStarted := time.Now()
		var op db.RunCancellationOperation
		var found bool
		if err := w.transaction(ctx, func(tx db.Tx) error {
			var err error
			op, found, err = w.Factory.ClaimRunCancellationOperation(ctx, tx, lease, runID)
			return err
		}); err != nil {
			errs = append(errs, fmt.Errorf("claim cancellation for Run %d: %w", runID, err))
			idle[runID] = true
			continue
		}
		if !found {
			idle[runID] = true
			continue
		}
		idle = map[int]bool{}
		// Translate the database's residual term into a local monotonic timer.
		// Subtract the whole request/commit duration conservatively; comparing
		// a database deadline directly to the web's wall clock permits skew.
		term := op.Deadline.Sub(op.DatabaseNow) - time.Since(claimStarted)
		callCtx, stop := context.WithTimeout(ctx, term)
		debt := db.CancellationTimeout
		var err error
		if callCtx.Err() == nil {
			debt, err = w.Actions.ExecuteCancellationOperation(callCtx, lease, op)
		}
		if callCtx.Err() != nil {
			debt = db.CancellationTimeout
		} else if err != nil && debt == db.CancellationDone {
			debt = db.CancellationUnavailable
		}
		stop()
		if err != nil {
			errs = append(errs, fmt.Errorf("cancellation %s for Run %d: %w", op.Kind, runID, err))
		}
		if err := w.transaction(ctx, func(tx db.Tx) error { return w.Factory.RecordRunCancellationProgress(ctx, tx, lease, op, debt) }); err != nil {
			errs = append(errs, fmt.Errorf("record cancellation %s for Run %d: %w", op.Kind, runID, err))
		}
	}
	return errors.Join(errs...)
}

func (w *CancellationWorker) transaction(ctx context.Context, fn func(db.Tx) error) error {
	tx, err := w.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
