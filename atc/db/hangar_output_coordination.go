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
		UPDATE hangar_capture_reservations r
		SET state = 'failed',
		    terminal_failure = $3,
		    release_intent_id = CASE
		        WHEN r.past_irreversible_publish_point THEN r.release_intent_id
		        ELSE coalesce(r.release_intent_id, gen_random_uuid())
		    END
		WHERE r.reservation_id = $1
		  AND `+hangarCurrentCaptureFence+` = $2
		  AND r.state IN ('unresolved', 'resolved')`,
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
		SELECT r.state, coalesce(r.terminal_failure, '') FROM hangar_capture_reservations r
		WHERE r.reservation_id = $1 AND `+hangarCurrentCaptureFence+` = $2`,
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
			    WHEN 'capture' THEN r.state = 'registered' OR r.release_acknowledged_at IS NOT NULL
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

// HangarOutputAnnouncer is the durable half of requirement 18.
//
// It is a thin adapter rather than a method on the repository because the
// coordinator's port takes no transaction: an announcement is its own fact and
// composes with nothing, so it owns the short transaction it commits in.
type HangarOutputAnnouncer struct {
	Conn       DbConn
	Repository *HangarOutputRepository
}

// Announce appends one announcement in its own transaction.
func (announcer *HangarOutputAnnouncer) Announce(ctx context.Context, handoff output.HandoffID, kind, disposition, reason string) error {
	tx, err := announcer.Conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)

	if err := announcer.Repository.RecordAnnouncement(ctx, tx, handoff, kind, disposition, reason); err != nil {
		return err
	}

	return tx.Commit()
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

// ReadEveryAnnouncement returns what the plane told watchers about EVERY
// handoff, in emission order.
//
// It exists for the absence half of requirement 18: "an ordinary step announces
// none of them" is a statement about what is NOT in the store, and a reader
// scoped to one handoff cannot make it. The whole store is small by
// construction -- three rows per capture, and only captures produce any.
func (repository *HangarOutputRepository) ReadEveryAnnouncement(ctx context.Context, tx output.Tx) ([]HangarAnnouncement, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT handoff_id, kind, coalesce(disposition, ''), reason
		FROM hangar_capture_announcements
		ORDER BY id`)
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
