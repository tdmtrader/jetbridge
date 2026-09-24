package db

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func hangarEpoch(value int64) executioncontrol.ActivationEpoch {
	return executioncontrol.ActivationEpoch(value)
}

func hangarExecutionID(value string) executioncontrol.ExecutionID {
	return executioncontrol.ExecutionID(value)
}

// ResolveLogicalReservation binds the server-derived scope and digest.
//
// It runs after canonicalization and before the first object create, and only
// for the current capture owner: the schema refuses a resolution at a fence
// other than the one the lease currently holds, so a stale owner resolves
// nothing. Scope and Digest arrive on the value the control plane filled in;
// there is no parameter here a task could reach.
//
// RECORDED, NOT FIXED: a resolution can land for a correlation whose generation
// has ALREADY been admitted to reclamation. hangar_check_reclaim_exclusion
// counts unresolved logical reservations and is attached to
// hangar_reclaim_jobs, hangar_claims and hangar_read_leases, not to this table;
// and AdmitReclaim's class-1 lock locks the rows that exist and cannot block an
// INSERT of one that does not. So Req 46's "no unresolved reservation" is an
// ADMISSION-TIME precondition, and a later resolution loses rather than being
// refused.
//
// It stays a precondition because the harm is bounded and self-healing and the
// alternative is not. A second capture of identical bytes dedupes onto the
// reclaiming generation, RegisterReceipt registers a receipt against it --
// upsertLifecycle deliberately does not rewrite `state`, so nothing is
// resurrected -- and the consumer's AcquireClaim then refuses with
// `reclaiming`. One capture fails; no consumer is handed a binding to bytes
// that are going away. Making this a refusal instead would mean a trigger on
// this table that failed a live capture for the sake of a reclamation that has
// not deleted anything yet.
func (repository *HangarOutputRepository) ResolveLogicalReservation(ctx context.Context, tx output.Tx, resolution output.LogicalResolution) error {
	if err := resolution.Validate(); err != nil {
		return err
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical:  []HangarLogicalKey{{Scope: resolution.Scope, Digest: resolution.Digest}},
		Captures: []output.ReservationID{resolution.ReservationID},
	}); err != nil {
		return err
	}

	// The state predicate is the whole reason this is an INSERT ... SELECT.
	// Cancellation is terminal (Req 11), and the cancellation does not take the
	// capture lease away -- so an owner still holding the current fence, which
	// is the only thing the schema checks here, could resolve a logical
	// identity for a capture the control plane had already given up on.
	result, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_logical_reservations
			(reservation_id, scope, digest, logical_bytes, capture_fence)
		SELECT $1, $2, $3, $4, $5
		FROM hangar_capture_reservations r
		WHERE r.reservation_id = $1 AND r.state IN ('unresolved', 'resolved')
		ON CONFLICT (reservation_id) DO NOTHING`,
		string(resolution.ReservationID),
		string(resolution.Scope),
		string(resolution.Digest),
		resolution.LogicalBytes,
		int64(resolution.CaptureFence),
	)
	if err != nil {
		return hangarConflict(err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		if err := hangarRefuseTerminalCapture(ctx, tx, resolution.ReservationID,
			"resolve a logical identity"); err != nil {
			return err
		}
	}
	if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hangar_capture_reservations SET state = 'resolved'
			WHERE reservation_id = $1 AND state = 'unresolved'`,
			string(resolution.ReservationID)); err != nil {
			return hangarConflict(err)
		}

		return nil
	}

	var scope, digest string
	if err := hangarQueryRow(ctx, tx, `
		SELECT scope, digest FROM hangar_logical_reservations WHERE reservation_id = $1`,
		[]any{string(resolution.ReservationID)}, &scope, &digest); err != nil {
		return err
	}
	if scope != string(resolution.Scope) || digest != string(resolution.Digest) {
		return fmt.Errorf("%w: reservation %s is already resolved to %s/%s; the logical identity "+
			"of published bytes is immutable", output.ErrConflict, resolution.ReservationID, scope, digest)
	}

	return nil
}

// RecordFirstObjectCreate marks the point past which cancellation can no longer
// unmake anything.
//
// It exists as its own call because the ordering it records is the whole reason
// the logical reservation exists: every possibly-created object must have a
// pre-existing reservation that recovery and inventory can correlate, and the
// schema refuses this write when there is none.
//
// It takes the fence it is offered under. Req 10: a stale owner may not seal,
// publish, sign/register a receipt, finalize or release -- and this is the
// publish half. Every other write on this path is fenced, by the schema for a
// logical resolution and by the lease itself for ownership; without the fence
// here, any caller holding a reservation id could move a capture past the one
// point nothing can walk back, including an owner that was superseded minutes
// ago and does not know it.
func (repository *HangarOutputRepository) RecordFirstObjectCreate(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence) error {
	if err := reservation.Validate(); err != nil {
		return err
	}
	if fence <= 0 {
		return fmt.Errorf("%w: the irreversible publish point is recorded under the capture fence "+
			"it was reached at; %d names no ownership", output.ErrIncomplete, fence)
	}

	// The capture class, named rather than taken by the UPDATE below. One class
	// taken implicitly cannot invert against anything -- there is nothing to
	// invert with -- but a lock the suffix never hears about is a lock the
	// order rule cannot see, and that invisibility is what let two writers take
	// classes 3 and 1 the wrong way round for ten phases.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Captures: []output.ReservationID{reservation},
	}); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations r
		SET first_create_attempted_at = coalesce(r.first_create_attempted_at, now()),
		    past_irreversible_publish_point = true
		WHERE r.reservation_id = $1
		  AND r.state IN ('unresolved', 'resolved')
		  AND EXISTS (
			SELECT 1 FROM hangar_capture_attempt_leases l
			WHERE l.reservation_id = r.reservation_id AND l.capture_fence = $2)`,
		string(reservation), int64(fence))
	if err != nil {
		return hangarConflict(err)
	}
	recorded, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if recorded == 0 {
		// Two reasons produce zero rows, and they are different answers. A
		// terminal capture is named first because "a stale owner may not
		// publish" would be a lie about a live owner whose capture was
		// cancelled underneath it, and the caller's next move differs: a stale
		// owner stops, a cancelled owner releases the source.
		if err := hangarRefuseTerminalCapture(ctx, tx, reservation,
			"pass the irreversible publish point"); err != nil {
			return err
		}

		return fmt.Errorf("%w: reservation %s is not owned at capture fence %d; a stale owner may "+
			"not publish", executioncontrol.ErrStaleFence, reservation, fence)
	}

	return nil
}

// hangarRefuseTerminalCapture reports a conflict when the reservation has
// already reached a state it cannot leave.
//
// It reads the state rather than inferring it, and it returns nil when the
// capture is still live, so the caller keeps whatever refusal it had for the
// other reasons the same statement can affect no rows.
func hangarRefuseTerminalCapture(ctx context.Context, tx output.Tx, reservation output.ReservationID, attempted string) error {
	var state string
	switch err := hangarQueryRow(ctx, tx,
		`SELECT state FROM hangar_capture_reservations WHERE reservation_id = $1`,
		[]any{string(reservation)}, &state); {
	case errors.Is(err, output.ErrNotFound):
		return nil
	case err != nil:
		return err
	}

	switch state {
	case "cancelled":
		return fmt.Errorf("%w: capture %s was terminally cancelled; an owner holding the current "+
			"fence may not %s afterwards, because cancellation before the irreversible publish "+
			"point is what makes the owed source release owed for a capture that published "+
			"nothing", output.ErrConflict, reservation, attempted)
	case "registered", "failed":
		return fmt.Errorf("%w: capture %s is %s, which is terminal; it may not %s",
			output.ErrConflict, reservation, state, attempted)
	}

	return nil
}

// RegisterReceipt admits a verified receipt into durable lifecycle state.
//
// The ref may not be known to the caller's lock set before it reads: the
// generation is assigned by the store. So this derives the candidate lock set
// without row locks, takes the suffix, and then revalidates every derived fact
// under those locks -- and a mismatch is a typed retry rather than a silent
// write against facts that moved.
func (repository *HangarOutputRepository) RegisterReceipt(ctx context.Context, tx output.Tx, admission output.ReceiptAdmission) error {
	if err := admission.Validate(); err != nil {
		return err
	}

	claims := admission.Receipt.Claims
	derived, err := DeriveHangarRefUnlocked(ctx, tx, claims.ReservationID)
	if err != nil {
		return err
	}
	if !derived.Resolved {
		return fmt.Errorf("%w: reservation %s has no logical reservation; a ref may not be "+
			"registered ahead of the correlation that recovery and inventory use",
			output.ErrIncomplete, claims.ReservationID)
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical:  []HangarLogicalKey{derived.Logical},
		Exact:    []hangar.TreeRef{claims.Ref},
		Captures: []output.ReservationID{claims.ReservationID},
		Receipts: []output.ReservationID{claims.ReservationID},
	})
	if err != nil {
		return err
	}
	if err := locks.RevalidateDerivation(ctx, tx, derived); err != nil {
		return err
	}
	if derived.Logical.Scope != claims.Ref.Scope || derived.Logical.Digest != claims.Ref.Digest {
		return fmt.Errorf("%w: the receipt registers %s/%s against a reservation resolved to %s/%s",
			output.ErrConflict, claims.Ref.Scope, claims.Ref.Digest,
			derived.Logical.Scope, derived.Logical.Digest)
	}

	// A receipt already registered for this reservation answers the question
	// before anything is written, and the answer is one of exactly two. The
	// same tree ref is one admission repeated -- an ambiguous create response
	// converges through verified per-capture retry, so the retry must return
	// the state the first attempt made. Another generation is Req 6's reuse
	// for different facts, and it is a conflict.
	//
	// The read is here, under the Receipts lock the suffix already took, and
	// before the lifecycle upsert, because the third outcome -- writing the
	// second generation's lifecycle and then dropping its receipt on an ON
	// CONFLICT DO NOTHING -- leaves one capture with two records, the newer
	// one `registered` with no receipt naming it and nothing to correlate it
	// with. Silence is the one answer this must never give.
	var (
		registeredScope, registeredDigest string
		registeredGeneration              int64
	)
	switch err := hangarQueryRow(ctx, tx, `
		SELECT l.scope, l.digest, l.generation
		FROM hangar_output_receipts r
		JOIN hangar_exact_lifecycles l ON l.id = r.lifecycle_id
		WHERE r.reservation_id = $1`,
		[]any{string(claims.ReservationID)},
		&registeredScope, &registeredDigest, &registeredGeneration); {
	case err == nil:
		if registeredScope != string(claims.Ref.Scope) ||
			registeredDigest != string(claims.Ref.Digest) ||
			registeredGeneration != claims.Ref.Generation {
			return fmt.Errorf("%w: reservation %s already registered %s/%s/%d; a receipt naming "+
				"%s/%s/%d reuses one capture's identity for different facts",
				output.ErrConflict, claims.ReservationID,
				registeredScope, registeredDigest, registeredGeneration,
				claims.Ref.Scope, claims.Ref.Digest, claims.Ref.Generation)
		}

		return nil
	case !errors.Is(err, output.ErrNotFound):
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_receipt_stat_challenges SET consumed_at = now()
		WHERE nonce = $1 AND consumed_at IS NULL AND not_after > now()`,
		admission.ChallengeNonce); err != nil {
		return hangarConflict(err)
	}

	lifecycle, err := repository.upsertLifecycle(ctx, tx, claims.Ref, admission.Metageneration,
		int64(claims.ActivationEpoch), "registered")
	if err != nil {
		return err
	}

	body, err := json.Marshal(claims)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_output_receipts
			(reservation_id, lifecycle_id, handoff_id, activation_epoch, receipt_key_id,
			 challenge_nonce, marker_version, algorithm, claims, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (reservation_id) DO NOTHING`,
		string(claims.ReservationID), lifecycle, string(claims.HandoffID),
		int64(claims.ActivationEpoch), admission.Receipt.KeyID, admission.ChallengeNonce,
		claims.MarkerVersion, admission.Receipt.Algorithm, body, admission.Receipt.Signature,
	); err != nil {
		return hangarConflict(err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_logical_reservations SET state = 'registered' WHERE reservation_id = $1`,
		string(claims.ReservationID)); err != nil {
		return hangarConflict(err)
	}
	// Registered, and NOT settled. A capture still owes the fenced release of
	// the source it sealed: Req 11 orders the exact receipt before that
	// release, and a held source is exempt from payload cleanup, sweep and
	// reuse, so stamping settled here would call a capture finished while its
	// incarnation was pinned on a node forever. `settled_at` is the release's
	// to stamp, exactly as it is on the other two branches.
	//
	// The intent id is minted here, by the database, for the same reason
	// CancelOrSettle mints one: the release is a pair, the acknowledgement
	// names one exact intent, and a release with nothing to name has no answer
	// to "which release was this".
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations
		SET state = 'registered',
		    release_intent_id = coalesce(release_intent_id, gen_random_uuid())
		WHERE reservation_id = $1`, string(claims.ReservationID)); err != nil {
		return hangarConflict(err)
	}

	return nil
}

func (repository *HangarOutputRepository) readReceiptForHandoff(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.Receipt, error) {
	var (
		body      []byte
		keyID     string
		algorithm string
		signature string
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT claims, receipt_key_id, algorithm, signature
		FROM hangar_output_receipts WHERE handoff_id = $1`,
		[]any{string(handoff)}, &body, &keyID, &algorithm, &signature); err != nil {
		return output.Receipt{}, err
	}

	var claims output.ReceiptClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		return output.Receipt{}, fmt.Errorf("%w: stored receipt claims for handoff %s do not "+
			"decode: %v", output.ErrCorrupt, handoff, err)
	}

	return output.Receipt{
		Claims:    claims,
		KeyID:     keyID,
		Algorithm: algorithm,
		Signature: signature,
	}, nil
}

// AdoptManagedOrphan records the lifecycle of a marked, unregistered generation
// that inventory found in the deployment's own bucket.
//
// It is the same lifecycle table registration writes, and it serializes on the
// same exact-lifecycle lock, which is what makes adoption and a late receipt
// registration one winner rather than two records of one generation -- the Req
// 33 lifecycle boundary, stated once and taken here.
//
// The precondition is Req 40's, restated in code because a rule that lives only
// in a test is a rule the next reader has to go looking for. EVERY correlated
// logical reservation must be resolved or terminal; the capture deadline plus
// the safety margin must have passed on the DATABASE clock; a terminal
// disposition must be recorded; and the source must have been released. An
// unresolved correlated reservation refuses adoption with a typed protected
// outcome NO MATTER how much grace has passed -- grace reduces work and
// provides recovery margin, and it is never the claim/reclaim mutex.
//
// What this writes is lifecycle state and nothing else. It never fabricates a
// capture, a receipt or a binding: receipts are daemon-signed (Req 25) and
// registration on retry belongs to the fenced capture owner (Req 40), while
// the inventory principal that called this holds list and get and nothing more
// (Req 54(b)).
func (repository *HangarOutputRepository) AdoptManagedOrphan(ctx context.Context, tx output.Tx, request output.AdoptionRequest) (output.AdoptionOutcome, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	ref := request.Ref

	// An object marked by another epoch is not this epoch's to adopt. It is
	// recorded and left completely alone: a lifecycle row under an epoch whose
	// attestation does not cover the object would be this cohort claiming
	// another's work, and relabelling it is what Req 45 forbids outright.
	if request.Marker.ActivationEpoch != request.ActivationEpoch {
		return output.AdoptionForeignEpoch, fmt.Errorf("%w: the object at generation %d is marked "+
			"for activation epoch %d and this sweep runs under %d; it is recorded for diagnosis "+
			"and never relabelled, adopted or deleted", output.ErrConflict, ref.Generation,
			request.Marker.ActivationEpoch, request.ActivationEpoch)
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	}); err != nil {
		return "", err
	}

	// Already ours. Adoption is for a generation with no lifecycle row, and an
	// upsert here would quietly rewrite a REGISTERED row's origin.
	var registered int
	if err := hangarQueryRow(ctx, tx, `
		SELECT count(*) FROM hangar_exact_lifecycles
		WHERE scope = $1 AND digest = $2 AND generation = $3`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation}, &registered); err != nil {
		return "", err
	}
	if registered > 0 {
		return output.AdoptionAlreadyRegistered, nil
	}

	// The four correlated-capture counts, in one statement on the database's
	// clock. They are counts and not a boolean because the refusal names which
	// one stopped it, and an operator asking "why is this object still here"
	// gets the answer rather than "not yet".
	var unresolved, nonterminal, undeadlined, unsettled int
	var graceElapsed bool
	if err := hangarQueryRow(ctx, tx, `
		SELECT
			count(*) FILTER (WHERE r.state = 'unresolved_generation'),
			count(*) FILTER (WHERE c.state NOT IN ('registered', 'failed', 'cancelled')),
			count(*) FILTER (WHERE c.capture_deadline_at + $3::interval > now()),
			count(*) FILTER (WHERE c.settled_at IS NULL),
			$4::timestamptz <= now() - $5::interval
		FROM hangar_logical_reservations r
		JOIN hangar_capture_reservations c USING (reservation_id)
		WHERE r.scope = $1 AND r.digest = $2`,
		[]any{
			string(ref.Scope), string(ref.Digest),
			hangarInterval(request.SafetyMargin),
			request.CreatedAt.UTC(), hangarInterval(request.Grace),
		},
		&unresolved, &nonterminal, &undeadlined, &unsettled, &graceElapsed); err != nil {
		return "", err
	}

	// The shield first, and unconditionally. Every other refusal below is a
	// "not yet"; this one is "not while that capture is alive", and it holds
	// however old the object is.
	var pendingInputs int
	if err := hangarQueryRow(ctx, tx, `SELECT count(*) FROM hangar_input_publications
		WHERE scope=$1 AND digest=$2 AND lifecycle_id IS NULL AND expires_at > clock_timestamp()`,
		[]any{string(ref.Scope), string(ref.Digest)}, &pendingInputs); err != nil {
		return "", err
	}
	if pendingInputs > 0 {
		return output.AdoptionProtectedByReservation, fmt.Errorf("%w: an input publication still correlates this object", output.ErrConflict)
	}
	if unresolved > 0 || nonterminal > 0 {
		return output.AdoptionProtectedByReservation, fmt.Errorf("%w: %d unresolved reservation(s) "+
			"and %d nonterminal capture(s) still correlate %s/%s; an unresolved reservation "+
			"protects its correlation from adoption even before a generation is known, and "+
			"however much grace has elapsed", output.ErrConflict, unresolved, nonterminal,
			ref.Scope, ref.Digest)
	}
	if undeadlined > 0 {
		return output.AdoptionBeforeCaptureDeadline, fmt.Errorf("%w: %d correlated capture(s) of "+
			"%s/%s are within their capture deadline plus the %s safety margin on the database "+
			"clock", output.ErrConflict, undeadlined, ref.Scope, ref.Digest, request.SafetyMargin)
	}
	if unsettled > 0 {
		return output.AdoptionCaptureNotSettled, fmt.Errorf("%w: %d correlated capture(s) of "+
			"%s/%s are decided and not settled; Req 40 wants the source released, not only the "+
			"decision taken", output.ErrConflict, unsettled, ref.Scope, ref.Digest)
	}
	if !graceElapsed {
		return output.AdoptionWithinPublicationGrace, fmt.Errorf("%w: the object at generation %d "+
			"was created at %s and its %s publication grace has not elapsed on the database "+
			"clock; adopting it would race the capture that created it", output.ErrConflict,
			ref.Generation, request.CreatedAt.UTC().Format(time.RFC3339), request.Grace)
	}

	if _, err := repository.upsertLifecycle(ctx, tx, ref, request.Metageneration,
		int64(request.ActivationEpoch), "adopted"); err != nil {
		return "", err
	}

	return output.AdoptionAdopted, nil
}

func (repository *HangarOutputRepository) upsertLifecycle(ctx context.Context, tx output.Tx, ref hangar.TreeRef, metageneration, epoch int64, origin string) (int64, error) {
	if metageneration <= 0 {
		return 0, fmt.Errorf("%w: no metageneration was observed for %s/%s/%d",
			output.ErrIncomplete, ref.Scope, ref.Digest, ref.Generation)
	}

	var id int64
	err := hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_exact_lifecycles
			(scope, digest, generation, metageneration, activation_epoch, marker_version,
			 origin, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (scope, digest, generation) DO UPDATE SET updated_at = now()
		RETURNING id`,
		[]any{
			string(ref.Scope), string(ref.Digest), ref.Generation,
			metageneration, epoch, output.MarkerVersion, origin,
		}, &id)
	if err != nil {
		return 0, err
	}

	return id, nil
}

// AcquireClaim composes Hangar protection with a consumer's own write.
//
// Idempotent for the same id and tree ref; the same id on another ref is a
// conflict, because a claim protects one immutable generation and cannot float
// to replacement content. The exact-lifecycle lock is what makes this and
// reclaim admission one winner: claimant first and the reclaimer rechecks and
// skips, reclaimer first and this transaction rolls back with no usable
// binding.
//
// THE COMMIT IS THE CONSUMER'S, AND SO IS THE REFUSAL. This runs inside the
// consumer's transaction (Reqs 30 and 31), so the deferred
// hangar_policy_admits_new_protection fires at the CONSUMER's COMMIT and
// arrives there as a bare driver error carrying SQLSTATE JB002 -- nothing this
// method returns, and nothing a caller can branch on. A consumer composing this
// into its own transaction MUST pass its commit error through
// db.HangarCommitError (or commit through db.HangarOutputTx, which is that
// function with a Commit around it). A consumer that does not will read a
// integrity denial as an ambiguous commit and retry a refusal that never clears.
// ReleaseClaim says the same, for the same reason.
func (repository *HangarOutputRepository) AcquireClaim(ctx context.Context, tx output.Tx, acquisition output.ClaimAcquisition) error {
	if err := acquisition.Validate(); err != nil {
		return err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: acquisition.Ref.Scope, Digest: acquisition.Ref.Digest}},
		Exact:   []hangar.TreeRef{acquisition.Ref},
		Claims:  []output.ClaimID{acquisition.ClaimID},
	})
	if err != nil {
		return err
	}
	lifecycle, err := locks.LifecycleID(acquisition.Ref)
	if err != nil {
		return err
	}

	var state string
	var epoch int64
	if err := hangarQueryRow(ctx, tx, `
		SELECT state, activation_epoch FROM hangar_exact_lifecycles WHERE id = $1`,
		[]any{lifecycle}, &state, &epoch); err != nil {
		return err
	}
	if state != "registered" && state != "adopted" {
		return fmt.Errorf("%w: %s/%s/%d is %s; a claim protects a readable registered or adopted "+
			"generation", output.ErrConflict, acquisition.Ref.Scope, acquisition.Ref.Digest,
			acquisition.Ref.Generation, state)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (claim_id) DO NOTHING`,
		string(acquisition.ClaimID), lifecycle, epoch, string(acquisition.ConsumerBindingID))
	if err != nil {
		return hangarConflict(err)
	}
	if inserted, err := result.RowsAffected(); err == nil && inserted == 1 {
		return nil
	}

	var existing int64
	var released sql.NullTime
	if err := hangarQueryRow(ctx, tx, `
		SELECT lifecycle_id, released_at FROM hangar_claims WHERE claim_id = $1`,
		[]any{string(acquisition.ClaimID)}, &existing, &released); err != nil {
		return err
	}
	if existing != lifecycle {
		return fmt.Errorf("%w: claim %s already protects another tree ref; reuse for another ref "+
			"is a conflict", output.ErrConflict, acquisition.ClaimID)
	}
	if released.Valid {
		return fmt.Errorf("%w: claim %s was released at %s and stays tombstoned for the lifetime "+
			"of the tree-ref lifecycle record", output.ErrConflict, acquisition.ClaimID, released.Time)
	}

	return nil
}

// ReleaseClaim gives up protection beside the consumer making its own binding
// unusable, in one transaction.
//
// Releasing an active or already-released claim is idempotent. The identity is
// never reused: the row is the tombstone, and the schema refuses both its
// deletion and its reactivation.
//
// Like AcquireClaim, this runs inside the CONSUMER's transaction, so a deferred
// refusal reaches the consumer at its own COMMIT as an unclassified driver
// error. Pass it through db.HangarCommitError.
func (repository *HangarOutputRepository) ReleaseClaim(ctx context.Context, tx output.Tx, release output.ClaimRelease) error {
	if err := release.Validate(); err != nil {
		return err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: release.Ref.Scope, Digest: release.Ref.Digest}},
		Exact:   []hangar.TreeRef{release.Ref},
		Claims:  []output.ClaimID{release.ClaimID},
	})
	if err != nil {
		return err
	}
	lifecycle, err := locks.LifecycleID(release.Ref)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_claims SET released_at = coalesce(released_at, now())
		WHERE claim_id = $1 AND lifecycle_id = $2`,
		string(release.ClaimID), lifecycle)
	if err != nil {
		return hangarConflict(err)
	}
	if updated, err := result.RowsAffected(); err == nil && updated == 0 {
		return fmt.Errorf("%w: claim %s does not protect %s/%s/%d", output.ErrNotFound,
			release.ClaimID, release.Ref.Scope, release.Ref.Digest, release.Ref.Generation)
	}

	return nil
}

// AcquireReadLease creates the reader's fenced protection inside the caller's
// transaction.
//
// It signs nothing and calls nobody. Minting the warrant that carries this lease
// happens after the transaction commits, deliberately: signing is not a
// database operation, and saying the two were atomic would be the cross-system
// atomic-commit claim this design refuses to make anywhere else.
func (repository *HangarOutputRepository) AcquireReadLease(ctx context.Context, tx output.Tx, request output.ReadLeaseRequest) (output.ReadLease, error) {
	if err := request.Validate(); err != nil {
		return output.ReadLease{}, err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical:    []HangarLogicalKey{{Scope: request.Ref.Scope, Digest: request.Ref.Digest}},
		Exact:      []hangar.TreeRef{request.Ref},
		Claims:     []output.ClaimID{request.ClaimID},
		ReadLeases: []output.ReadLeaseID{request.ReadLeaseID},
	})
	if err != nil {
		return output.ReadLease{}, err
	}
	lifecycle, err := locks.LifecycleID(request.Ref)
	if err != nil {
		return output.ReadLease{}, err
	}

	// The exact lifecycle, under the lock, before anything is written. Req 35
	// admits a managed-output warrant only for a REGISTERED MARKED generation
	// whose lifecycle state is readable: a caller-supplied ref, a stale receipt
	// or the ordinary strict-input path cannot reach this, and neither can a
	// generation that reclamation has already admitted.
	//
	// The marker version is not read here: hangar_exact_lifecycles constrains
	// it to the accepted version at the column, so a row with another one
	// cannot exist and a check would be code no state can reach. What CAN
	// differ is the marker on the object the stat just saw, and
	// ReadLeaseRequest.Validate refuses that before this transaction opens.
	var state string
	var metageneration, epoch int64
	if err := hangarQueryRow(ctx, tx, `
		SELECT state, metageneration, activation_epoch
		FROM hangar_exact_lifecycles WHERE id = $1`,
		[]any{lifecycle}, &state, &metageneration, &epoch); err != nil {
		return output.ReadLease{}, err
	}
	if state != "registered" && state != "adopted" {
		return output.ReadLease{}, fmt.Errorf("%w: %s/%s/%d is %s; a managed read is warranted only "+
			"against a readable registered or adopted generation", output.ErrConflict,
			request.Ref.Scope, request.Ref.Digest, request.Ref.Generation, state)
	}
	if int64(request.ActivationEpoch) != epoch {
		return output.ReadLease{}, fmt.Errorf("%w: the read names epoch %d and %s/%s/%d was "+
			"registered under epoch %d", output.ErrConflict, request.ActivationEpoch,
			request.Ref.Scope, request.Ref.Digest, request.Ref.Generation, epoch)
	}

	// And the stat, revalidated against the durable row it claims to describe.
	// The stat itself ran outside these locks -- it is a network call and no
	// lock is held across one -- so what makes it evidence is this comparison
	// and its freshness, both decided on the DATABASE clock.
	if request.StatProof.Metageneration != metageneration {
		return output.ReadLease{}, fmt.Errorf("%w: the stat observed metageneration %d and the "+
			"registered generation is at %d; the object at that key is not the one this lifecycle "+
			"records", output.ErrConflict, request.StatProof.Metageneration, metageneration)
	}
	if err := hangarStatProofFresh(ctx, tx, request.StatObservedAt); err != nil {
		return output.ReadLease{}, err
	}

	// The absence of a claim and the failure to ask are different answers, and
	// wrapping both as ErrNotFound told a caller "there is no claim" when what
	// happened was that the database could not be reached. A consumer reads
	// that as "my binding is gone" and gives up; the honest answer sends it
	// back to try again.
	var released sql.NullTime
	if err := hangarQueryRow(ctx, tx, `
		SELECT released_at FROM hangar_claims WHERE claim_id = $1 AND lifecycle_id = $2`,
		[]any{string(request.ClaimID), lifecycle}, &released); err != nil {
		if !errors.Is(err, output.ErrNotFound) {
			return output.ReadLease{}, err
		}

		return output.ReadLease{}, fmt.Errorf("%w: no claim %s protects %s/%s/%d; a managed-output "+
			"warrant needs at least one active claim", output.ErrNotFound, request.ClaimID,
			request.Ref.Scope, request.Ref.Digest, request.Ref.Generation)
	}
	if released.Valid {
		return output.ReadLease{}, fmt.Errorf("%w: claim %s was already released",
			output.ErrConflict, request.ClaimID)
	}

	// The admitted term, derived once and STORED, because a renewal warrants one
	// of them and the row is the only place that length can come from later.
	term := output.LeaseTermFor(request.MaterializationTimeout)
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return output.ReadLease{}, err
	}

	// Idempotent on the identity, and a conflict for different facts.
	//
	// A repeat is a RETRY, not a renewal: the lease id and the nonce are the
	// caller's, generated before the attempt, so a caller whose commit answer
	// was lost asks again with the same ones. Advancing the fence or the expiry
	// there would hand the retry a different lease than the one that may
	// already be committed, and requirement 37's byte-identical re-mint would
	// be impossible to honour. Extending a live lease is RenewReadLease's, and
	// it is a different question asked by a different actor.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_read_leases
			(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at,
			 lease_term_seconds, grant_nonce, destination_handle, destination_volume,
			 stat_metageneration, stat_marker_version, stat_observed_at)
		VALUES ($1, $2, $3, $4, 1, now() + $5::interval, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (read_lease_id) DO NOTHING`,
		string(request.ReadLeaseID), string(request.ClaimID), lifecycle,
		int64(request.ActivationEpoch), interval, int(term.Round(time.Second).Seconds()),
		request.WarrantNonce, request.Destination.Handle, request.Destination.Volume,
		request.StatProof.Metageneration, request.StatProof.Marker.Version,
		request.StatObservedAt.Time,
	); err != nil {
		return output.ReadLease{}, hangarConflict(err)
	}

	var (
		existingClaim, existingNonce, existingHandle, existingVolume string
		existingLifecycle, existingEpoch, fence                      int64
		granted, expires                                             time.Time
		leaseReleased                                                sql.NullTime
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT claim_id, lifecycle_id, activation_epoch, lease_fence, granted_at, expires_at,
		       released_at, grant_nonce, destination_handle, destination_volume
		FROM hangar_read_leases WHERE read_lease_id = $1`,
		[]any{string(request.ReadLeaseID)},
		&existingClaim, &existingLifecycle, &existingEpoch, &fence, &granted, &expires,
		&leaseReleased, &existingNonce, &existingHandle, &existingVolume); err != nil {
		return output.ReadLease{}, err
	}
	if leaseReleased.Valid {
		return output.ReadLease{}, fmt.Errorf("%w: read lease %s was released at %s and stays "+
			"tombstoned; a released reader does not reactivate", output.ErrConflict,
			request.ReadLeaseID, leaseReleased.Time)
	}
	if existingClaim != string(request.ClaimID) || existingLifecycle != lifecycle ||
		existingEpoch != int64(request.ActivationEpoch) ||
		existingNonce != request.WarrantNonce ||
		existingHandle != request.Destination.Handle ||
		existingVolume != request.Destination.Volume {
		return output.ReadLease{}, fmt.Errorf("%w: read lease %s already protects another read; "+
			"reuse of a lease identity for different facts is a conflict", output.ErrConflict,
			request.ReadLeaseID)
	}

	return output.ReadLease{
		ProtocolVersion: output.ProtocolVersion,
		ReadLeaseID:     request.ReadLeaseID,
		ClaimID:         request.ClaimID,
		Ref:             request.Ref,
		ActivationEpoch: request.ActivationEpoch,
		LeaseFence:      output.LeaseFence(fence),
		GrantedAt:       output.NewTimestamp(granted),
		ExpiresAt:       output.NewTimestamp(expires),
	}, nil
}

// RenewReadLease extends the reader's authority while work continues. Work may
// only begin, or continue, with enough of the lease left to finish inside it.
//
// ONE TERM FROM NOW, and the term is the row's. Deriving it from
// `ExpiresAt - GrantedAt` of the lease being renewed compounds: expires_at is
// what the previous renewal moved and granted_at never moves, so the k-th
// renewal would be k-1 intervals longer than the first and an abandoned reader
// would pin its generation for far longer than the term it was admitted under.
// Requirement 36 names one term, and lease_term_seconds is where it lives.
//
// The row's own state is checked HERE rather than left to the caller. The one
// production caller does run ValidateReadLease first, which refuses an expired
// lease -- but this method is on the repository contract for any caller, and a
// renewal that resurrected an expired lease would re-pin a generation recovery
// had already released. The lifecycle join is the same rule AcquireReadLease
// admits under: a generation recorded missing or conflicted is not one a reader
// may keep protecting.
func (repository *HangarOutputRepository) RenewReadLease(ctx context.Context, tx output.Tx, lease output.ReadLease) (output.ReadLease, error) {
	if err := lease.Validate(); err != nil {
		return output.ReadLease{}, err
	}

	// The suffix, and it names the generation as well as the lease.
	//
	// Class 4 alone does not intersect AdmitReclaim's class 1 and 2, so the two
	// took DISJOINT lock sets and nothing serialized them at all. What was left
	// was the deferred hangar_reclaim_exclusion trigger, and a deferred
	// constraint trigger is a SNAPSHOT READ, not a mutex: it fires inside its
	// own transaction, before that transaction's commit is visible, so two
	// transactions whose constraint phases overlap each see the other as
	// uncommitted and BOTH commit. That was reproduced: a live renewed read
	// lease and an admitted reclaim job committed together for the same
	// generation, which is what AC 13 and Req 36 forbid.
	//
	// The trigger stays, and it is a good backstop -- it closes every SEQUENTIAL
	// pair, which is what a backstop is for. It is the exact-lifecycle row,
	// class 2, that both this and AdmitReclaim now hold, that makes the
	// concurrent pair impossible rather than unlikely.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical:    []HangarLogicalKey{{Scope: lease.Ref.Scope, Digest: lease.Ref.Digest}},
		Exact:      []hangar.TreeRef{lease.Ref},
		ReadLeases: []output.ReadLeaseID{lease.ReadLeaseID},
	}); err != nil {
		return output.ReadLease{}, err
	}

	var expires time.Time
	var fence int64
	if err := hangarQueryRow(ctx, tx, `
		UPDATE hangar_read_leases r
		SET renewed_at = now(),
		    expires_at = now() + make_interval(secs => r.lease_term_seconds)
		WHERE r.read_lease_id = $1
		  AND r.released_at IS NULL
		  AND r.expires_at > now()
		  AND EXISTS (
			SELECT 1 FROM hangar_exact_lifecycles l
			WHERE l.id = r.lifecycle_id AND l.state IN ('registered', 'adopted'))
		RETURNING r.expires_at, r.lease_fence`,
		[]any{string(lease.ReadLeaseID)},
		&expires, &fence); err != nil {
		return output.ReadLease{}, fmt.Errorf("%w: read lease %s is released, expired, or "+
			"protects a generation that is no longer readable",
			output.ErrConflict, lease.ReadLeaseID)
	}

	renewed := lease
	renewed.ExpiresAt = output.NewTimestamp(expires)
	renewed.LeaseFence = output.LeaseFence(fence)

	return renewed, nil
}

// ReleaseReadLease closes the reader's protection after verified staging. It is
// idempotent, and the row is retained: its tombstone is what prevents a stale
// resurrection.
func (repository *HangarOutputRepository) ReleaseReadLease(ctx context.Context, tx output.Tx, lease output.ReadLease) error {
	if err := lease.ReadLeaseID.Validate(); err != nil {
		return err
	}
	// The same set the renewal takes, for symmetry rather than for necessity:
	// a release only ever REMOVES protection, so a reclaim admission that ran
	// beside it could not be made wrong by it. Two writers of the same row that
	// take different sets is how the renewal's disjoint set went unnoticed, so
	// they take the same one.
	if err := lease.Ref.Validate(); err != nil {
		return err
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical:    []HangarLogicalKey{{Scope: lease.Ref.Scope, Digest: lease.Ref.Digest}},
		Exact:      []hangar.TreeRef{lease.Ref},
		ReadLeases: []output.ReadLeaseID{lease.ReadLeaseID},
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_read_leases SET released_at = coalesce(released_at, now())
		WHERE read_lease_id = $1`, string(lease.ReadLeaseID)); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// AdmitReclaim marks an exact generation `reclaiming` durably before any
// external delete.
//
// Every exclusion is rechecked here under the exact-lifecycle lock, and the
// schema rechecks them again at commit: elapsed publication grace, no active
// claim, no active read lease, no unresolved reservation for the same content,
// and no unresolved runtime integrity findings. A reclaimer that arrives second
// recognises the claimant and skips.
//
// Grace is a PARAMETER and the instant it is measured against is not. The
// caller brings the deployment's configured publication grace, which is
// configuration; the row brings registered_at, and the comparison is made on
// the database clock in the statement below. A precondition whose instant came
// from the process about to delete would be a precondition that process chose,
// which is the same rule the reclaim job's own generation precondition follows.
//
// registered_at rather than the object's creation time, which this table does
// not carry: for a `registered` row the object create precedes the receipt, and
// for an `adopted` row adoption itself already required grace to elapse since
// creation. Both are conservative -- the wait is never shorter than grace
// measured from the object.
func (repository *HangarOutputRepository) AdmitReclaim(ctx context.Context, tx output.Tx, ref hangar.TreeRef, owner string, metageneration int64, term, grace time.Duration) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if grace <= 0 {
		return fmt.Errorf("%w: reclaim admission names no publication grace, and elapsed grace "+
			"is one of the seven preconditions", output.ErrIncomplete)
	}
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	})
	if err != nil {
		return err
	}
	lifecycle, err := locks.LifecycleID(ref)
	if err != nil {
		return err
	}

	var claims, leases, pending int
	var epoch int64
	var state string
	var withinGrace bool
	if err := hangarQueryRow(ctx, tx, `
		SELECT l.state, l.activation_epoch,
		       l.registered_at > now() - $2::interval,
		       (SELECT count(*) FROM hangar_claims
		         WHERE lifecycle_id = l.id AND released_at IS NULL),
		       (SELECT count(*) FROM hangar_read_leases
		         WHERE lifecycle_id = l.id AND released_at IS NULL AND expires_at > now()),
		       (SELECT count(*) FROM hangar_logical_reservations
		         WHERE scope = l.scope AND digest = l.digest AND state = 'unresolved_generation') +
		       (SELECT count(*) FROM hangar_input_publications
		         WHERE scope = l.scope AND digest = l.digest AND lifecycle_id IS NULL AND expires_at > clock_timestamp())
		FROM hangar_exact_lifecycles l WHERE l.id = $1`,
		[]any{lifecycle, hangarInterval(grace)},
		&state, &epoch, &withinGrace, &claims, &leases, &pending); err != nil {
		return err
	}
	if state != "registered" && state != "adopted" {
		return fmt.Errorf("%w: %s/%s/%d is already %s", output.ErrConflict,
			ref.Scope, ref.Digest, ref.Generation, state)
	}
	if withinGrace {
		return fmt.Errorf("%w: %s/%s/%d is still inside its %s publication grace on the "+
			"database clock; reclamation requires elapsed grace, and a generation admissible "+
			"the instant its receipt landed would be one this plane deleted while the capture "+
			"that made it could still legitimately be retrying",
			output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, grace)
	}
	if claims > 0 || leases > 0 || pending > 0 {
		return fmt.Errorf("%w: %s/%s/%d is protected by %d claim(s), %d read lease(s) and %d "+
			"unresolved reservation(s); reclaim admission is refused while any of them exists",
			output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, claims, leases, pending)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_reclaim_jobs
			(lifecycle_id, activation_epoch, owner_id, lease_fence, generation, metageneration, expires_at)
		VALUES ($1, $2, $3, 1, $4, $5, now() + $6::interval)`,
		lifecycle, epoch, owner, ref.Generation, metageneration, interval); err != nil {
		return hangarConflict(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_exact_lifecycles SET state = 'reclaiming', updated_at = now() WHERE id = $1`,
		lifecycle); err != nil {
		return hangarConflict(err)
	}

	// The acceleration, and it is INSIDE the transaction on purpose.
	//
	// PostgreSQL delivers a NOTIFY issued in a transaction only when that
	// transaction commits, so "after the transaction that created the work has
	// committed" is what this already means -- a listener woken by an admission
	// that rolled back would read a job that does not exist. Doing it out of
	// band after the commit would be the same claim with a window in it.
	//
	// It is an acceleration and never the way work is found: the delete
	// controller has a nonzero periodic wake, so a lost notification costs a
	// minute rather than a job.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_notify($1, '')`, output.NotifyChannel(output.OperationReclaimDelete),
	); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// hangarStatProofFresh asks the DATABASE whether an observation is still fresh.
//
// A node's own clock is never the authority here, for the same reason it is
// never the authority for a lease: a drifting node could otherwise present an
// hour-old stat as fresh, and freshness is the whole of what a stat proof is.
// The comparison is a predicate rather than an age in seconds so that the
// answer and the bound meet in one place instead of in Go arithmetic over a
// number the database already knew how to compare.
//
// Two details that are not incidental. It reads clock_timestamp() and not
// now(): now() is the TRANSACTION's start, and a stat taken legitimately after
// the transaction opened would be "in the future" by it. And the window is
// symmetric -- the observation must be within the same bound on either side --
// because the ATC and the database are different hosts and a few milliseconds
// of NTP skew must not be a refusal, while an observation dated minutes ahead
// is the same broken clock a stale one is.
func hangarStatProofFresh(ctx context.Context, tx output.Tx, observed output.Timestamp) error {
	var future, stale bool
	if err := hangarQueryRow(ctx, tx, `
		SELECT $1::timestamptz > clock_timestamp() + $2::interval,
		       $1::timestamptz < clock_timestamp() - $2::interval`,
		[]any{observed.Time, hangarInterval(output.MaxStatProofAge)}, &future, &stale); err != nil {
		return err
	}
	if future {
		return fmt.Errorf("%w: the admitting stat is dated more than %s ahead of the database "+
			"clock", output.ErrIncomplete, output.MaxStatProofAge)
	}
	if stale {
		return fmt.Errorf("%w: the admitting stat is older than %s on the database clock; an "+
			"observation cannot stand in for the object store indefinitely", output.ErrTimeout,
			output.MaxStatProofAge)
	}

	return nil
}

// LoadReadLease reads a committed lease back by identity.
//
// It is what the minter calls AFTER the transaction commits, and what recovery
// calls after an ambiguous one. Loading rather than remembering is the point:
// an ambiguous commit is answered by asking the database what it has, and a
// mint over the caller's idea of the lease would be a warrant for a row that may
// never have existed.
func (repository *HangarOutputRepository) LoadReadLease(ctx context.Context, tx output.Tx, id output.ReadLeaseID) (output.ReadLeaseRecord, error) {
	if err := id.Validate(); err != nil {
		return output.ReadLeaseRecord{}, err
	}

	var (
		claim, scope, digest, nonce, handle, volume string
		generation, fence, epoch                    int64
		granted, expires                            time.Time
		released                                    sql.NullTime
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT r.claim_id, l.scope, l.digest, l.generation, r.lease_fence, r.activation_epoch,
		       r.granted_at, r.expires_at, r.released_at,
		       r.grant_nonce, r.destination_handle, r.destination_volume
		FROM hangar_read_leases r
		JOIN hangar_exact_lifecycles l ON l.id = r.lifecycle_id
		WHERE r.read_lease_id = $1`,
		[]any{string(id)},
		&claim, &scope, &digest, &generation, &fence, &epoch,
		&granted, &expires, &released, &nonce, &handle, &volume); err != nil {
		return output.ReadLeaseRecord{}, err
	}
	if released.Valid {
		return output.ReadLeaseRecord{}, fmt.Errorf("%w: read lease %s was released at %s",
			output.ErrConflict, id, released.Time)
	}

	return output.ReadLeaseRecord{
		Lease: output.ReadLease{
			ProtocolVersion: output.ProtocolVersion,
			ReadLeaseID:     id,
			ClaimID:         output.ClaimID(claim),
			Ref: hangar.TreeRef{
				Scope: hangar.Scope(scope), Digest: hangar.Digest(digest), Generation: generation,
			},
			ActivationEpoch: hangarEpoch(epoch),
			LeaseFence:      output.LeaseFence(fence),
			GrantedAt:       output.NewTimestamp(granted),
			ExpiresAt:       output.NewTimestamp(expires),
		},
		Destination:  output.ReadDestination{Handle: handle, Volume: volume},
		WarrantNonce: nonce,
	}, nil
}

// ValidateReadLease answers the materializing daemon's independent question.
//
// Every field the warrant carried is compared against the committed row, and the
// row's own state -- released, expired, superseded by a later fence, or beside
// a lifecycle that reclamation has admitted -- is what decides. A valid HMAC
// bound to any of those authorizes nothing, and this is the method that says so.
//
// The remaining term is measured in SQL. Requirement 36 lets work begin only
// with the operation's timeout plus two minutes left, and a daemon that
// measured that against its own clock would be deciding, on a node, a question
// the database owns.
func (repository *HangarOutputRepository) ValidateReadLease(ctx context.Context, tx output.Tx, validation output.ReadLeaseValidation) (output.ReadLeaseRecord, error) {
	if err := validation.Validate(); err != nil {
		return output.ReadLeaseRecord{}, err
	}

	record, err := repository.LoadReadLease(ctx, tx, validation.ReadLeaseID)
	if err != nil {
		return output.ReadLeaseRecord{}, err
	}

	// THERE IS NO FENCE CHECK, and its absence is a fact about the plane rather
	// than an omission.
	//
	// hangar_read_leases.lease_fence has no writer: AcquireReadLease inserts 1
	// and nothing anywhere moves it. A retry of an ambiguous commit deliberately
	// does not advance it -- requirement 37 wants a byte-identical re-mint, and
	// a moving fence would make that impossible -- and Phase 7's takeover works
	// on the CAPTURE fence, a different column on a different table. So a
	// comparison here could only ever be a value checked against itself, which
	// is the kind of check that passes for a reason nobody can state.
	//
	// The column stays, with a note at the migration, because a column with no
	// reader is cheaper than renumbering a migration; the warrant no longer binds
	// one. What supersession this lease HAS is the released tombstone, which
	// LoadReadLease above has already refused.
	if record.Lease.ClaimID != validation.ClaimID ||
		record.Lease.Ref != validation.Ref ||
		record.Lease.ActivationEpoch != validation.ActivationEpoch ||
		record.Destination != validation.Destination ||
		subtle.ConstantTimeCompare([]byte(record.WarrantNonce), []byte(validation.WarrantNonce)) != 1 {
		return output.ReadLeaseRecord{}, fmt.Errorf("%w: the warrant presented for read lease %s "+
			"does not describe the lease this transaction committed", output.ErrUnauthorized,
			validation.ReadLeaseID)
	}

	// The claim is deliberately NOT rechecked here.
	//
	// Requirement 36 and AC 13 are explicit: reclaim admission is refused while
	// any read lease is active EVEN IF the domain releases its last claim, and
	// releasing the last claim during a transfer must not delete the bytes out
	// from under a reader. A claim is what admits a warrant; the lease is what
	// protects the read once it has one. A daemon that refused to stage because
	// the consumer had already unbound would be enforcing the opposite rule.
	// There is no reclaim check here, and its absence is the schema's doing
	// rather than an omission. hangar_reclaim_exclusion refuses an admitted
	// reclaim beside an active read lease and refuses an active read lease
	// beside an admitted reclaim, so the two states cannot coexist: a lease a
	// reclaimer got past was released first, and LoadReadLease above has
	// already answered that. A count here would be code no state can reach.
	var expired, tooShort bool
	if err := hangarQueryRow(ctx, tx, `
		SELECT r.expires_at <= now(), r.expires_at < now() + $2::interval
		FROM hangar_read_leases r WHERE r.read_lease_id = $1`,
		[]any{string(validation.ReadLeaseID), hangarInterval(validation.RequiredRemaining)},
		&expired, &tooShort); err != nil {
		return output.ReadLeaseRecord{}, err
	}
	if expired {
		return output.ReadLeaseRecord{}, fmt.Errorf("%w: read lease %s has expired on the "+
			"database clock", output.ErrTimeout, validation.ReadLeaseID)
	}
	if tooShort {
		return output.ReadLeaseRecord{}, fmt.Errorf("%w: read lease %s has less than %s left and "+
			"that is what the work needs; work begins only with the operation's timeout plus %s "+
			"remaining", output.ErrTimeout, validation.ReadLeaseID, validation.RequiredRemaining,
			output.LeaseStartMargin)
	}
	return record, nil
}

// ReadClaims reports every claim recorded for one tree ref, active and
// tombstoned, in acquisition order.
//
// It takes no lock. It is a read for a caller that wants to know what is there,
// not a step in a transaction that is about to decide something -- and a read
// that took the exact-lifecycle lock would make asking the question serialize
// against every claimant and reclaimer of that generation.
func (repository *HangarOutputRepository) ReadClaims(ctx context.Context, tx output.Tx, ref hangar.TreeRef) ([]output.ClaimRecord, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT c.claim_id, c.consumer_binding_id, c.acquired_at, c.released_at
		FROM hangar_claims c
		JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
		WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3
		ORDER BY c.acquired_at, c.claim_id`,
		string(ref.Scope), string(ref.Digest), ref.Generation)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var claims []output.ClaimRecord
	for rows.Next() {
		var (
			id, binding string
			acquired    time.Time
			released    sql.NullTime
		)
		if err := rows.Scan(&id, &binding, &acquired, &released); err != nil {
			return nil, err
		}
		record := output.ClaimRecord{
			ClaimID:           output.ClaimID(id),
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID(binding),
			AcquiredAt:        output.NewTimestamp(acquired),
		}
		if released.Valid {
			at := output.NewTimestamp(released.Time)
			record.ReleasedAt = &at
		}
		claims = append(claims, record)
	}

	return claims, rows.Err()
}

// CloseAbandonedReadLeases releases read leases whose term has run out on the
// database clock.
//
// It is recovery, and it is the reason an abandoned reader does not pin a
// generation forever: a materializer that died mid-staging leaves an unreleased
// lease, and requirement 46's "no active read lease" plus AC 13's "closes or
// safely EXPIRES" both mean the same thing about it. Nothing here guesses -- the
// only leases it touches are ones the database itself says have expired, and it
// closes them by writing the release the daemon never got to write, so the
// tombstone that prevents resurrection exists either way.
//
// It is bounded, like every other worker pass in this plane, and it reports how
// many it closed so a caller can tell "nothing was owed" from "the batch was
// full".
func (repository *HangarOutputRepository) CloseAbandonedReadLeases(ctx context.Context, tx output.Tx, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: a recovery pass is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}

	// The candidates first, unlocked, because a lock set has to be derived from
	// identities and there is no identity until something has been selected.
	rows, err := tx.QueryContext(ctx, `
		SELECT read_lease_id FROM hangar_read_leases
		WHERE released_at IS NULL AND expires_at <= now()
		ORDER BY expires_at
		LIMIT $1`, limit)
	if err != nil {
		return 0, hangarConflict(err)
	}
	var candidates []output.ReadLeaseID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()

			return 0, err
		}
		candidates = append(candidates, output.ReadLeaseID(id))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return 0, hangarConflict(err)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	// Then the suffix, over exactly those identities, in the one order this
	// system has -- the helper sorts them, so two passes given overlapping
	// batches take them the same way round.
	//
	// Class 4 ALONE, deliberately, and unlike RenewReadLease beside it. A
	// renewal extends protection, so a reclaim admission that ran beside it
	// could be admitted over a generation a reader still holds; that is why the
	// renewal now takes classes 1 and 2 as well, and meets AdmitReclaim on the
	// exact-lifecycle row. Recovery only ever REMOVES protection. A reclaim
	// admission racing it either sees the lease still live and refuses, or sees
	// it closed and proceeds, and both are correct: the pass closes leases the
	// database itself says have run out. Locking the generation here would be a
	// batch pass taking class 2 over every correlation it swept, for nothing.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		ReadLeases: candidates,
	}); err != nil {
		return 0, err
	}

	// And the write, with the predicate REPEATED under the lock. The candidate
	// read ran before the rows were held, so a lease that was renewed in
	// between must not be closed by a decision taken from the stale answer --
	// which is also the only reason the count this returns is the UPDATE's
	// rather than the SELECT's.
	placeholders := make([]string, 0, len(candidates))
	closing := make([]any, 0, len(candidates))
	for index, id := range candidates {
		placeholders = append(placeholders, fmt.Sprintf("$%d", index+1))
		closing = append(closing, string(id))
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE hangar_read_leases SET released_at = now()
		WHERE read_lease_id IN (%s)
		  AND released_at IS NULL AND expires_at <= now()`,
		strings.Join(placeholders, ", ")), closing...)
	if err != nil {
		return 0, hangarConflict(err)
	}
	closed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(closed), nil
}
