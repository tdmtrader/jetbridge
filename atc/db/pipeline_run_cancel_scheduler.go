package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

var ErrRunCancellationExternalWork = errors.New("cancellation operation requires an external or terminal handler")

// ExecuteCancellationOperation handles the database-owned scheduling action.
// The composite worker must supply executor, source and terminal handlers for
// the other kinds; this method explicitly refuses them, never marks them done.
// It owns and commits its transaction independently of claiming and recording
// queue progress, so an interrupted reply can safely repeat this exact action.
func (f *pipelineRunFactory) ExecuteCancellationOperation(ctx context.Context, lease RunCancellationLease, op RunCancellationOperation) (RunCancellationDebt, error) {
	if op.Kind != CancelSchedulerDebt {
		return CancellationUnavailable, ErrRunCancellationExternalWork
	}
	parts := strings.Split(op.Subject, "/")
	if len(parts) != 2 || op.WorkerEpoch != lease.Epoch {
		return CancellationConflict, ErrRunCancellationProgressStale
	}
	jobID, err := strconv.Atoi(parts[0])
	if err != nil || jobID < 1 {
		return CancellationConflict, ErrRunCancellationProgressStale
	}
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return CancellationUnavailable, err
	}
	defer Rollback(tx)
	if err := lockCancellingRun(ctx, tx, op.RunID); err != nil {
		return CancellationConflict, err
	}
	// Run -> owned job -> worker lease. No caller may hold the worker row and
	// then acquire a Run or a shared Hangar lifecycle lock.
	var subject string
	if err := tx.QueryRowContext(ctx, `SELECT j.id::text||'/'||(extract(epoch FROM j.schedule_requested)*1000000)::bigint::text
 FROM jobs j JOIN pipelines p ON p.id=j.pipeline_id WHERE j.id=$1 AND p.pipeline_run_id=$2 FOR UPDATE OF j`, jobID, op.RunID).Scan(&subject); err != nil {
		return CancellationConflict, err
	}
	if subject != op.Subject {
		return CancellationConflict, ErrRunCancellationProgressStale
	}
	if err := f.CheckCancellationOperation(ctx, tx, lease, op); err != nil {
		return CancellationConflict, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET last_scheduled=greatest(last_scheduled,schedule_requested) WHERE id=$1`, jobID); err != nil {
		return CancellationUnavailable, err
	}
	return CancellationDone, tx.Commit()
}
