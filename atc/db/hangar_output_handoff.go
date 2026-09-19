package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// HangarOutputRepository is the PostgreSQL half of the Hangar output plane.
//
// It lives here rather than in hangar/output because atc/db.Tx already exists
// here, and a repository that accepted the caller's transaction from the leaf
// would have made the leaf depend on this package. The leaf declares the
// interfaces; this satisfies them.
//
// Every method takes the caller's Tx and nothing that can commit. A consumer's
// binding write and the Hangar operation beside it commit together or roll back
// together, which is only true if the transaction belongs to the caller --
// so nothing here begins, commits or rolls one back, and there is no method
// that could.
type HangarOutputRepository struct {
	prefix HangarConsumerPrefix
}

// NewHangarOutputRepository takes the consumer-prefix token rather than a
// string, so that a caller that never thought about its own domain locks cannot
// construct one.
func NewHangarOutputRepository(prefix HangarConsumerPrefix) *HangarOutputRepository {
	return &HangarOutputRepository{prefix: prefix}
}

var (
	_ output.CaptureRepository   = (*HangarOutputRepository)(nil)
	_ output.ClaimRepository     = (*HangarOutputRepository)(nil)
	_ output.ReadLeaseRepository = (*HangarOutputRepository)(nil)
	_ output.CancelSettler       = (*HangarOutputRepository)(nil)
)

// PredeclareHandoff records the pre-start, non-authorizing predeclaration.
//
// Repeating the same handoff identity with the same immutable facts returns the
// same durable state; reusing it for different facts is a conflict. That is
// what makes the identity an idempotency key rather than a name, and it is
// checked here rather than left to the unique index, because "the same facts"
// is a comparison the index cannot make.
func (repository *HangarOutputRepository) PredeclareHandoff(ctx context.Context, tx output.Tx, admission output.CaptureAdmission) error {
	if err := admission.Validate(); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_handoff_predeclarations
			(handoff_id, source_hold_id, execution_id, execution_fence, output_name,
			 activation_epoch, capture_deadline_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (handoff_id) DO NOTHING`,
		string(admission.HandoffID),
		string(admission.SourceHoldID),
		string(admission.Execution.ExecutionID),
		int64(admission.Execution.Fence),
		string(admission.Output),
		int64(admission.ActivationEpoch),
		admission.CaptureDeadline.Time,
	)
	if err != nil {
		return hangarConflict(err)
	}
	if inserted, err := result.RowsAffected(); err == nil && inserted == 1 {
		return nil
	}

	// The row already existed. Idempotent only if it says the same thing.
	var (
		lease, execution, name string
		fence, epoch           int64
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT source_hold_id, execution_id, execution_fence, output_name, activation_epoch
		FROM hangar_handoff_predeclarations WHERE handoff_id = $1`,
		[]any{string(admission.HandoffID)},
		&lease, &execution, &fence, &name, &epoch,
	); err != nil {
		return err
	}

	if lease != string(admission.SourceHoldID) ||
		execution != string(admission.Execution.ExecutionID) ||
		fence != int64(admission.Execution.Fence) ||
		name != string(admission.Output) ||
		epoch != int64(admission.ActivationEpoch) {
		return fmt.Errorf("%w: handoff %s was predeclared for a different execution, source "+
			"lease, output or epoch; a new build uses new identities",
			output.ErrConflict, admission.HandoffID)
	}

	return nil
}

// AcknowledgeSourceHold records the daemon's pre-start acknowledgement of the
// provisional, non-authorizing hold.
//
// It is a separate call from PredeclareHandoff because it is a separate fact
// with a separate author: the control plane predeclares, the node acknowledges,
// and Stage 2 needs both. It authorizes nothing -- not sealing, termination,
// deletion, publication or a binding -- and the schema has nowhere for it to.
func (repository *HangarOutputRepository) AcknowledgeSourceHold(ctx context.Context, tx output.Tx, acknowledgement output.CaptureAcknowledgement) error {
	if err := acknowledgement.ValidateAs(output.CaptureHoldAcknowledged); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_handoff_predeclarations
		SET hold_acknowledged_at = now()
		WHERE handoff_id = $1
		  AND source_hold_id = $2
		  AND execution_id = $3
		  AND hold_acknowledged_at IS NULL`,
		string(acknowledgement.HandoffID),
		string(acknowledgement.SourceHoldID),
		string(acknowledgement.Execution.ExecutionID),
	)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 1 {
		return nil
	}

	// Zero rows is either an idempotent repeat or a hold for facts that do not
	// match current execution admission. Start and recovery fail closed until
	// it is the former.
	var acknowledged sql.NullTime
	if err := hangarQueryRow(ctx, tx, `
		SELECT hold_acknowledged_at FROM hangar_handoff_predeclarations
		WHERE handoff_id = $1 AND source_hold_id = $2 AND execution_id = $3`,
		[]any{
			string(acknowledgement.HandoffID),
			string(acknowledgement.SourceHoldID),
			string(acknowledgement.Execution.ExecutionID),
		}, &acknowledged); err != nil {
		return fmt.Errorf("%w: no predeclaration matches the acknowledged hold for handoff %s",
			output.ErrInvalidIdentity, acknowledgement.HandoffID)
	}
	if !acknowledged.Valid {
		return fmt.Errorf("%w: the hold for handoff %s could not be acknowledged",
			output.ErrUnresolved, acknowledgement.HandoffID)
	}

	return nil
}

// RecordNoCaptureIntent wins the arbiter as no_capture and records the first
// half of an exact fenced release.
//
// The arbiter row and this child are written in one statement pair, because the
// exclusive branch and what that branch did are one decision. Losing the
// arbiter is ErrConflict: another branch already won, permanently.
func (repository *HangarOutputRepository) RecordNoCaptureIntent(ctx context.Context, tx output.Tx, disposition output.NoCaptureDisposition) error {
	if err := disposition.Validate(); err != nil {
		return err
	}

	witness, err := hangarMarshalWitness(disposition.FinishAcknowledgement)
	if err != nil {
		return err
	}

	repeated, err := repository.winArbiter(ctx, tx, disposition.HandoffID, output.DispositionNoCapture)
	if err != nil {
		return err
	}
	if repeated {
		return repository.confirmNoCaptureRepeat(ctx, tx, disposition)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_no_capture_dispositions
			(handoff_id, execution_id, activation_epoch, source_hold_id, reason,
			 release_intent_id, finish_acknowledgement)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(disposition.HandoffID),
		string(disposition.Execution.ExecutionID),
		int64(disposition.ActivationEpoch),
		string(disposition.SourceHoldID),
		string(disposition.Reason),
		string(disposition.ReleaseIntentID),
		witness,
	); err != nil {
		return hangarConflict(err)
	}

	return nil
}

func (repository *HangarOutputRepository) confirmNoCaptureRepeat(ctx context.Context, tx output.Tx, disposition output.NoCaptureDisposition) error {
	var reason, intent string
	if err := hangarQueryRow(ctx, tx, `
		SELECT reason, release_intent_id FROM hangar_no_capture_dispositions WHERE handoff_id = $1`,
		[]any{string(disposition.HandoffID)}, &reason, &intent); err != nil {
		return err
	}
	if reason != string(disposition.Reason) || intent != string(disposition.ReleaseIntentID) {
		return fmt.Errorf("%w: handoff %s already reached no_capture as %s under release intent "+
			"%s", output.ErrConflict, disposition.HandoffID, reason, intent)
	}

	return nil
}

// AcknowledgeNoCaptureRelease completes the second, daemon-side half.
//
// Until it exists the source stays held, the external non-success stays
// pending, and destructive cleanup is forbidden. Nothing here describes the
// pair as an atomic commit, because across two systems it is not one: recovery
// repeats the same release intent until committed-versus-not is known.
func (repository *HangarOutputRepository) AcknowledgeNoCaptureRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error {
	return repository.acknowledgeRelease(ctx, tx, acknowledgement,
		output.DispositionNoCapture, "hangar_no_capture_dispositions")
}

// RecordPreReservationCancelIntent wins the third exclusive branch.
//
// With no source incarnation reserved anywhere the branch closes here, with no
// daemon call at all: there is nothing on any node to release, and a release
// intent would be a promise to no one. With one, the intent is recorded and the
// acknowledgement is still owed.
func (repository *HangarOutputRepository) RecordPreReservationCancelIntent(ctx context.Context, tx output.Tx, disposition output.PreReservationCancelDisposition) error {
	if err := disposition.Validate(); err != nil {
		return err
	}

	repeated, err := repository.winArbiter(ctx, tx, disposition.HandoffID,
		output.DispositionPreReservationCancel)
	if err != nil {
		return err
	}
	if repeated {
		var held bool
		if err := hangarQueryRow(ctx, tx, `
			SELECT source_reserved FROM hangar_pre_reservation_cancel_dispositions
			WHERE handoff_id = $1`, []any{string(disposition.HandoffID)}, &held); err != nil {
			return err
		}
		if held != disposition.SourceReserved {
			return fmt.Errorf("%w: handoff %s already cancelled with source_reserved=%t",
				output.ErrConflict, disposition.HandoffID, held)
		}

		return nil
	}

	var intent any
	finalized := "NULL"
	if disposition.SourceReserved {
		intent = string(disposition.ReleaseIntentID)
	} else {
		// Nothing reserved anywhere, so nothing is owed and the branch is
		// terminal on the spot.
		finalized = "now()"
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO hangar_pre_reservation_cancel_dispositions
			(handoff_id, execution_id, activation_epoch, source_hold_id, source_reserved,
			 release_intent_id, finalized_at)
		VALUES ($1, $2, $3, $4, $5, $6, %s)`, finalized),
		string(disposition.HandoffID),
		string(disposition.Execution.ExecutionID),
		int64(disposition.ActivationEpoch),
		string(disposition.SourceHoldID),
		disposition.SourceReserved,
		intent,
	); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// AcknowledgePreReservationCancelRelease completes only the held form of that
// branch, and finalizes it.
func (repository *HangarOutputRepository) AcknowledgePreReservationCancelRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error {
	return repository.acknowledgeRelease(ctx, tx, acknowledgement,
		output.DispositionPreReservationCancel, "hangar_pre_reservation_cancel_dispositions")
}

func (repository *HangarOutputRepository) acknowledgeRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement, branch output.Disposition, table string) error {
	if err := acknowledgement.Validate(); err != nil {
		return err
	}
	if acknowledgement.Disposition != branch {
		return fmt.Errorf("%w: a %s release acknowledgement was offered to the %s branch",
			output.ErrIncomplete, acknowledgement.Disposition, branch)
	}

	body, err := json.Marshal(acknowledgement)
	if err != nil {
		return err
	}

	// Each branch's terminal fact has a different name, so the one that
	// finalizes it is named per branch rather than assumed. The capture branch
	// SETTLES: with the release acknowledged there is nothing still owed, which
	// is what settled already means on the other two.
	finalize := ""
	switch branch {
	case output.DispositionPreReservationCancel:
		finalize = ", finalized_at = coalesce(finalized_at, now())"
	case output.DispositionCapture:
		finalize = ", settled_at = coalesce(settled_at, now())"
	}

	// The capture branch releases in every TERMINAL state, on either side of
	// the irreversible publish point, and that is Phase 7 removing a special
	// case rather than adding one.
	//
	// The rule the guard used to carry was that a cancelled or failed capture
	// could not release once an object might exist -- a node working from stale
	// state. It had the consequence backwards. What a release releases is the
	// HOLD, never the bytes (the Phase 5 ruling): the incarnation stays on the
	// node as ordinary step content and reclamation by policy is what removes
	// it. A held source, by contrast, is exempt from payload cleanup, sweep and
	// reuse -- so a capture that could not release pinned an incarnation on a
	// node for the life of the deployment. And the schema EARNS settlement with
	// an acknowledged release, so the same capture could never settle, and its
	// correlation stayed shielded from orphan adoption forever.
	//
	// The object such a capture created is not lost by letting the hold go. It
	// carries that capture's marker; the sweep finds it; and once the
	// reservation is terminal and publication grace has elapsed, adoption takes
	// it. That is Req 40's "terminal settlement eventually permits marked-orphan
	// adoption", and the mechanism is inventory rather than a second ledger.
	//
	// What still has to be refused is a release for a capture that is not
	// terminal at all: a live capture's source is held because it is being
	// written, and an unsealed tree released mid-write is the seam Phase 4
	// closed. That rule is now a CHECK on the row --
	// hangar_release_intent_implies_terminal -- and deliberately NOT a second
	// state list here.
	//
	// The state list was here and could refuse nothing. All three writers of
	// release_intent_id set a terminal state in the same statement, so removing
	// the clause entirely left every suite green; a guard nothing can redden
	// reads like a control and is a comment. As a constraint it is a rule about
	// the ROW rather than about one path to it, so a fourth writer that minted
	// an intent for a live capture is refused whether or not it comes through
	// this statement, and a spec can put a row in front of it.
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET release_acknowledged_at = now(), release_acknowledgement = $3%s
		WHERE handoff_id = $1
		  AND release_intent_id = $2
		  AND release_acknowledged_at IS NULL`, table, finalize),
		string(acknowledgement.HandoffID),
		string(acknowledgement.ReleaseIntentID),
		body,
	)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 1 {
		return nil
	}

	// Zero rows: either this exact release is already acknowledged, which is
	// the idempotent repeat, or it names an intent this branch never recorded.
	var acknowledged sql.NullTime
	if err := hangarQueryRow(ctx, tx, fmt.Sprintf(`
		SELECT release_acknowledged_at FROM %s WHERE handoff_id = $1 AND release_intent_id = $2`, table),
		[]any{string(acknowledgement.HandoffID), string(acknowledgement.ReleaseIntentID)},
		&acknowledged,
	); err != nil {
		return fmt.Errorf("%w: no %s release intent %s for handoff %s; a release acknowledgement "+
			"is for one exact fenced release", output.ErrInvalidIdentity, branch,
			acknowledgement.ReleaseIntentID, acknowledgement.HandoffID)
	}
	if !acknowledged.Valid {
		// The intent is this branch's and it is unacknowledged, so the UPDATE
		// matched nothing for a reason that is not the intent.
		//
		// There used to be a branch here saying the handoff was past the
		// irreversible publish point and that "retrying this release will never
		// admit it". That is no longer true and the distinction it drew is no
		// longer this statement's to draw: past-the-point is not a reason to
		// refuse a release -- a release releases the HOLD, never the bytes, and
		// a terminal capture on either side of the point owes one. Leaving the
		// branch would have told a caller "never" about the one case that is
		// now retryable, which is the opposite of what a caller does with it.
		return fmt.Errorf("%w: release intent %s is recorded but unacknowledged",
			output.ErrUnresolved, acknowledgement.ReleaseIntentID)
	}

	return nil
}

// winArbiter takes the one-row disposition arbiter for a handoff.
//
// It reports whether this call is a repeat of the same branch. Losing to
// another branch is ErrConflict, permanently: the three branches exclude one
// another and the arbiter is what makes that true rather than hoped for.
//
// Winning it is not capture authority. It seals, publishes and binds nothing,
// and no caller may read a won arbiter as permission to do any of them.
func (repository *HangarOutputRepository) winArbiter(ctx context.Context, tx output.Tx, handoff output.HandoffID, branch output.Disposition) (bool, error) {
	if err := handoff.Validate(); err != nil {
		return false, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
		VALUES ($1, $2)
		ON CONFLICT (handoff_id) DO NOTHING`, string(handoff), string(branch))
	if err != nil {
		return false, hangarConflict(err)
	}
	if inserted, err := result.RowsAffected(); err == nil && inserted == 1 {
		return false, nil
	}

	var decided string
	if err := hangarQueryRow(ctx, tx, `
		SELECT disposition FROM hangar_handoff_dispositions WHERE handoff_id = $1`,
		[]any{string(handoff)}, &decided); err != nil {
		return false, err
	}
	if decided != string(branch) {
		return false, fmt.Errorf("%w: handoff %s is already dispositioned %s and cannot also "+
			"reach %s; the three branches exclude one another permanently",
			output.ErrConflict, handoff, decided, branch)
	}

	return true, nil
}

// hangarMarshalWitness stores the finish witness as it was, or nothing.
//
// Nothing is the right answer for a reconciliation: it exists precisely because
// no acknowledgement could be obtained, and a NULL is how the schema says so.
func hangarMarshalWitness(acknowledgement *executioncontrol.Acknowledgement) (any, error) {
	if acknowledgement == nil {
		return nil, nil
	}

	return json.Marshal(*acknowledgement)
}

// AcknowledgeCaptureRelease completes the capture branch's own fenced release.
//
// The capture branch owes one in exactly one situation, and the branch review's
// F7 is where that was settled: a capture that terminally cancels or fails
// before the irreversible publish point has decided something and released
// nothing, so the source is still held on some node until this statement says
// otherwise. `Settled` means the same thing on all three branches -- nothing is
// still owed -- and a cancelled capture with no acknowledged release is decided
// and unsettled, which is the state drain has to wait on.
//
// It is not owed past the publish point. There the capture settles a registered
// receipt or a terminal orphan, and there is nothing left to release; the state
// guard above is what refuses one offered anyway.
func (repository *HangarOutputRepository) AcknowledgeCaptureRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error {
	// The branch check is acknowledgeRelease's, and it is not repeated here:
	// there is one statement of "this acknowledgement belongs to this branch",
	// and a second copy could only drift from it. Mutation is what found this
	// one redundant -- removing it reddened nothing.
	return repository.acknowledgeRelease(ctx, tx, acknowledgement,
		output.DispositionCapture, "hangar_capture_reservations")
}
