package dbtest

import (
	"context"

	"github.com/concourse/concourse/atc/db"
)

// RunActivationEpoch is the Run activation epoch CreateRun admits under.
const RunActivationEpoch int64 = 1

// ActivateRuns turns the durable Run activation marker on at
// RunActivationEpoch, through the same reconciliation a web node runs at
// startup. Specs about refused admission turn it off themselves.
func ActivateRuns(conn db.DbConn) error {
	_, err := db.ReconcilePipelineRunActivation(context.Background(), conn, RunActivationEpoch)
	return err
}

// CreateRun admits one Run of template in its own transaction, with no
// invocation record: the fixture a spec uses when it is about the Run's
// payload, builds or lifecycle rather than about admission. It admits under
// whatever epoch the marker already admits, activating RunActivationEpoch if
// nothing has.
func CreateRun(conn db.DbConn, factory db.PipelineRunFactory, ctx context.Context, template db.Pipeline, params db.RunParams, createdBy string) (db.RunCreation, error) {
	var epoch int64
	var enabled bool
	if err := conn.QueryRowContext(ctx, `SELECT epoch, admission_enabled FROM pipeline_run_activation WHERE singleton`).Scan(&epoch, &enabled); err != nil {
		return db.RunCreation{}, err
	}
	if !enabled {
		if epoch < RunActivationEpoch {
			epoch = RunActivationEpoch
		}
		if _, err := db.ReconcilePipelineRunActivation(ctx, conn, epoch); err != nil {
			return db.RunCreation{}, err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return db.RunCreation{}, err
	}
	defer db.Rollback(tx)
	creation, err := factory.CreateRunInTx(ctx, tx, template, params, createdBy, db.RunCreationOpts{ActivationEpoch: epoch})
	if err != nil {
		return db.RunCreation{}, err
	}
	if err := tx.Commit(); err != nil {
		return db.RunCreation{}, err
	}
	_ = factory.AfterRunCreated(ctx, creation)
	return creation, nil
}

// FinalizeRun makes the Run's terminal publication if it has settled and
// announces it, exactly as the Run results component does, and reports
// whether it completed.
func FinalizeRun(ctx context.Context, conn db.DbConn, factory db.PipelineRunFactory, runID int) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer db.Rollback(tx)
	completed, err := factory.FinalizeOutputRun(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if completed {
		factory.AfterRunCompleted()
	}
	return completed, nil
}
