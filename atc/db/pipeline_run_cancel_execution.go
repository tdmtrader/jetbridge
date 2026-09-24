package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type RunCancellationExecution struct {
	Admission RunExecutionAdmission
	Closed    bool
	Start     *executioncontrol.Acknowledgement
}

// CancellationRunExecution resolves only a retained base execution. Selected
// outputs belong to the source handler, which must classify their handoff first.
func (f *pipelineRunFactory) CancellationRunExecution(ctx context.Context, tx Tx, lease RunCancellationLease, op RunCancellationOperation) (RunCancellationExecution, error) {
	in, err := f.cancellationRunExecution(ctx, tx, op)
	if err != nil {
		return in, err
	}
	return in, f.CheckCancellationOperation(ctx, tx, lease, op)
}

func (f *pipelineRunFactory) cancellationRunExecution(ctx context.Context, tx Tx, op RunCancellationOperation) (RunCancellationExecution, error) {
	var in RunCancellationExecution
	if op.Kind != CancelExecution {
		return in, ErrRunCancellationExternalWork
	}
	run, err := lockRunResultPublication(ctx, tx, op.RunID)
	if err != nil {
		return in, err
	}
	if !run.CancellationRequested() || run.Status() != atc.RunStatusRunning {
		return in, ErrRunCancellationProgressStale
	}
	var build int
	var plan atc.PlanID
	err = tx.QueryRowContext(ctx, `SELECT build_id,plan_id FROM pipeline_run_executions
 WHERE run_id=$1 AND execution_id::text||'/'||execution_fence::text=$2 AND handoff_id IS NULL`, op.RunID, op.Subject).Scan(&build, &plan)
	if err == sql.ErrNoRows {
		return in, collectedRunExecution(ctx, tx, op, &in)
	}
	if err != nil {
		return in, err
	}
	var found bool
	in.Admission, found, err = f.RunExecution(ctx, tx, build, plan)
	if err != nil {
		return in, err
	}
	if !found {
		return in, output.ErrInvalidIdentity
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2)`, string(in.Admission.Identity.ExecutionID), int64(in.Admission.Identity.Fence)).Scan(&in.Closed)
	if err == nil && !in.Closed {
		in.Start, err = retainedRunExecutionStart(ctx, tx, in.Admission.Identity)
	}
	return in, err
}

// RecordCancelledRunExecution persists a verified outcome or the original node's
// durable first-start fence. No RPC occurs while these Run/build locks are held.
func (f *pipelineRunFactory) RecordCancelledRunExecution(ctx context.Context, tx Tx, lease RunCancellationLease, op RunCancellationOperation, evidence RunOutputCancellationEvidence, verifier RunExecutionVerifier) error {
	in, err := f.cancellationRunExecution(ctx, tx, op)
	if err != nil {
		return err
	}
	if in.Closed && in.Admission.BuildID == 0 {
		// Closed and collected while the node was being observed.
		return f.CheckCancellationOperation(ctx, tx, lease, op)
	}
	a := in.Admission
	if evidence.NodeUID != a.NodeUID || evidence.Execution.Identity != a.Identity {
		return output.ErrInvalidIdentity
	}
	if err = evidence.Execution.Validate(); err != nil {
		return err
	}
	switch evidence.Execution.Classification {
	case executioncontrol.ClassificationAuthoritativeFinish, executioncontrol.ClassificationAuthoritativeStop:
		if evidence.StartClosure != nil {
			return output.ErrInvalidIdentity
		}
		if err = f.RecordRunExecutionWitness(ctx, tx, a.BuildID, a.PlanID, *evidence.Execution.Acknowledgement, verifier); err != nil {
			return err
		}
	case executioncontrol.ClassificationNeverStarted:
		closure := evidence.StartClosure
		if closure == nil {
			return atc.ErrRunOutputPending
		}
		if err = closure.Validate(); err != nil {
			return err
		}
		if !closure.Accepted || closure.Identity != a.Identity || closure.Classification != executioncontrol.ClassificationNeverStarted {
			return atc.ErrRunOutputPending
		}
		var started bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_execution_starts WHERE execution_id=$1 AND execution_fence=$2)`, string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&started); err != nil {
			return err
		}
		if started {
			return fmt.Errorf("%w: never-started cancellation contradicts a retained start", output.ErrConflict)
		}
		body, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_execution_closures(execution_id,execution_fence,classification,observation,worker_epoch)
 VALUES($1,$2,'never_started',$3,$4) ON CONFLICT DO NOTHING`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), body, lease.Epoch); err != nil {
			return err
		}
		var same bool
		if err = tx.QueryRowContext(ctx, `SELECT observation=$3::jsonb FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), body).Scan(&same); err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: exact cancellation closure changed", output.ErrConflict)
		}
	default:
		return atc.ErrRunOutputPending
	}
	// Worker ownership is always the last lock, after all Run/domain facts.
	return f.CheckCancellationOperation(ctx, tx, lease, op)
}

// collectedRunExecution resolves a discovered execution that has no base
// execution row. One linked to a selected output belongs to the source
// handler. One with no row at all was a closed check execution that check
// collection has since removed with its build: it is closed, and there is
// nothing left to stop.
func collectedRunExecution(ctx context.Context, tx Tx, op RunCancellationOperation, in *RunCancellationExecution) error {
	var linked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_executions
 WHERE run_id=$1 AND execution_id::text||'/'||execution_fence::text=$2)
 OR EXISTS(SELECT 1 FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id)
 WHERE s.run_id=$1 AND h.execution_id::text||'/'||h.execution_fence::text=$2)`, op.RunID, op.Subject).Scan(&linked)
	if err != nil {
		return err
	}
	if linked {
		return ErrRunCancellationExternalWork
	}
	in.Closed = true
	return nil
}
