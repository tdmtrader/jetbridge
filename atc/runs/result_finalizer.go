package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc/db"
)

// ResultFinalizer runs after capture recovery so terminal publication can take
// the complete domain lock prefix in its own transaction, even when the original
// build tracker died. Run remains the only execution and result identity.
type ResultFinalizer struct {
	Conn    db.DbConn
	Factory db.PipelineRunFactory
}

func (f *ResultFinalizer) Run(ctx context.Context) error {
	var errs []error
	afterID := 0
	for {
		tx, err := f.Conn.BeginTx(ctx, nil)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		ids, err := f.Factory.PendingOutputRuns(ctx, tx, afterID, 100)
		db.Rollback(tx)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		if len(ids) == 0 {
			return errors.Join(errs...)
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			afterID = id
			if err := f.finalize(ctx, id); err != nil {
				errs = append(errs, fmt.Errorf("finalize Run %d: %w", id, err))
			}
		}
	}
}

func (f *ResultFinalizer) finalize(ctx context.Context, id int) error {
	tx, err := f.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	completed, err := f.Factory.FinalizeOutputRun(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return db.HangarCommitError(err)
	}
	if completed {
		f.Factory.AfterRunCompleted()
	}
	return nil
}
