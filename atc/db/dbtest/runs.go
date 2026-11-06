package dbtest

import (
	"context"

	"github.com/concourse/concourse/atc/db"
)

// CreateRun admits one Run of template in its own transaction, with no
// invocation record: the fixture a spec uses when it is about the Run's
// payload, builds or lifecycle rather than about admission. No Run is created
// without the output plane in service, so it puts the plane there first, as
// the web's startup write does.
func CreateRun(conn db.DbConn, factory db.PipelineRunFactory, ctx context.Context, template db.Pipeline, params db.RunParams, createdBy string) (db.RunCreation, error) {
	if _, err := db.SetHangarEnabled(ctx, conn, true); err != nil {
		return db.RunCreation{}, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return db.RunCreation{}, err
	}
	defer db.Rollback(tx)
	creation, err := factory.CreateRunInTx(ctx, tx, template, params, createdBy, db.RunCreationOpts{HangarOutput: true})
	if err != nil {
		return db.RunCreation{}, err
	}
	if err := tx.Commit(); err != nil {
		return db.RunCreation{}, err
	}
	// Best effort, exactly as CreatePipelineRunV2 treats it: the Run is
	// committed and every component also polls, so a lost wakeup delays the
	// Run rather than failing its creation. PipelineRunFactory's specs pin
	// that a creation stays successful when the NOTIFY fails, through this
	// fixture; the API logs the failure, a fixture has no logger to log to.
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
