package db

import (
	"context"
	"fmt"
	"strings"
)

// A build closure is an aborted Run build's request to settle the output
// handoffs and close the execution it left open, without cancelling its Run.
// The cancellation worker converges it with Run cancellation's own operations,
// scoped to that build: it never discovers scheduler debt, candidates or
// terminal publication, and it never fences the Run. Candidates need no
// closure operation because ordinary terminal publication releases every
// candidate claim it does not select.

// recordBuildClosure asks for the build's closure. Repeating it is harmless.
func recordBuildClosure(ctx context.Context, tx Tx, runID, buildID int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_build_closures(build_id,run_id) VALUES($1,$2) ON CONFLICT (build_id) DO NOTHING`, buildID, runID)
	return err
}

// runHasOpenBuildClosure reports whether any closure of the Run is still open.
func runHasOpenBuildClosure(ctx context.Context, tx Tx, runID int) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE run_id=$1 AND closed_at IS NULL)`, runID).Scan(&open)
	return open, err
}

// buildHasOpenClosure reports whether the build's closure is still open.
func buildHasOpenClosure(ctx context.Context, tx Tx, buildID int) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE build_id=$1 AND closed_at IS NULL)`, buildID).Scan(&open)
	return open, err
}

// closeOpenBuildClosures closes every open closure of the Run whose work is
// done. It runs in the transaction that records an operation as completed.
func closeOpenBuildClosures(ctx context.Context, tx Tx, runID int) error {
	rows, err := tx.QueryContext(ctx, openBuildClosures, runID)
	if err != nil {
		return err
	}
	var builds []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			Close(rows)
			return err
		}
		builds = append(builds, id)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return err
	}
	for _, id := range builds {
		if _, err := closeBuildClosure(ctx, tx, runID, id); err != nil {
			return err
		}
	}
	return nil
}

// buildClosureSubjects returns the subject query for one operation kind,
// restricted to the builds that the SQL fragment builds selects. $1 is always
// the Run. A kind a build closure never owns has no query.
func buildClosureSubjects(kind RunCancellationKind, builds string) (string, bool) {
	var query string
	switch kind {
	case CancelHandoff:
		query = `SELECT s.handoff_id::text AS subject FROM pipeline_run_output_starts s WHERE s.run_id=$1 AND s.build_id IN (%s)`
	case CancelCapture:
		query = `SELECT c.reservation_id::text AS subject FROM pipeline_run_output_starts s JOIN hangar_capture_reservations c USING(handoff_id) WHERE s.run_id=$1 AND s.build_id IN (%s)`
	case CancelSourceHold:
		query = `SELECT h.source_hold_id::text AS subject FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id) WHERE s.run_id=$1 AND s.build_id IN (%s)`
	case CancelBuild:
		query = `SELECT id::text AS subject FROM builds WHERE pipeline_run_id=$1 AND id IN (%s)`
	case CancelExecution:
		query = `SELECT h.execution_id::text||'/'||h.execution_fence::text AS subject FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id) WHERE s.run_id=$1 AND s.build_id IN (%[1]s)
 UNION SELECT execution_id::text||'/'||execution_fence::text AS subject FROM pipeline_run_executions WHERE run_id=$1 AND build_id IN (%[1]s)`
	default:
		return "", false
	}
	return fmt.Sprintf(query, builds), true
}

// openBuildClosures selects the Run's open closure builds for discovery.
const openBuildClosures = `SELECT build_id FROM pipeline_run_build_closures WHERE run_id=$1 AND closed_at IS NULL`

// closeBuildClosure closes the build's closure once the build has finished
// and none of its closure operations is still incomplete. It runs in the
// transaction that records an operation's progress, so whichever of the
// closure's operations completes last closes it.
func closeBuildClosure(ctx context.Context, tx Tx, runID, buildID int) (bool, error) {
	var pending []string
	for _, kind := range []RunCancellationKind{CancelHandoff, CancelCapture, CancelSourceHold, CancelBuild, CancelExecution} {
		subjects, _ := buildClosureSubjects(kind, `SELECT $2::integer`)
		pending = append(pending, fmt.Sprintf(`SELECT 1 FROM pipeline_run_cancellation_operations op
 WHERE op.run_id=$1 AND op.kind='%s' AND op.completed_at IS NULL AND op.subject IN (%s)`, kind, subjects))
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_run_build_closures SET closed_at=clock_timestamp()
 WHERE build_id=$2 AND run_id=$1 AND closed_at IS NULL
 AND EXISTS(SELECT 1 FROM builds WHERE id=$2 AND completed)
 AND NOT EXISTS(`+strings.Join(pending, "\n UNION ALL ")+`)`, runID, buildID)
	if err != nil {
		return false, err
	}
	closed, err := result.RowsAffected()
	return closed == 1, err
}
