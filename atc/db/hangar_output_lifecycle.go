package db

import (
	"context"
	"database/sql"
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

// registerLifecycle records that one exact generation is managed by this
// plane, or finds the row that already says so.
//
// A reclaimed generation never resurrects: the insert does nothing on conflict,
// and a row already stamped reclaimed is refused rather than reused. The
// caller holds the tree lock (LockHangarSuffix's logical class), so a reclaim
// pass deciding about this generation waits for the registration or precedes
// it, and never lands between the insert and the check.
func (repository *HangarOutputRepository) registerLifecycle(ctx context.Context, tx output.Tx, ref hangar.TreeRef, epoch int64) (int64, error) {
	if epoch <= 0 {
		return 0, fmt.Errorf("%w: %s/%s/%d is registered under no control-key generation",
			output.ErrIncomplete, ref.Scope, ref.Digest, ref.Generation)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_exact_lifecycles (scope, digest, generation, activation_epoch)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scope, digest, generation) DO NOTHING`,
		string(ref.Scope), string(ref.Digest), ref.Generation, epoch); err != nil {
		return 0, hangarConflict(err)
	}

	var id int64
	var reclaimed sql.NullTime
	if err := hangarQueryRow(ctx, tx, `
		SELECT id, reclaimed_at FROM hangar_exact_lifecycles
		WHERE scope = $1 AND digest = $2 AND generation = $3`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation}, &id, &reclaimed); err != nil {
		return 0, err
	}
	if reclaimed.Valid {
		return 0, fmt.Errorf("%w: %s/%s/%d was reclaimed at %s; a reclaimed generation never "+
			"resurrects", output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, reclaimed.Time)
	}

	return id, nil
}

// AcquireClaim composes Hangar protection with a consumer's own write, and
// returns the claim as the row records it.
//
// Idempotent for the same id and tree ref; the same id on another ref is a
// conflict, because a claim protects one immutable generation and cannot float
// to replacement content. The exact-lifecycle lock is what makes this and the
// reclaim pass one winner: claimant first and the pass rechecks and skips,
// pass first and this transaction refuses a reclaimed generation.
//
// A reader's claim carries a term and expires on the database clock; a
// consumer's does not. The record returned is what a read warrant is minted
// from, so an acquisition whose commit answer was lost is resolved by asking
// again with the same identity: the repeat returns the committed row.
//
// THE COMMIT IS THE CONSUMER'S, AND SO IS THE REFUSAL. This runs inside the
// consumer's transaction, so the deferred
// hangar_policy_admits_new_protection fires at the CONSUMER's COMMIT and
// arrives there as a bare driver error carrying SQLSTATE JB002 -- nothing this
// method returns, and nothing a caller can branch on. A consumer composing this
// into its own transaction MUST pass its commit error through
// db.HangarCommitError (or commit through db.HangarOutputTx, which is that
// function with a Commit around it). A consumer that does not will read a
// integrity denial as an ambiguous commit and retry a refusal that never clears.
// ReleaseClaim says the same, for the same reason.
func (repository *HangarOutputRepository) AcquireClaim(ctx context.Context, tx output.Tx, acquisition output.ClaimAcquisition) (output.ClaimRecord, error) {
	if err := acquisition.Validate(); err != nil {
		return output.ClaimRecord{}, err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: acquisition.Ref.Scope, Digest: acquisition.Ref.Digest}},
		Exact:   []hangar.TreeRef{acquisition.Ref},
		Claims:  []output.ClaimID{acquisition.ClaimID},
	})
	if err != nil {
		return output.ClaimRecord{}, err
	}
	lifecycle, err := locks.LifecycleID(acquisition.Ref)
	if err != nil {
		return output.ClaimRecord{}, err
	}

	var reclaimed sql.NullTime
	var epoch int64
	if err := hangarQueryRow(ctx, tx, `
		SELECT reclaimed_at, activation_epoch FROM hangar_exact_lifecycles WHERE id = $1`,
		[]any{lifecycle}, &reclaimed, &epoch); err != nil {
		return output.ClaimRecord{}, err
	}
	if reclaimed.Valid {
		return output.ClaimRecord{}, fmt.Errorf("%w: %s/%s/%d was reclaimed at %s; a claim protects "+
			"a registered generation", output.ErrNotFound, acquisition.Ref.Scope,
			acquisition.Ref.Digest, acquisition.Ref.Generation, reclaimed.Time)
	}

	var expiry any
	if acquisition.Term > 0 {
		expiry = hangarInterval(acquisition.Term)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
		ON CONFLICT (claim_id) DO NOTHING`,
		string(acquisition.ClaimID), lifecycle, epoch, string(acquisition.ConsumerBindingID), expiry); err != nil {
		return output.ClaimRecord{}, hangarConflict(err)
	}

	var (
		existing, recordedEpoch int64
		binding                 string
		acquired                time.Time
		expires, released       sql.NullTime
	)
	if err := hangarQueryRow(ctx, tx, `
		SELECT lifecycle_id, activation_epoch, consumer_binding_id, acquired_at, expires_at, released_at
		FROM hangar_claims WHERE claim_id = $1`,
		[]any{string(acquisition.ClaimID)},
		&existing, &recordedEpoch, &binding, &acquired, &expires, &released); err != nil {
		return output.ClaimRecord{}, err
	}
	if existing != lifecycle {
		return output.ClaimRecord{}, fmt.Errorf("%w: claim %s already protects another tree ref; "+
			"reuse for another ref is a conflict", output.ErrConflict, acquisition.ClaimID)
	}
	if released.Valid {
		return output.ClaimRecord{}, fmt.Errorf("%w: claim %s was released at %s and stays "+
			"tombstoned for the lifetime of the tree-ref lifecycle record", output.ErrConflict,
			acquisition.ClaimID, released.Time)
	}

	record := output.ClaimRecord{
		ClaimID:           acquisition.ClaimID,
		Ref:               acquisition.Ref,
		ConsumerBindingID: output.OpaqueID(binding),
		ActivationEpoch:   hangarEpoch(recordedEpoch),
		AcquiredAt:        output.NewTimestamp(acquired.UTC()),
	}
	if expires.Valid {
		at := output.NewTimestamp(expires.Time.UTC())
		record.ExpiresAt = &at
	}

	return record, nil
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

// ReadClaims reports every claim recorded for one tree ref -- consumers' and
// readers', active and tombstoned -- in acquisition order.
//
// It takes no lock. It is a read for a caller that wants to know what is there,
// not a step in a transaction that is about to decide something -- and a read
// that took the exact-lifecycle lock would make asking the question serialize
// against every claimant and the reclaim pass for that generation.
func (repository *HangarOutputRepository) ReadClaims(ctx context.Context, tx output.Tx, ref hangar.TreeRef) ([]output.ClaimRecord, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT c.claim_id, c.consumer_binding_id, c.activation_epoch, c.acquired_at, c.expires_at, c.released_at
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
			id, binding       string
			epoch             int64
			acquired          time.Time
			expires, released sql.NullTime
		)
		if err := rows.Scan(&id, &binding, &epoch, &acquired, &expires, &released); err != nil {
			return nil, err
		}
		record := output.ClaimRecord{
			ClaimID:           output.ClaimID(id),
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID(binding),
			ActivationEpoch:   hangarEpoch(epoch),
			AcquiredAt:        output.NewTimestamp(acquired),
		}
		if expires.Valid {
			at := output.NewTimestamp(expires.Time)
			record.ExpiresAt = &at
		}
		if released.Valid {
			at := output.NewTimestamp(released.Time)
			record.ReleasedAt = &at
		}
		claims = append(claims, record)
	}

	return claims, rows.Err()
}
