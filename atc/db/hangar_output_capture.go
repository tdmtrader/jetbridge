package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// CommitCaptureReservation is Stage 2.
//
// Only an authoritative successful finish witness reaches it -- the value type
// has nowhere to put anything else, and the schema's finish_successful column
// is a boolean that can only be true. It records the opaque producer
// checkpoint, wins the one-row arbiter as capture, and creates a distinct
// unresolved reservation whose id it returns, because that id is the
// correlation handle every later step needs.
//
// Winning the arbiter is not capture authority. Nothing here seals, publishes
// or binds, and the returned id names an unresolved reservation with no scope,
// no digest, no generation and no receipt.
func (repository *HangarOutputRepository) CommitCaptureReservation(ctx context.Context, tx output.Tx, disposition output.SuccessfulFinishDisposition) (output.ReservationID, error) {
	if err := disposition.Validate(); err != nil {
		return "", err
	}

	repeated, err := repository.winArbiter(ctx, tx, disposition.HandoffID, output.DispositionCapture)
	if err != nil {
		return "", err
	}
	if repeated {
		// The same identity with the same immutable facts returns the same
		// durable state. A checkpoint that differs is a different Stage 2
		// wearing an old idempotency key.
		var existing, checkpoint string
		if err := hangarQueryRow(ctx, tx, `
			SELECT reservation_id, producer_checkpoint_id FROM hangar_capture_reservations
			WHERE handoff_id = $1`, []any{string(disposition.HandoffID)}, &existing, &checkpoint); err != nil {
			return "", err
		}
		if checkpoint != string(disposition.ProducerCheckpointID) {
			return "", fmt.Errorf("%w: handoff %s already committed producer checkpoint %q",
				output.ErrConflict, disposition.HandoffID, checkpoint)
		}

		return output.ReservationID(existing), nil
	}

	witness, err := json.Marshal(disposition.FinishAcknowledgement)
	if err != nil {
		return "", err
	}

	reservation := output.ReservationID(uuid.NewString())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_capture_reservations
			(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
			 producer_checkpoint_id, finish_acknowledgement, finish_successful,
			 capture_fence, capture_deadline_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $9)`,
		string(reservation),
		string(disposition.HandoffID),
		string(disposition.Execution.ExecutionID),
		int64(disposition.ActivationEpoch),
		string(disposition.SourceLeaseID),
		string(disposition.ProducerCheckpointID),
		witness,
		int64(disposition.CaptureFence),
		disposition.CaptureDeadline.Time,
	); err != nil {
		return "", hangarConflict(err)
	}

	return reservation, nil
}

// HangarCaptureLease is one renewable, fenced grant of capture ownership.
type HangarCaptureLease struct {
	ReservationID output.ReservationID
	OwnerID       string
	Fence         output.CaptureFence
	ExpiresAt     time.Time
}

// AcquireCaptureLease takes or takes over capture ownership.
//
// Takeover advances the fence, always, and the schema refuses one that does
// not. The term is measured on the database clock rather than the caller's,
// because a node whose clock drifts must not be able to expire its own
// ownership -- and expiry alone is never proof or release authority anyway.
func (repository *HangarOutputRepository) AcquireCaptureLease(ctx context.Context, tx output.Tx, reservation output.ReservationID, owner string, term time.Duration) (HangarCaptureLease, error) {
	if err := reservation.Validate(); err != nil {
		return HangarCaptureLease{}, err
	}
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return HangarCaptureLease{}, err
	}

	var fence int64
	err = hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_capture_attempt_leases
			(reservation_id, owner_id, capture_fence, expires_at)
		VALUES ($1, $2, 1, now() + $3::interval)
		ON CONFLICT (reservation_id) DO UPDATE
		SET owner_id = EXCLUDED.owner_id,
		    renewed_at = now(),
		    expires_at = now() + $3::interval,
		    capture_fence = CASE
		        WHEN hangar_capture_attempt_leases.owner_id = EXCLUDED.owner_id
		            THEN hangar_capture_attempt_leases.capture_fence
		        ELSE hangar_capture_attempt_leases.capture_fence + 1
		    END
		WHERE hangar_capture_attempt_leases.owner_id = EXCLUDED.owner_id
		   OR hangar_capture_attempt_leases.expires_at <= now()
		RETURNING capture_fence`,
		[]any{string(reservation), owner, interval}, &fence)
	if err != nil {
		return HangarCaptureLease{}, fmt.Errorf("%w: capture ownership of reservation %s is held "+
			"by another owner whose lease has not expired", output.ErrConflict, reservation)
	}

	return HangarCaptureLease{
		ReservationID: reservation,
		OwnerID:       owner,
		Fence:         output.CaptureFence(fence),
	}, nil
}

// ClassifyHandoff is what a generic caller may learn about a capture.
//
// It reads; it decides nothing. Disposition is absent before the arbiter is
// won, because an unwon arbiter is the absence of a branch rather than a fourth
// kind of branch.
func (repository *HangarOutputRepository) ClassifyHandoff(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffStatus, error) {
	if err := handoff.Validate(); err != nil {
		return output.HandoffStatus{}, err
	}

	status := output.HandoffStatus{HandoffID: handoff}

	var (
		branch       sql.NullString
		settled      sql.NullBool
		irreversible sql.NullBool
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT d.disposition,
		       CASE d.disposition
		           WHEN 'capture' THEN r.settled_at IS NOT NULL
		           WHEN 'no_capture' THEN n.release_acknowledged_at IS NOT NULL
		           ELSE c.finalized_at IS NOT NULL
		       END,
		       coalesce(r.past_irreversible_publish_point, false)
		FROM hangar_handoff_predeclarations p
		LEFT JOIN hangar_handoff_dispositions d ON d.handoff_id = p.handoff_id
		LEFT JOIN hangar_capture_reservations r ON r.handoff_id = p.handoff_id
		LEFT JOIN hangar_no_capture_dispositions n ON n.handoff_id = p.handoff_id
		LEFT JOIN hangar_pre_reservation_cancel_dispositions c ON c.handoff_id = p.handoff_id
		WHERE p.handoff_id = $1`,
		[]any{string(handoff)}, &branch, &settled, &irreversible); err != nil {
		return output.HandoffStatus{}, err
	}

	if branch.Valid {
		disposition := output.Disposition(branch.String)
		status.Disposition = &disposition
		status.Settled = settled.Bool
	}
	status.PastIrreversiblePublishPoint = irreversible.Bool

	if receipt, err := repository.readReceiptForHandoff(ctx, tx, handoff); err == nil {
		status.Receipt = &receipt
	}

	return status, nil
}

// CancelOrSettle is the product-neutral cancel and settle seam.
//
// Before the irreversible publish point it terminally cancels and records a
// fenced release of the source. After it, cancellation cannot unmake an object:
// the capture settles a registered receipt or a terminal orphan, and this
// returns what it found. Neither path can create a consumer binding, and there
// is no parameter through which a caller could ask for one.
func (repository *HangarOutputRepository) CancelOrSettle(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffStatus, error) {
	status, err := repository.ClassifyHandoff(ctx, tx, handoff)
	if err != nil {
		return output.HandoffStatus{}, err
	}
	if status.Settled || status.PastIrreversiblePublishPoint {
		// Past the publish point there is nothing to cancel. The capture
		// settles a receipt or a terminal orphan on its own, under its own
		// fence, and a canceller waiting for that is not a canceller failing.
		return status, nil
	}
	if status.Disposition != nil && *status.Disposition == output.DispositionCapture {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hangar_capture_reservations
			SET state = 'cancelled', settled_at = now()
			WHERE handoff_id = $1 AND settled_at IS NULL`, string(handoff)); err != nil {
			return output.HandoffStatus{}, hangarConflict(err)
		}

		return repository.ClassifyHandoff(ctx, tx, handoff)
	}

	var (
		lease, execution string
		epoch, fence     int64
		held             sql.NullTime
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT source_lease_id, execution_id, execution_fence, activation_epoch, hold_acknowledged_at
		FROM hangar_handoff_predeclarations WHERE handoff_id = $1`,
		[]any{string(handoff)}, &lease, &execution, &fence, &epoch, &held); err != nil {
		return output.HandoffStatus{}, err
	}

	disposition := output.PreReservationCancelDisposition{
		ProtocolVersion:  output.ProtocolVersion,
		Disposition:      output.DispositionPreReservationCancel,
		ActivationEpoch:  hangarEpoch(epoch),
		HandoffID:        handoff,
		SourceLeaseID:    output.SourceLeaseID(lease),
		HoldAcknowledged: held.Valid,
	}
	disposition.Execution.ExecutionID = hangarExecutionID(execution)
	disposition.Execution.Fence = executioncontrol.Fence(fence)
	if held.Valid {
		disposition.ReleaseIntentID = output.ReleaseIntentID(uuid.NewString())
	}

	if err := repository.RecordPreReservationCancelIntent(ctx, tx, disposition); err != nil {
		return output.HandoffStatus{}, err
	}

	return repository.ClassifyHandoff(ctx, tx, handoff)
}
