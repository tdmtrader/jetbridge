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

	"github.com/google/uuid"

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
		  AND source_hold_id = $5
		  AND execution_id = $6
		  AND execution_fence = $7
		  AND activation_epoch = $8
		  AND output_name = $9
		  AND reserved_at IS NULL`,
		string(reserved.HandoffID),
		locator,
		incarnation,
		reserved.Directory,
		string(reserved.SourceHoldID),
		string(reserved.Execution.ExecutionID),
		int64(reserved.Execution.Fence),
		int64(reserved.ActivationEpoch),
		string(reserved.Incarnation.Output),
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
		WHERE handoff_id = $1 AND source_hold_id = $2 AND execution_id = $3
		  AND execution_fence = $4 AND activation_epoch = $5 AND output_name = $6`,
		[]any{
			string(reserved.HandoffID),
			string(reserved.SourceHoldID),
			string(reserved.Execution.ExecutionID),
			int64(reserved.Execution.Fence),
			int64(reserved.ActivationEpoch),
			string(reserved.Incarnation.Output),
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
//
// It can return ErrHangarLockRetry, for the same reason CancelOrSettle can:
// this closes two Hangar rows in two lock classes, so it enters the suffix, and
// a suffix can always be told the facts it was derived from have moved. The
// caller rolls back and comes round again.
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

	// Class 1 BEFORE class 3, because hangarTerminalizeLogical below writes the
	// logical reservation and a bare UPDATE takes that row's lock. Taking only
	// the capture class here left this writer holding class 3 and reaching back
	// for class 1, which deadlocks against any publisher taking the stated
	// order.
	if err := hangarLockTerminalCapture(ctx, tx, repository.prefix, reservation); err != nil {
		return err
	}

	// The release intent is minted whatever side of the publish point the
	// capture failed on, and that is a Phase 7 change with a reason.
	//
	// A failed capture ALWAYS owes its source back. What a release releases is
	// the HOLD, never the bytes (the Phase 5 ruling): the incarnation stays on
	// the node as ordinary step content, and the hold is what exempts it from
	// payload cleanup, sweep and reuse. Withholding the intent past the publish
	// point left a terminally failed capture that could never settle -- the
	// schema earns settlement with a release acknowledgement -- and therefore
	// an incarnation pinned on a node for the life of the deployment, beside a
	// correlation adoption would never be allowed to collect.
	//
	// The object it may have created needs no record here. It carries this
	// capture's marker, the sweep will find it, and once this reservation is
	// terminal and its grace has elapsed adoption takes it: that is Req 40's
	// "terminal settlement eventually permits marked-orphan adoption", and the
	// mechanism is inventory rather than a second ledger of orphans.
	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations r
		SET state = 'failed',
		    terminal_failure = $3,
		    release_intent_id = coalesce(r.release_intent_id, gen_random_uuid())
		WHERE r.reservation_id = $1
		  AND `+hangarCurrentCaptureFence+` = $2
		  AND r.state IN ('unresolved', 'resolved')`,
		string(reservation), int64(fence), failure)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 1 {
		return hangarTerminalizeLogical(ctx, tx, reservation)
	}

	var state, existing string
	var stored sql.NullString
	if err := hangarQueryRow(ctx, tx, `
		SELECT r.state, coalesce(r.terminal_failure, '') FROM hangar_capture_reservations r
		WHERE r.reservation_id = $1 AND `+hangarCurrentCaptureFence+` = $2`,
		[]any{string(reservation), int64(fence)}, &state, &existing); err != nil {
		return fmt.Errorf("%w: reservation %s is not owned at capture fence %d; a stale owner "+
			"may not finalize", output.ErrUnauthorized, reservation, fence)
	}
	_ = stored
	if state == string(output.CaptureStateFailed) && existing == failure {
		return hangarTerminalizeLogical(ctx, tx, reservation)
	}

	return fmt.Errorf("%w: reservation %s is %s and cannot also fail as %q",
		output.ErrConflict, reservation, state, failure)
}

// RecordSealDeadline stamps, once, the database-clock moment past which this
// capture's seal is unprovable, and returns it.
//
// It is durable, and on the database's clock, for the reason every deadline in
// this plane is: the process that begins a seal is not necessarily the process
// that has to decide whether the boundary was proved in time. A capture crosses
// an ATC restart, so a deadline held in the memory of the process that composed
// it is a deadline that never expires -- which is what left `seal_unconfirmed`
// with no producer except a mistaken guess at a lost answer.
//
// `coalesce`, not an overwrite: a begin_seal repeated after a lost answer is
// the same seal, and a fresh deadline on every retry is an unbounded seal
// wearing a bound.
func (repository *HangarOutputRepository) RecordSealDeadline(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence, term time.Duration) (output.Timestamp, error) {
	if err := reservation.Validate(); err != nil {
		return output.Timestamp{}, err
	}
	if fence == 0 {
		return output.Timestamp{}, fmt.Errorf("%w: a seal deadline under no capture fence",
			output.ErrUnauthorized)
	}

	// The capture class, named rather than taken by the UPDATE below: see
	// RecordFirstObjectCreate for why an unnamed single-class lock is still a
	// lock this order has to be able to see.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Captures: []output.ReservationID{reservation},
	}); err != nil {
		return output.Timestamp{}, err
	}

	var deadline time.Time
	if err := hangarQueryRow(ctx, tx, `
		UPDATE hangar_capture_reservations r
		SET seal_deadline_at = coalesce(r.seal_deadline_at, now() + $3::interval)
		WHERE r.reservation_id = $1
		  AND `+hangarCurrentCaptureFence+` = $2
		  AND r.state IN ('unresolved', 'resolved')
		RETURNING r.seal_deadline_at`,
		[]any{string(reservation), int64(fence), hangarInterval(term)}, &deadline); err != nil {
		return output.Timestamp{}, fmt.Errorf("%w: reservation %s is not owned at capture fence "+
			"%d, or is terminal; a stale owner may not seal", output.ErrUnauthorized,
			reservation, fence)
	}

	return output.NewTimestamp(deadline), nil
}

// SealDeadlinePassed answers, on the database clock, whether this capture's
// seal deadline has elapsed.
//
// The comparison is made in SQL and not from a value any process computed,
// which is the same rule the ownership lease follows: a node whose clock drifts
// must not be able to expire -- or extend -- a deadline it is subject to.
//
// It is clock_timestamp() and NOT now(), and the seal deadline is the one place
// in this plane where that distinction is load-bearing. now() is
// transaction_timestamp(), so a transaction open for N seconds reads it N
// seconds early; the plane rules on that imprecision rather than fixing it (see
// the note at the top of hangar_output_reclaim.go), and the ruling holds for
// LEASES because every lease here is floored at fifteen minutes by a CHECK and
// N is milliseconds. The seal deadline is not a lease: Req 17 makes it
// configurable down to THIRTY SECONDS, so a slow check transaction reading its
// own start instant judges a seal in-time that is not -- the exact failure
// seal_deadline_at was added to prevent. hangarStatProofFresh uses
// clock_timestamp() for the same reason.
//
// The STAMP above stays on now(): what it needs is the same instant the rest of
// the transaction is writing under, and a deadline stamped from a later reading
// than the row it lands beside is the mixture of two moments this plane refuses.
func (repository *HangarOutputRepository) SealDeadlinePassed(ctx context.Context, tx output.Tx, reservation output.ReservationID) (bool, error) {
	if err := reservation.Validate(); err != nil {
		return false, err
	}

	var passed bool
	if err := hangarQueryRow(ctx, tx, `
		SELECT coalesce(
		       seal_deadline_at IS NOT NULL AND clock_timestamp() >= seal_deadline_at, false)
		FROM hangar_capture_reservations WHERE reservation_id = $1`,
		[]any{string(reservation)}, &passed); err != nil {
		return false, err
	}

	return passed, nil
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
		SELECT p.source_hold_id, p.execution_id, p.execution_fence, p.output_name,
		       p.activation_epoch, p.capture_deadline_at,
		       p.reserved_locator, p.reserved_incarnation, p.reserved_directory, p.reserved_at,
		       p.hold_acknowledged_at,
		       d.disposition,
		       r.reservation_id, r.producer_checkpoint_id,
		       CASE WHEN r.reservation_id IS NULL THEN NULL
		            ELSE `+hangarCurrentCaptureFence+` END, r.state,
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
		SourceHoldID:    output.SourceHoldID(lease),
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

// IssueStatChallenge mints the one-use, database-clock-bounded challenge a
// receipt is signed against.
//
// It is here rather than at the coordinator because the challenge is a ROW: its
// one-use property is the row being consumed exactly once, and a nonce a
// process kept in memory would be one-use only for as long as that process
// lived. A receipt signed over old facts proves only that the facts were once
// true, which is why the freshness is durable rather than remembered.
//
// The generation is part of it, so it cannot be issued before the object exists.
func (repository *HangarOutputRepository) IssueStatChallenge(ctx context.Context, tx output.Tx, handoff output.HandoffID, reservation output.ReservationID, ref hangar.TreeRef, fence output.CaptureFence, keyID string, term time.Duration) (output.StatChallenge, error) {
	if err := handoff.Validate(); err != nil {
		return output.StatChallenge{}, err
	}
	if err := reservation.Validate(); err != nil {
		return output.StatChallenge{}, err
	}
	if err := ref.Validate(); err != nil {
		return output.StatChallenge{}, err
	}
	if fence == 0 {
		return output.StatChallenge{}, fmt.Errorf(
			"%w: a stat challenge under no capture fence", output.ErrUnauthorized)
	}
	interval := hangarInterval(term)

	nonce := "nonce-" + uuid.NewString()
	var issuedAt, notAfter time.Time
	var epoch int64
	if err := hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_receipt_stat_challenges
			(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
			 scope, digest, generation, capture_fence, not_after)
		SELECT $1, $2, $3, r.activation_epoch, $7, $4, $5, $6, $8, now() + $9::interval
		FROM hangar_capture_reservations r
		WHERE r.reservation_id = $3 AND `+hangarCurrentCaptureFence+` = $8
		RETURNING issued_at, not_after, activation_epoch`,
		[]any{
			nonce, string(handoff), string(reservation),
			string(ref.Scope), string(ref.Digest), ref.Generation,
			keyID, int64(fence), interval,
		}, &issuedAt, &notAfter, &epoch); err != nil {
		return output.StatChallenge{}, fmt.Errorf("%w: reservation %s is not owned at capture "+
			"fence %d; a stale owner may not obtain a receipt", output.ErrUnauthorized,
			reservation, fence)
	}

	challenge := output.StatChallenge{
		Nonce:           nonce,
		HandoffID:       handoff,
		ReservationID:   reservation,
		ActivationEpoch: hangarEpoch(epoch),
		Ref:             ref,
		CaptureFence:    fence,
		IssuedAt:        output.NewTimestamp(issuedAt),
		NotAfter:        output.NewTimestamp(notAfter),
	}

	return challenge, challenge.Validate()
}

// RecordAnnouncement appends one Req 18 announcement.
//
// Durable rather than a log line, because the process that announces a
// selection is not the process that announces the outcome: a capture crosses
// an ATC restart, and an announcement stream held in memory would lose exactly
// the announcement that explains why hijack stopped working.
//
// A repeat of the same kind is idempotent. A capture that re-announced its
// selection after a restart would be telling a watcher that hijack went away
// twice, and the retry that produced it is not news.
func (repository *HangarOutputRepository) RecordAnnouncement(ctx context.Context, tx output.Tx, handoff output.HandoffID, kind, disposition, reason string) error {
	if err := handoff.Validate(); err != nil {
		return err
	}

	var branch any
	if disposition != "" {
		branch = disposition
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_capture_announcements (handoff_id, kind, disposition, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (handoff_id, kind) DO NOTHING`,
		string(handoff), kind, branch, reason,
	); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// HangarAnnouncement is one thing a capture told a watcher.
type HangarAnnouncement struct {
	Handoff     string
	Kind        string
	Disposition string
	Reason      string
}

// ReadAnnouncements returns what a capture told a watcher, in emission order.
//
// Order is part of what it says: a selection announced after an outcome would
// be explaining a refusal instead of preventing one, and a reader that sorted
// by anything but the append order could not tell.
func (repository *HangarOutputRepository) ReadAnnouncements(ctx context.Context, tx output.Tx, handoff output.HandoffID) ([]HangarAnnouncement, error) {
	if err := handoff.Validate(); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT handoff_id, kind, coalesce(disposition, ''), reason
		FROM hangar_capture_announcements
		WHERE handoff_id = $1
		ORDER BY id`, string(handoff))
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer rows.Close()

	var announcements []HangarAnnouncement
	for rows.Next() {
		var announcement HangarAnnouncement
		if err := rows.Scan(&announcement.Handoff, &announcement.Kind,
			&announcement.Disposition, &announcement.Reason); err != nil {
			return nil, err
		}
		announcements = append(announcements, announcement)
	}

	return announcements, rows.Err()
}

// IncompleteHandoffs lists the handoffs that still owe something.
//
// It is a query about DEBT rather than about age or state, and the difference
// matters: what a coordinator has to look at is exactly the set that has not
// settled, and a list built from a timestamp would either miss a handoff whose
// producer is still running or keep visiting captures that are done.
//
// The predicate is the same "nothing is still owed" ClassifyHandoff uses, in
// its negative form. Two spellings of settled would be two answers.
func (repository *HangarOutputRepository) IncompleteHandoffs(ctx context.Context, tx output.Tx, limit int) ([]output.HandoffID, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a batch of %d handoffs", output.ErrIncomplete, limit)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT p.handoff_id
		FROM hangar_handoff_predeclarations p
		LEFT JOIN hangar_handoff_dispositions d ON d.handoff_id = p.handoff_id
		LEFT JOIN hangar_capture_reservations r ON r.handoff_id = p.handoff_id
		LEFT JOIN hangar_no_capture_dispositions n ON n.handoff_id = p.handoff_id
		LEFT JOIN hangar_pre_reservation_cancel_dispositions c ON c.handoff_id = p.handoff_id
		WHERE NOT coalesce(
			CASE d.disposition
			    WHEN 'capture' THEN r.release_acknowledged_at IS NOT NULL
			    WHEN 'no_capture' THEN n.release_acknowledged_at IS NOT NULL
			    WHEN 'pre_reservation_cancel' THEN c.finalized_at IS NOT NULL
			    ELSE false
			END, false)
		ORDER BY p.created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer rows.Close()

	var handoffs []output.HandoffID
	for rows.Next() {
		var handoff string
		if err := rows.Scan(&handoff); err != nil {
			return nil, err
		}
		handoffs = append(handoffs, output.HandoffID(handoff))
	}

	return handoffs, rows.Err()
}

// HangarConsumerPrefixForComponent is the recovery component's own token.
//
// The component is not composing with anyone's binding write -- it advances a
// capture and nothing else -- so the prefix it "holds" is empty by
// construction. It still names itself, because the whole point of the token is
// that a caller which reached the lock suffix has written down which caller it
// was, and an anonymous one would be a boolean with extra steps.
func HangarConsumerPrefixForComponent() HangarConsumerPrefix {
	prefix, err := HangarConsumerPrefixHeld("hangar-output-capture-component")
	if err != nil {
		// The name is a constant, so this cannot fail; a panic here would be a
		// programming error at startup rather than a runtime condition.
		panic("hangar: the capture component's own consumer prefix is invalid: " + err.Error())
	}

	return prefix
}

// hangarTerminalizeLogical closes the logical half of a terminal capture.
//
// It exists because a terminally failed or cancelled capture used to leave its
// logical reservation at `unresolved_generation` forever, and an unresolved
// reservation is a SHIELD: it protects its (scope, digest) correlation from
// orphan adoption (Req 40) and refuses reclaim admission (the schema's reclaim
// exclusion). A capture that has given up and kept its shield is a correlation
// nothing can ever collect -- the leak Req 40's own "or records terminal
// failure" arm exists to close.
//
// `registered` is never overwritten: a capture that registered a generation and
// then failed on a LATER step still resolved its logical identity, and calling
// that terminal would lose the resolution.
func hangarTerminalizeLogical(ctx context.Context, tx output.Tx, reservation output.ReservationID) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_logical_reservations SET state = 'terminal'
		WHERE reservation_id = $1 AND state = 'unresolved_generation'`,
		string(reservation)); err != nil {
		return hangarConflict(err)
	}

	return nil
}
