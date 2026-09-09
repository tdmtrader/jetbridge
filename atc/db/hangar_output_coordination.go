package db

// What a coordinator reads, and the two facts it writes that no earlier phase
// needed.
//
// Everything here exists because a capture has to be recoverable by a process
// that was not the one that started it. That process cannot ask "what was I
// doing"; it can only read what is durably true. So it needs one read that
// assembles every fact about a handoff, and it needs two facts recorded that
// were previously only in the memory of the process that learned them: WHERE
// the source incarnation is, and that a capture failed terminally.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RecordSourceReservation records the daemon's answer to reserve-incarnation.
//
// The control plane needs it durably for one reason, and it is a recovery
// reason: to RELEASE a source you have to know which node holds it and which
// incarnation it is, and after a crash there is nowhere else to learn either.
// The reservation is taken before the Pod exists, the ledger that knows it is
// node-local, and a control plane cannot ask every node in the cluster whether
// it happens to be holding something.
//
// The locator is opaque here exactly as it is everywhere else in this plane:
// it is stored and handed back, and nothing in Hangar parses it.
//
// Idempotent for the same location and a typed conflict for a different one. A
// second location for one handoff is a second capture wearing one identity, and
// the Pod has already been built around the first answer.
func (repository *HangarOutputRepository) RecordSourceReservation(ctx context.Context, tx output.Tx, reserved output.ReservedIncarnation, locator string) error {
	if err := reserved.Validate(); err != nil {
		return err
	}
	if locator == "" {
		return fmt.Errorf("%w: the reservation for handoff %s names no node to reach; a release "+
			"has to be addressed to the one node holding the source", output.ErrIncomplete,
			reserved.HandoffID)
	}

	incarnation, err := json.Marshal(reserved.Incarnation)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_handoff_predeclarations
		SET reserved_locator = $2,
		    reserved_incarnation = $3,
		    reserved_directory = $4,
		    reserved_at = now()
		WHERE handoff_id = $1
		  AND source_lease_id = $5
		  AND execution_id = $6
		  AND reserved_at IS NULL`,
		string(reserved.HandoffID),
		locator,
		incarnation,
		reserved.Directory,
		string(reserved.SourceLeaseID),
		string(reserved.Execution.ExecutionID),
	)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 1 {
		return nil
	}

	var (
		stored_locator, directory string
		stored                    []byte
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT reserved_locator, reserved_incarnation, reserved_directory
		FROM hangar_handoff_predeclarations
		WHERE handoff_id = $1 AND source_lease_id = $2 AND execution_id = $3`,
		[]any{
			string(reserved.HandoffID),
			string(reserved.SourceLeaseID),
			string(reserved.Execution.ExecutionID),
		}, &stored_locator, &stored, &directory); err != nil {
		return fmt.Errorf("%w: no predeclaration matches the reservation for handoff %s",
			output.ErrInvalidIdentity, reserved.HandoffID)
	}

	var existing output.SourceIncarnation
	if err := json.Unmarshal(stored, &existing); err != nil {
		return fmt.Errorf("%w: the stored reservation for handoff %s is unreadable: %v",
			output.ErrCorrupt, reserved.HandoffID, err)
	}
	if stored_locator != locator || existing != reserved.Incarnation ||
		directory != reserved.Directory {
		return fmt.Errorf("%w: handoff %s already reserved incarnation %s/%d on %q and this "+
			"reservation names %s/%d on %q; a second location for one handoff is a second "+
			"capture, and the Pod is already built around the first answer",
			output.ErrConflict, reserved.HandoffID, existing.Output, existing.HandleGeneration,
			stored_locator, reserved.Incarnation.Output,
			reserved.Incarnation.HandleGeneration, locator)
	}

	return nil
}

// RecordTerminalCaptureFailure closes a capture with a typed failure and no
// receipt.
//
// It is a distinct method from cancellation because it is a distinct fact:
// cancellation is something a caller asked for and this is something that could
// not be done -- `seal_unconfirmed`, `source_lost`, a collision at the derived
// key. Both are terminal, both may owe a fenced release before the irreversible
// publish point, and neither ever creates a receipt or a claim.
//
// The fence is checked, so a stale owner cannot fail a capture the current
// owner is still publishing.
func (repository *HangarOutputRepository) RecordTerminalCaptureFailure(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence, failure string) error {
	if err := reservation.Validate(); err != nil {
		return err
	}
	if failure == "" {
		return fmt.Errorf("%w: a terminal capture failure names no reason; `it failed` is not a "+
			"typed outcome and cannot be told apart from an unfinished one", output.ErrIncomplete)
	}
	if fence == 0 {
		return fmt.Errorf("%w: a terminal capture failure under no capture fence",
			output.ErrUnauthorized)
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Captures: []output.ReservationID{reservation},
	}); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations
		SET state = 'failed',
		    terminal_failure = $3,
		    release_intent_id = CASE
		        WHEN past_irreversible_publish_point THEN release_intent_id
		        ELSE coalesce(release_intent_id, gen_random_uuid())
		    END
		WHERE reservation_id = $1
		  AND capture_fence = $2
		  AND state IN ('unresolved', 'resolved')`,
		string(reservation), int64(fence), failure)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 1 {
		return nil
	}

	var state, existing string
	var stored sql.NullString
	if err := hangarQueryRow(ctx, tx, `
		SELECT state, coalesce(terminal_failure, '') FROM hangar_capture_reservations
		WHERE reservation_id = $1 AND capture_fence = $2`,
		[]any{string(reservation), int64(fence)}, &state, &existing); err != nil {
		return fmt.Errorf("%w: reservation %s is not owned at capture fence %d; a stale owner "+
			"may not finalize", output.ErrUnauthorized, reservation, fence)
	}
	_ = stored
	if state == string(output.CaptureStateFailed) && existing == failure {
		return nil
	}

	return fmt.Errorf("%w: reservation %s is %s and cannot also fail as %q",
		output.ErrConflict, reservation, state, failure)
}

// LoadHandoffRecord assembles every durable fact about one handoff.
//
// One statement, and that is deliberate. The facts live in five tables and a
// coordinator that read them one at a time would be deciding on a mixture of
// two moments -- which is exactly the class of bug the whole plane exists to
// refuse. The seal is absent from it because the seal has no answer here: the
// source ledger is authoritative for sealing, and a second copy in PostgreSQL
// would be the copy that disagrees after a takeover.
func (repository *HangarOutputRepository) LoadHandoffRecord(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffRecord, error) {
	if err := handoff.Validate(); err != nil {
		return output.HandoffRecord{}, err
	}

	var (
		lease, execution, name       string
		fence, epoch                 int64
		deadline                     time.Time
		locator, directory           sql.NullString
		incarnation                  []byte
		reservedAt, holdAcknowledged sql.NullTime
		branch                       sql.NullString
		reservation, checkpoint      sql.NullString
		captureFence                 sql.NullInt64
		state                        sql.NullString
		terminalFailure              sql.NullString
		pastPublish                  sql.NullBool
		scope, digest                sql.NullString
		logicalBytes                 sql.NullInt64
		generation                   sql.NullInt64
		captureIntent, noIntent      sql.NullString
		cancelIntent                 sql.NullString
		captureAck, noAck, cancelAck sql.NullTime
	)

	if err := hangarQueryRow(ctx, tx, `
		SELECT p.source_lease_id, p.execution_id, p.execution_fence, p.output_name,
		       p.activation_epoch, p.capture_deadline_at,
		       p.reserved_locator, p.reserved_incarnation, p.reserved_directory, p.reserved_at,
		       p.hold_acknowledged_at,
		       d.disposition,
		       r.reservation_id, r.producer_checkpoint_id, r.capture_fence, r.state,
		       r.terminal_failure, r.past_irreversible_publish_point,
		       r.release_intent_id, r.release_acknowledged_at,
		       l.scope, l.digest, l.logical_bytes,
		       x.generation,
		       n.release_intent_id, n.release_acknowledged_at,
		       c.release_intent_id, c.release_acknowledged_at
		FROM hangar_handoff_predeclarations p
		LEFT JOIN hangar_handoff_dispositions d ON d.handoff_id = p.handoff_id
		LEFT JOIN hangar_capture_reservations r ON r.handoff_id = p.handoff_id
		LEFT JOIN hangar_logical_reservations l ON l.reservation_id = r.reservation_id
		LEFT JOIN hangar_output_receipts e ON e.reservation_id = r.reservation_id
		LEFT JOIN hangar_exact_lifecycles x ON x.id = e.lifecycle_id
		LEFT JOIN hangar_no_capture_dispositions n ON n.handoff_id = p.handoff_id
		LEFT JOIN hangar_pre_reservation_cancel_dispositions c ON c.handoff_id = p.handoff_id
		WHERE p.handoff_id = $1`,
		[]any{string(handoff)},
		&lease, &execution, &fence, &name, &epoch, &deadline,
		&locator, &incarnation, &directory, &reservedAt, &holdAcknowledged,
		&branch,
		&reservation, &checkpoint, &captureFence, &state,
		&terminalFailure, &pastPublish,
		&captureIntent, &captureAck,
		&scope, &digest, &logicalBytes,
		&generation,
		&noIntent, &noAck,
		&cancelIntent, &cancelAck,
	); err != nil {
		return output.HandoffRecord{}, err
	}

	record := output.HandoffRecord{
		HandoffID:       handoff,
		SourceLeaseID:   output.SourceLeaseID(lease),
		ActivationEpoch: hangarEpoch(epoch),
		Output:          output.OutputName(name),
		CaptureDeadline: output.NewTimestamp(deadline),

		HoldAcknowledged:             holdAcknowledged.Valid,
		PastIrreversiblePublishPoint: pastPublish.Bool,
	}
	record.Execution.ExecutionID = hangarExecutionID(execution)
	record.Execution.Fence = executioncontrol.Fence(fence)

	if reservedAt.Valid {
		var placed output.SourceIncarnation
		if err := json.Unmarshal(incarnation, &placed); err != nil {
			return output.HandoffRecord{}, fmt.Errorf(
				"%w: the stored reservation for handoff %s is unreadable: %v",
				output.ErrCorrupt, handoff, err)
		}
		record.Source = output.SourcePlacement{
			Locator:     locator.String,
			Incarnation: placed,
			Directory:   directory.String,
		}
	}

	if branch.Valid {
		decided, err := output.ParseDisposition(branch.String)
		if err != nil {
			return output.HandoffRecord{}, err
		}
		record.Disposition = &decided

		switch decided {
		case output.DispositionNoCapture:
			record.ReleaseIntentID = output.ReleaseIntentID(noIntent.String)
			record.ReleaseAcknowledged = noAck.Valid
		case output.DispositionPreReservationCancel:
			record.ReleaseIntentID = output.ReleaseIntentID(cancelIntent.String)
			record.ReleaseAcknowledged = cancelAck.Valid
		case output.DispositionCapture:
			record.ReleaseIntentID = output.ReleaseIntentID(captureIntent.String)
			record.ReleaseAcknowledged = captureAck.Valid
		}
	}

	if reservation.Valid {
		record.ReservationID = output.ReservationID(reservation.String)
		record.ProducerCheckpointID = output.OpaqueID(checkpoint.String)
		record.CaptureFence = output.CaptureFence(captureFence.Int64)
		parsed, err := output.ParseCaptureState(state.String)
		if err != nil {
			return output.HandoffRecord{}, err
		}
		record.State = parsed
		record.TerminalFailure = terminalFailure.String
	}

	if scope.Valid && digest.Valid {
		record.LogicalResolved = true
		record.Scope = hangar.Scope(scope.String)
		record.Digest = hangar.Digest(digest.String)
		if generation.Valid {
			record.Ref = hangar.TreeRef{
				Scope:      record.Scope,
				Digest:     record.Digest,
				Generation: generation.Int64,
			}
		}
	}

	if receipt, err := repository.readReceiptForHandoff(ctx, tx, handoff); err == nil {
		record.Receipt = &receipt
	}

	// Settled means one thing on all three branches: nothing is still owed.
	// It is derived here from the same columns ClassifyHandoff derives it from
	// rather than selected again, so the two cannot answer differently.
	status, err := repository.ClassifyHandoff(ctx, tx, handoff)
	if err != nil {
		return output.HandoffRecord{}, err
	}
	record.Settled = status.Settled

	return record, record.Validate()
}
