package db

import (
	"code.cloudfoundry.org/lager/v3"
	sq "github.com/Masterminds/squirrel"
)

// runCheckGCMarker is the transaction-local allowance the evidence guards
// honour for exactly one thing: deleting a closed check or image get
// execution of a completed Run check build, with its witnesses. Task and put
// evidence, and any evidence of a job build, stays deletable only by a team
// purge.
const runCheckGCMarker = `SELECT set_config('concourse.pipeline_run_check_gc', 'on', true)`

// inertRunCheckEvidence holds for a completed Run check build b whose every
// execution is inert: a closed check, or a closed get that fetched the
// check's custom image inside the check build (FetchImage does this on a
// resource cache miss for an image that is not a registry-image). A check
// produces no Run result, and nothing can interrupt or fence an execution
// once it has a closure. A Run output start naming the build or an execution
// is never inert.
const inertRunCheckEvidence = `
	b.completed AND b.pipeline_run_id IS NOT NULL AND b.run_job_name IS NULL
	AND NOT EXISTS (
	  SELECT 1 FROM pipeline_run_executions other
	  WHERE other.build_id = b.id AND (other.kind NOT IN ('check', 'get') OR other.handoff_id IS NOT NULL OR NOT EXISTS (
	    SELECT 1 FROM pipeline_run_execution_closures c
	    WHERE c.execution_id = other.execution_id AND c.execution_fence = other.execution_fence)))
	AND NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s WHERE s.build_id = b.id)`

// closedRunChecks selects executed Run checks whose execution evidence is
// inert, and which the ordinary rules would collect: a resource check that
// is neither its scope's last check nor its resource's newest, and a
// resource-type check that is not its type's newest.
const closedRunChecks = `
	SELECT b.id FROM builds b
	WHERE ` + inertRunCheckEvidence + `
	  AND EXISTS (SELECT 1 FROM pipeline_run_executions e WHERE e.build_id = b.id)
	  AND (
	    (b.resource_id IS NOT NULL
	      AND NOT EXISTS (SELECT 1 FROM resource_config_scopes s WHERE s.last_check_build_id = b.id)
	      AND EXISTS (SELECT 1 FROM builds n WHERE n.resource_id = b.resource_id AND n.id > b.id))
	    OR
	    (b.resource_type_id IS NOT NULL
	      AND EXISTS (SELECT 1 FROM builds n WHERE n.resource_type_id = b.resource_type_id AND n.id > b.id)))`

// deleteClosedRunChecks collects superseded executed Run checks with their
// execution evidence and events, a batch per transaction.
func (cl *checkLifecycle) deleteClosedRunChecks(logger lager.Logger) error {
	for batch := 0; ; batch++ {
		selected, deleted, err := cl.deleteClosedRunCheckBatch(logger)
		if err != nil {
			return err
		}
		logger.Debug("deleted-executed-run-checks", lager.Data{"count": deleted, "batch": batch})
		// A batch full of checks that refused deletion would come back
		// unchanged; stop rather than spin on it.
		if selected < CheckDeleteBatchSize || deleted == 0 {
			return nil
		}
	}
}

// deleteClosedRunCheckBatch locks its batch with SKIP LOCKED, so a check held
// by another transaction is left for a later pass instead of stalling this
// one. It deletes the batch as a whole under a savepoint and, if anything in
// it is refused, retries check by check, so one refusal costs only that check.
func (cl *checkLifecycle) deleteClosedRunCheckBatch(logger lager.Logger) (int, int, error) {
	tx, err := cl.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer Rollback(tx)
	if _, err = tx.Exec(runCheckGCMarker); err != nil {
		return 0, 0, err
	}
	rows, err := tx.Query(`SELECT id FROM builds WHERE id IN (`+closedRunChecks+`)
		ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, CheckDeleteBatchSize)
	if err != nil {
		return 0, 0, err
	}
	var ids []int
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			Close(rows)
			return 0, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	Close(rows)
	if err != nil || len(ids) == 0 {
		return 0, 0, err
	}

	deleted := len(ids)
	if err = deleteRunChecksAt(tx, "run_check_batch", ids); err != nil {
		deleted = 0
		for _, id := range ids {
			if err := deleteRunChecksAt(tx, "run_check", []int{id}); err != nil {
				logger.Error("failed-to-delete-executed-run-check", err, lager.Data{"build": id})
				continue
			}
			deleted++
		}
	}
	return len(ids), deleted, tx.Commit()
}

// deleteRunChecksAt deletes checks, their executions (whose witnesses go by
// cascade) and their events under a savepoint, rolling back to it on refusal.
func deleteRunChecksAt(tx Tx, savepoint string, ids []int) error {
	if _, err := tx.Exec(`SAVEPOINT ` + savepoint); err != nil {
		return err
	}
	for _, statement := range []sq.DeleteBuilder{
		psql.Delete("pipeline_run_executions").Where(sq.Eq{"build_id": ids}),
		psql.Delete("builds").Where(sq.Eq{"id": ids}),
		psql.Delete("check_build_events").Where(sq.Eq{"build_id": ids}),
	} {
		if _, err := statement.RunWith(tx).Exec(); err != nil {
			if _, rollbackErr := tx.Exec(`ROLLBACK TO SAVEPOINT ` + savepoint); rollbackErr != nil {
				return rollbackErr
			}
			return err
		}
	}
	_, err := tx.Exec(`RELEASE SAVEPOINT ` + savepoint)
	return err
}
