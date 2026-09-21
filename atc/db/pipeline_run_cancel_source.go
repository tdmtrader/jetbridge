package db

import (
	"context"
	"database/sql"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// RunCancellationSource resolves a claimed identity to its original admission.
// It grants no new admission and never chooses a replacement node or execution.
type RunCancellationSource struct {
	Task                   RunOutputTask
	Dispatched, Classified bool
	Start                  *executioncontrol.Acknowledgement
}

func (f *pipelineRunFactory) CancellationOutputTask(ctx context.Context, tx Tx, lease RunCancellationLease, op RunCancellationOperation) (RunCancellationSource, error) {
	var in RunCancellationSource
	var subject string
	switch op.Kind {
	case CancelHandoff:
		subject = "s.handoff_id::text"
	case CancelSourceHold:
		subject = "h.source_hold_id::text"
	case CancelExecution:
		subject = "h.execution_id::text||'/'||h.execution_fence::text"
	case CancelCapture:
		subject = "c.reservation_id::text"
	default:
		return in, ErrRunCancellationExternalWork
	}
	if err := lockCancellingRun(ctx, tx, op.RunID); err != nil {
		return in, err
	}
	var build int
	var task string
	err := tx.QueryRowContext(ctx, `SELECT s.build_id,s.task_id,s.source_requested_at IS NOT NULL,
 EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications x WHERE x.handoff_id=s.handoff_id)
 FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id)
 LEFT JOIN hangar_capture_reservations c USING(handoff_id)
 WHERE s.run_id=$1 AND `+subject+`=$2`, op.RunID, op.Subject).Scan(&build, &task, &in.Dispatched, &in.Classified)
	if err == sql.ErrNoRows && op.Kind == CancelExecution {
		return in, ErrRunCancellationExternalWork
	}
	if err != nil {
		return in, err
	}
	var found bool
	in.Task, found, err = f.OutputTask(ctx, tx, build, task)
	if err != nil {
		return in, err
	}
	if !found {
		return in, sql.ErrNoRows
	}
	in.Start, err = retainedRunExecutionStart(ctx, tx, in.Task.Record.Execution)
	if err != nil {
		return in, err
	}
	return in, f.CheckCancellationOperation(ctx, tx, lease, op)
}

// CheckCancellationOperation is the LAST lock before committing action facts.
// The action has already acquired its domain locks in their normal order. No
// caller may take Run or Hangar locks after this worker-row lock.
func (f *pipelineRunFactory) CheckCancellationOperation(ctx context.Context, tx Tx, lease RunCancellationLease, op RunCancellationOperation) error {
	if op.WorkerEpoch != lease.Epoch {
		return ErrRunCancellationProgressStale
	}
	now, _, err := lockCurrentCancellationLease(ctx, tx, lease)
	if err != nil {
		return err
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations
 WHERE id=$1 AND run_id=$2 AND kind=$3 AND subject=$4 AND attempt_count=$5 AND worker_epoch=$6
 AND claimed AND completed_at IS NULL AND next_at>$7)`, op.ID, op.RunID, string(op.Kind), op.Subject, op.Attempt, lease.Epoch, now).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return ErrRunCancellationProgressStale
	}
	return nil
}
