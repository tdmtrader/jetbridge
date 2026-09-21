package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunOutputCancellationEvidence is supplied only by the trusted cleanup worker
// after contacting the exact node outside its transaction. It is not API input.
// An actual finish/stop must carry the node's verified signature. Never-started
// closure instead retains the node's accepted durable stop fence and a subsequent
// never-started classification; it must never fabricate a process outcome.
type RunOutputCancellationEvidence struct {
	NodeUID      string                                              `json:"node_uid"`
	Execution    executioncontrol.ClassifyResult                     `json:"execution"`
	StartClosure *executioncontrol.RequestSourcePreservingStopResult `json:"start_closure,omitempty"`
}

// RecordCancellationClassification retains the first known handoff snapshot
// before a worker requests interruption or closes a never-started execution.
// Its source and Stage 2 fields come from the owning transaction. This is a
// prerequisite for later cleanup, not a finish witness or permission to release.
func (repository *RunOutputRepository) RecordCancellationClassification(ctx context.Context, tx Tx, lease RunCancellationLease, handoff output.HandoffID, observation RunOutputCancellationEvidence) error {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, handoff)
	if err != nil {
		return err
	}
	if owner == nil || (!owner.cancelled && !owner.aborted) {
		return fmt.Errorf("%w: handoff has no owning Run cancellation", output.ErrConflict)
	}
	if _, _, err := lockCurrentCancellationLease(ctx, runTx, lease); err != nil {
		return err
	}
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, handoff)
	if err != nil {
		return err
	}
	var node string
	if err := runTx.QueryRowContext(ctx, `SELECT node_uid FROM pipeline_run_output_starts WHERE handoff_id=$1`, string(handoff)).Scan(&node); err != nil {
		return err
	}
	if observation.StartClosure != nil || observation.NodeUID != node || observation.Execution.Identity != record.Execution {
		return fmt.Errorf("%w: invalid initial cancellation observation", output.ErrInvalidIdentity)
	}
	if !record.Source.Reserved() {
		return atc.ErrRunOutputPending
	}
	if err := observation.Execution.Validate(); err != nil {
		return err
	}
	switch observation.Execution.Classification {
	case executioncontrol.ClassificationNeverStarted, executioncontrol.ClassificationExecuting:
		if record.Disposition != nil && *record.Disposition != output.DispositionPreReservationCancel {
			return fmt.Errorf("%w: execution observation conflicts with its producer checkpoint", output.ErrConflict)
		}
	case executioncontrol.ClassificationAuthoritativeFinish, executioncontrol.ClassificationAuthoritativeStop:
		if err := repository.verifyRunFinish(ctx, runTx, record, *observation.Execution.Acknowledgement); err != nil {
			return err
		}
	default:
		return atc.ErrRunOutputPending
	}
	var disposition, reservation any
	if record.Disposition != nil {
		disposition = string(*record.Disposition)
	}
	if record.ReservationID != "" {
		reservation = string(record.ReservationID)
	}
	body, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	// Later observations may see a finished execution where the first saw an
	// executing one. Preserve the first classification; the separate outcome
	// evidence records closure after re-observing the exact node.
	_, err = runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_cancellation_classifications
 (handoff_id,worker_epoch,classification,hold_acknowledged,disposition,reservation_id,observation)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, string(handoff), lease.Epoch, string(observation.Execution.Classification), record.HoldAcknowledged, disposition, reservation, body)
	return err
}

// RecordCancellationEvidence is its own short progress transaction. Commit it
// before calling CancelOrSettle in another transaction: the worker row is the
// last lock here and cannot precede a later Hangar lifecycle lock.
func (repository *RunOutputRepository) RecordCancellationEvidence(ctx context.Context, tx Tx, lease RunCancellationLease, handoff output.HandoffID, evidence RunOutputCancellationEvidence) error {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, handoff)
	if err != nil {
		return err
	}
	if owner == nil || (!owner.cancelled && !owner.aborted) {
		return fmt.Errorf("%w: source has no owning Run cancellation", output.ErrConflict)
	}
	if _, _, err := lockCurrentCancellationLease(ctx, runTx, lease); err != nil {
		return err
	}
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, handoff)
	if err != nil {
		return err
	}
	var node string
	if err := runTx.QueryRowContext(ctx, `SELECT node_uid FROM pipeline_run_output_starts WHERE handoff_id=$1`, string(handoff)).Scan(&node); err != nil {
		return err
	}
	if evidence.NodeUID != node || evidence.Execution.Identity != record.Execution {
		return fmt.Errorf("%w: cancellation observation does not match the exact Run source", output.ErrInvalidIdentity)
	}
	if err := evidence.Execution.Validate(); err != nil {
		return err
	}
	switch evidence.Execution.Classification {
	case executioncontrol.ClassificationAuthoritativeFinish, executioncontrol.ClassificationAuthoritativeStop:
		if evidence.StartClosure != nil {
			return fmt.Errorf("%w: an actual outcome cannot also be a never-started closure", output.ErrInvalidIdentity)
		}
		if err := repository.verifyRunFinish(ctx, runTx, record, *evidence.Execution.Acknowledgement); err != nil {
			return err
		}
	case executioncontrol.ClassificationNeverStarted:
		closure := evidence.StartClosure
		if closure == nil {
			return atc.ErrRunOutputPending
		}
		if err := closure.Validate(); err != nil {
			return err
		}
		if !closure.Accepted || closure.Classification != executioncontrol.ClassificationNeverStarted || closure.Identity != record.Execution {
			return atc.ErrRunOutputPending
		}
		// A committed capture itself proves an actual finish. It can never be
		// overwritten by a stale or misrouted never-started observation.
		if record.Disposition != nil && *record.Disposition != output.DispositionPreReservationCancel {
			return fmt.Errorf("%w: never-started evidence conflicts with the producer checkpoint", output.ErrConflict)
		}
	default:
		return atc.ErrRunOutputPending
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	var classified bool
	if err := runTx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=$1)
 OR EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1)`, string(handoff)).Scan(&classified); err != nil {
		return err
	}
	if !classified {
		return atc.ErrRunOutputPending
	}
	if _, err := runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_cancellation_evidence(handoff_id,worker_epoch,classification,evidence) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, string(handoff), lease.Epoch, string(evidence.Execution.Classification), body); err != nil {
		return err
	}
	var same bool
	if err := runTx.QueryRowContext(ctx, `SELECT evidence=$2::jsonb FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1`, string(handoff), body).Scan(&same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%w: cancellation source evidence changed on replay", output.ErrConflict)
	}
	return nil
}
