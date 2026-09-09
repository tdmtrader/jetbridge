package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

	result, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_logical_reservations
			(reservation_id, scope, digest, logical_bytes, capture_fence)
		VALUES ($1, $2, $3, $4, $5)
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
	if inserted, err := result.RowsAffected(); err == nil && inserted == 1 {
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
func (repository *HangarOutputRepository) RecordFirstObjectCreate(ctx context.Context, tx output.Tx, reservation output.ReservationID) error {
	if err := reservation.Validate(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations
		SET first_create_attempted_at = coalesce(first_create_attempted_at, now()),
		    past_irreversible_publish_point = true
		WHERE reservation_id = $1`, string(reservation)); err != nil {
		return hangarConflict(err)
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
	// same exact ref is one admission repeated -- an ambiguous create response
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
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_capture_reservations
		SET state = 'registered', settled_at = coalesce(settled_at, now())
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
// registration one winner rather than two records of one generation.
func (repository *HangarOutputRepository) AdoptManagedOrphan(ctx context.Context, tx output.Tx, ref hangar.TreeRef, metageneration int64, epoch executioncontrol.ActivationEpoch) error {
	if err := ref.Validate(); err != nil {
		return err
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	}); err != nil {
		return err
	}

	var unresolved int
	if err := hangarQueryRow(ctx, tx, `
		SELECT count(*) FROM hangar_logical_reservations
		WHERE scope = $1 AND digest = $2 AND state = 'unresolved_generation'`,
		[]any{string(ref.Scope), string(ref.Digest)}, &unresolved); err != nil {
		return err
	}
	if unresolved > 0 {
		return fmt.Errorf("%w: %d unresolved reservation(s) still correlate %s/%s; an unresolved "+
			"reservation protects its correlation from adoption even before a generation is known",
			output.ErrConflict, unresolved, ref.Scope, ref.Digest)
	}

	_, err := repository.upsertLifecycle(ctx, tx, ref, metageneration, int64(epoch), "adopted")

	return err
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
// Idempotent for the same id and exact ref; the same id on another ref is a
// conflict, because a claim protects one immutable generation and cannot float
// to replacement content. The exact-lifecycle lock is what makes this and
// reclaim admission one winner: claimant first and the reclaimer rechecks and
// skips, reclaimer first and this transaction rolls back with no usable
// binding.
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
		return fmt.Errorf("%w: claim %s already protects another exact ref; reuse for another ref "+
			"is a conflict", output.ErrConflict, acquisition.ClaimID)
	}
	if released.Valid {
		return fmt.Errorf("%w: claim %s was released at %s and stays tombstoned for the lifetime "+
			"of the exact-ref lifecycle record", output.ErrConflict, acquisition.ClaimID, released.Time)
	}

	return nil
}

// ReleaseClaim gives up protection beside the consumer making its own binding
// unusable, in one transaction.
//
// Releasing an active or already-released claim is idempotent. The identity is
// never reused: the row is the tombstone, and the schema refuses both its
// deletion and its reactivation.
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
// It signs nothing and calls nobody. Minting the grant that carries this lease
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

	var released sql.NullTime
	if err := hangarQueryRow(ctx, tx, `
		SELECT released_at FROM hangar_claims WHERE claim_id = $1 AND lifecycle_id = $2`,
		[]any{string(request.ClaimID), lifecycle}, &released); err != nil {
		return output.ReadLease{}, fmt.Errorf("%w: no claim %s protects %s/%s/%d; a managed-output "+
			"grant needs at least one active claim", output.ErrNotFound, request.ClaimID,
			request.Ref.Scope, request.Ref.Digest, request.Ref.Generation)
	}
	if released.Valid {
		return output.ReadLease{}, fmt.Errorf("%w: claim %s was already released",
			output.ErrConflict, request.ClaimID)
	}

	interval, err := hangarLeaseInterval(output.LeaseTermFor(request.MaterializationTimeout))
	if err != nil {
		return output.ReadLease{}, err
	}

	var granted, expires time.Time
	var fence int64
	if err := hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_read_leases
			(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
		VALUES ($1, $2, $3, $4, 1, now() + $5::interval)
		ON CONFLICT (read_lease_id) DO UPDATE
		SET renewed_at = now(),
		    expires_at = now() + $5::interval,
		    lease_fence = hangar_read_leases.lease_fence + 1
		RETURNING granted_at, expires_at, lease_fence`,
		[]any{
			string(request.ReadLeaseID), string(request.ClaimID), lifecycle,
			int64(request.ActivationEpoch), interval,
		}, &granted, &expires, &fence); err != nil {
		return output.ReadLease{}, err
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
func (repository *HangarOutputRepository) RenewReadLease(ctx context.Context, tx output.Tx, lease output.ReadLease) (output.ReadLease, error) {
	if err := lease.Validate(); err != nil {
		return output.ReadLease{}, err
	}

	interval, err := hangarLeaseInterval(lease.ExpiresAt.Sub(lease.GrantedAt.Time))
	if err != nil {
		return output.ReadLease{}, err
	}

	var expires time.Time
	var fence int64
	if err := hangarQueryRow(ctx, tx, `
		UPDATE hangar_read_leases
		SET renewed_at = now(), expires_at = now() + $2::interval
		WHERE read_lease_id = $1 AND released_at IS NULL AND lease_fence = $3
		RETURNING expires_at, lease_fence`,
		[]any{string(lease.ReadLeaseID), interval, int64(lease.LeaseFence)},
		&expires, &fence); err != nil {
		return output.ReadLease{}, fmt.Errorf("%w: read lease %s is released, expired or held at "+
			"another fence", output.ErrConflict, lease.ReadLeaseID)
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
// schema rechecks them again at commit: no active claim, no active read lease,
// no unresolved reservation for the same content, and a currently provable safe
// policy. A reclaimer that arrives second recognises the claimant and skips.
func (repository *HangarOutputRepository) AdmitReclaim(ctx context.Context, tx output.Tx, ref hangar.TreeRef, owner string, metageneration int64, term time.Duration) error {
	if err := ref.Validate(); err != nil {
		return err
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
	if err := hangarQueryRow(ctx, tx, `
		SELECT l.state, l.activation_epoch,
		       (SELECT count(*) FROM hangar_claims
		         WHERE lifecycle_id = l.id AND released_at IS NULL),
		       (SELECT count(*) FROM hangar_read_leases
		         WHERE lifecycle_id = l.id AND released_at IS NULL),
		       (SELECT count(*) FROM hangar_logical_reservations
		         WHERE scope = l.scope AND digest = l.digest AND state = 'unresolved_generation')
		FROM hangar_exact_lifecycles l WHERE l.id = $1`,
		[]any{lifecycle}, &state, &epoch, &claims, &leases, &pending); err != nil {
		return err
	}
	if state != "registered" && state != "adopted" {
		return fmt.Errorf("%w: %s/%s/%d is already %s", output.ErrConflict,
			ref.Scope, ref.Digest, ref.Generation, state)
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

	return nil
}

// RecordPolicySnapshot stores one lifetime-policy attestation.
//
// It records what was observed and when. Whether that evidence is still fresh
// enough is a question every later reader answers for itself, which is why the
// snapshot is a row rather than a flag somebody flipped.
func (repository *HangarOutputRepository) RecordPolicySnapshot(ctx context.Context, tx output.Tx, snapshot output.PolicySnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_policy_snapshots
			(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
			 lifecycle_delete_rules, state, observed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		int64(snapshot.ActivationEpoch), snapshot.BucketFingerprint, snapshot.Metageneration,
		snapshot.PolicyHash, snapshot.LifecycleDeleteRules, string(snapshot.State),
		snapshot.ObservedAt.Time); err != nil {
		return hangarConflict(err)
	}

	return nil
}
