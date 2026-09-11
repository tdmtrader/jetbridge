package db

// The controller half of the output plane: one durable lease per operation
// kind, and the inventory sweep's one durable cursor and its debt.
//
// Everything here is measured on the DATABASE clock. A controller is a process
// that can be paused, restarted, partitioned or replaced, and a lease measured
// on such a process's own clock is a lease that expires when the process
// decides it has. `now()` in these statements is PostgreSQL's, and the fence is
// what a write presents to say which owner it is from: an expiry alone would
// let a paused owner wake after a takeover and finish work somebody else now
// owns.
//
// None of these methods takes a row lock, and that is deliberate rather than an
// omission. "Lock order is an API" governs the rows a CONSUMER's transaction
// composes with -- logical, exact, capture and their subordinates -- and a
// controller's own lease and cursor are neither: they are single rows written
// by their one owner, fenced optimistically, and a FOR UPDATE on them would be
// a second lock order for no exclusion the fence does not already give.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

// ClaimOperationLease takes or renews one kind's durable ownership.
//
// Idempotent for the same owner: a controller that lost its answer and asks
// again holds the same lease at the same fence, rather than fencing itself out.
// A different owner may take over only once the current lease has expired ON
// THE DATABASE CLOCK, and a takeover advances the fence, which is what makes
// the previous owner's later writes refuse.
func (repository *HangarOutputRepository) ClaimOperationLease(ctx context.Context, tx output.Tx, kind output.OperationKind, epoch int64, owner string, term time.Duration) (output.OperationLease, error) {
	if err := kind.Validate(); err != nil {
		return output.OperationLease{}, err
	}
	probe := output.OperationLease{
		Kind: kind, ActivationEpoch: hangarEpoch(epoch), OwnerID: owner, LeaseFence: 1,
	}
	if err := probe.Validate(); err != nil {
		return output.OperationLease{}, err
	}
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return output.OperationLease{}, err
	}

	lease := output.OperationLease{
		Kind: kind, ActivationEpoch: hangarEpoch(epoch), OwnerID: owner,
	}
	var renewedAt, expiresAt time.Time
	err = hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_operation_leases
			(kind, activation_epoch, owner_id, lease_fence, expires_at)
		VALUES ($1, $2, $3, 1, now() + $4::interval)
		ON CONFLICT (kind, activation_epoch) DO UPDATE
			SET owner_id    = excluded.owner_id,
			    lease_fence = hangar_operation_leases.lease_fence
			                  + CASE WHEN hangar_operation_leases.owner_id = excluded.owner_id
			                         THEN 0 ELSE 1 END,
			    renewed_at  = now(),
			    expires_at  = now() + $4::interval
			WHERE hangar_operation_leases.owner_id = excluded.owner_id
			   OR hangar_operation_leases.expires_at <= now()
		RETURNING lease_fence, renewed_at, expires_at`,
		[]any{string(kind), epoch, owner, interval},
		&lease.LeaseFence, &renewedAt, &expiresAt)
	if errors.Is(err, output.ErrNotFound) {
		return output.OperationLease{}, fmt.Errorf("%w: the %s lease for epoch %d is held by "+
			"another owner and has not expired on the database clock; one kind has one owner, "+
			"and a second would be a second cursor over the same work",
			output.ErrConflict, kind, epoch)
	}
	if err != nil {
		return output.OperationLease{}, err
	}
	lease.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	lease.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return lease, lease.Validate()
}

// RenewOperationLease extends a lease the caller still holds.
//
// It names the fence as well as the owner, so an owner that was fenced out and
// has not noticed cannot renew its way back in. A renewal of an already-expired
// lease is refused rather than resurrected: between expiry and this statement
// another owner may have taken over, and the only honest answer is to go back
// through a claim.
func (repository *HangarOutputRepository) RenewOperationLease(ctx context.Context, tx output.Tx, lease output.OperationLease, term time.Duration) (output.OperationLease, error) {
	if err := lease.Validate(); err != nil {
		return output.OperationLease{}, err
	}
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return output.OperationLease{}, err
	}

	renewed := lease
	var renewedAt, expiresAt time.Time
	err = hangarQueryRow(ctx, tx, `
		UPDATE hangar_operation_leases
		   SET renewed_at = now(), expires_at = now() + $5::interval
		 WHERE kind = $1 AND activation_epoch = $2 AND owner_id = $3 AND lease_fence = $4
		   AND expires_at > now()
		RETURNING renewed_at, expires_at`,
		[]any{string(lease.Kind), int64(lease.ActivationEpoch), lease.OwnerID,
			int64(lease.LeaseFence), interval},
		&renewedAt, &expiresAt)
	if errors.Is(err, output.ErrNotFound) {
		return output.OperationLease{}, fmt.Errorf("%w: the %s lease for epoch %d is no longer "+
			"owner %s at fence %d, or it has expired; an expired owner cannot renew, delete or "+
			"finalize", output.ErrConflict, lease.Kind, lease.ActivationEpoch, lease.OwnerID, lease.LeaseFence)
	}
	if err != nil {
		return output.OperationLease{}, err
	}
	renewed.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	renewed.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return renewed, nil
}

// Deferred: the operator status and diagnosis surface is Phase 8's; no running
// process reads it yet
//
// ReadOperationLease reads one kind's lease back, whether or not it is held.
//
// Diagnosis stays possible in every state this plane can be in, which is why
// this exists beside the claim: an operator asking "who owns the reclaimer"
// must not have to take the lease to find out.
func (repository *HangarOutputRepository) ReadOperationLease(ctx context.Context, tx output.Tx, kind output.OperationKind, epoch int64) (output.OperationLease, error) {
	lease := output.OperationLease{Kind: kind, ActivationEpoch: hangarEpoch(epoch)}
	var renewedAt, expiresAt time.Time
	err := hangarQueryRow(ctx, tx, `
		SELECT owner_id, lease_fence, renewed_at, expires_at
		  FROM hangar_operation_leases WHERE kind = $1 AND activation_epoch = $2`,
		[]any{string(kind), epoch}, &lease.OwnerID, &lease.LeaseFence, &renewedAt, &expiresAt)
	if errors.Is(err, output.ErrNotFound) {
		return output.OperationLease{}, fmt.Errorf("%w: no %s lease exists for epoch %d",
			output.ErrNotFound, kind, epoch)
	}
	if err != nil {
		return output.OperationLease{}, err
	}
	lease.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	lease.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return lease, nil
}

// LoadInventoryCursor reads, and if necessary creates, the one cursor for this
// bucket and epoch, under the caller's fence.
//
// There is exactly one cursor per bucket and activation epoch (Req 42), so this
// is an upsert rather than a create: two controllers racing the first pass must
// end up on one row, not two partitions of one bucket. The caller's fence is
// its operation lease's, and a fence BELOW the row's is a stale owner --
// refused here rather than discovered when its advance writes nothing, because
// a stale owner that could reserve a page would read objects another owner is
// already dispositioning.
func (repository *HangarOutputRepository) LoadInventoryCursor(ctx context.Context, tx output.Tx, bucket string, epoch int64, fence int64) (output.InventoryCursor, error) {
	if bucket == "" {
		return output.InventoryCursor{}, fmt.Errorf("%w: the inventory cursor names its bucket",
			output.ErrIncomplete)
	}
	if fence <= 0 {
		return output.InventoryCursor{}, fmt.Errorf("%w: a cursor fence of %d; only the current "+
			"fencing epoch may reserve a page or advance the cursor", output.ErrIncomplete, fence)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_inventory_cursors (bucket_fingerprint, activation_epoch, cursor_fence)
		VALUES ($1, $2, $3)
		ON CONFLICT (bucket_fingerprint, activation_epoch) DO UPDATE
			SET cursor_fence = $3, updated_at = now()
			WHERE hangar_inventory_cursors.cursor_fence <= $3`,
		bucket, epoch, fence); err != nil {
		return output.InventoryCursor{}, hangarConflict(err)
	}

	cursor := output.InventoryCursor{ProtocolVersion: output.ProtocolVersion}
	var storedFence int64
	var updatedAt time.Time
	if err := hangarQueryRow(ctx, tx, `
		SELECT cursor_fence, after_key, after_generation, cycle, updated_at
		  FROM hangar_inventory_cursors
		 WHERE bucket_fingerprint = $1 AND activation_epoch = $2`,
		[]any{bucket, epoch},
		&storedFence, &cursor.AfterKey, &cursor.AfterGeneration, &cursor.Cycle, &updatedAt,
	); err != nil {
		return output.InventoryCursor{}, err
	}
	if storedFence > fence {
		return output.InventoryCursor{}, fmt.Errorf("%w: the inventory cursor for %s at epoch %d "+
			"is at fence %d and this owner holds %d; a stale owner may not reserve a page",
			output.ErrConflict, bucket, epoch, storedFence, fence)
	}

	cursor.ActivationEpoch = hangarEpoch(epoch)
	cursor.CursorFence = output.CursorFence(storedFence)
	cursor.UpdatedAt = output.NewTimestamp(updatedAt.UTC())

	return cursor, cursor.Validate()
}

// AdvanceInventoryCursor commits one page's dispositions and moves the cursor.
//
// Both halves, one statement pair, one transaction: Req 44 says per-object debt
// is committed BEFORE the cursor advances, and the only arrangement in which
// that is true whatever the process does is the arrangement where a crash
// between them cannot happen. A caller that wrote debt and then crashed would
// replay the page; a caller that advanced and then crashed would lose the debt
// and with it the record that an object was never dispositioned at all.
//
// The advance is fenced, not locked. A stale owner's UPDATE matches no row and
// this returns a typed conflict, which is the same outcome a lock would have
// produced one statement later and without a second lock order.
func (repository *HangarOutputRepository) AdvanceInventoryCursor(ctx context.Context, tx output.Tx, bucket string, next output.InventoryCursor, debt []output.InventoryDebt) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if bucket == "" {
		return fmt.Errorf("%w: the inventory cursor names its bucket", output.ErrIncomplete)
	}

	for _, owed := range debt {
		if err := repository.RecordInventoryDebt(ctx, tx, bucket, owed); err != nil {
			return err
		}
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_inventory_cursors
		   SET after_key = $4, after_generation = $5, cycle = $6, updated_at = now()
		 WHERE bucket_fingerprint = $1 AND activation_epoch = $2 AND cursor_fence = $3`,
		bucket, int64(next.ActivationEpoch), int64(next.CursorFence),
		next.AfterKey, next.AfterGeneration, int64(next.Cycle))
	if err != nil {
		return hangarConflict(err)
	}
	advanced, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if advanced == 0 {
		return fmt.Errorf("%w: the inventory cursor for %s at epoch %d is no longer at fence %d; "+
			"only the current fencing epoch may advance it, and a page reserved under an "+
			"overtaken fence is replayed by its new owner rather than committed by its old one",
			output.ErrConflict, bucket, next.ActivationEpoch, next.CursorFence)
	}

	return nil
}

// RecordInventoryDebt writes one per-object failure.
//
// Repeating an object's debt bumps its attempt count rather than adding a row.
// The count is the diagnosis an operator actually wants -- "this object has
// failed 400 times" is a different problem from "400 objects failed once" --
// and the alternative, a row per sighting, is an unbounded table fed by a
// single poisoned object.
func (repository *HangarOutputRepository) RecordInventoryDebt(ctx context.Context, tx output.Tx, bucket string, debt output.InventoryDebt) error {
	if err := debt.Validate(); err != nil {
		return err
	}
	if bucket == "" {
		return fmt.Errorf("%w: inventory debt names its bucket", output.ErrIncomplete)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_inventory_debt
			(bucket_fingerprint, activation_epoch, object_key, generation, reason, attempts, detail)
		VALUES ($1, $2, $3, $4, $5, 1, $6)
		ON CONFLICT (bucket_fingerprint, activation_epoch, object_key, generation, reason)
		DO UPDATE SET attempts = hangar_inventory_debt.attempts + 1,
		              observed_at = now(),
		              detail = excluded.detail`,
		bucket, int64(debt.ActivationEpoch), debt.ObjectKey, debt.Generation,
		string(debt.Reason), debt.Detail); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// Deferred: the operator status and diagnosis surface is Phase 8's; no running
// process reads it yet
//
// ReadInventoryDebt reads a bounded page of what the sweep still owes.
func (repository *HangarOutputRepository) ReadInventoryDebt(ctx context.Context, tx output.Tx, bucket string, epoch int64, limit int) ([]output.InventoryDebt, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a debt read is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT object_key, generation, reason, attempts, detail, observed_at
		  FROM hangar_inventory_debt
		 WHERE bucket_fingerprint = $1 AND activation_epoch = $2
		 ORDER BY observed_at, id
		 LIMIT $3`, bucket, epoch, limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var owed []output.InventoryDebt
	for rows.Next() {
		debt := output.InventoryDebt{
			ProtocolVersion: output.ProtocolVersion,
			ActivationEpoch: hangarEpoch(epoch),
		}
		var reason string
		var observedAt time.Time
		if err := rows.Scan(&debt.ObjectKey, &debt.Generation, &reason, &debt.Attempts,
			&debt.Detail, &observedAt); err != nil {
			return nil, err
		}
		parsed, err := output.ParseDebtReason(reason)
		if err != nil {
			return nil, err
		}
		debt.Reason = parsed
		debt.ObservedAt = output.NewTimestamp(observedAt.UTC())
		owed = append(owed, debt)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return owed, nil
}

// RecordPolicyAttestation stores one reading and everything it concluded, in
// one transaction.
//
// The two halves go together because a snapshot that said `at_risk` with no
// finding beside it is an operator being told something is wrong and nothing
// else. The snapshot is superseded by the next reading; the findings are not,
// because Req 52's recovery needs a fresh safe attestation AND violation
// reconciliation, and a finding stored on the snapshot would vanish with it.
//
// A repeated finding bumps nothing and adds nothing: the open row is keyed on
// (epoch, violation, subject), so a monitor running every fifteen minutes
// against an unfixed bucket leaves one row rather than ninety-six a day.
func (repository *HangarOutputRepository) RecordPolicyAttestation(ctx context.Context, tx output.Tx, snapshot output.PolicySnapshot, findings []output.PolicyFinding) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}

	var id int64
	if err := hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_policy_snapshots
			(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
			 lifecycle_delete_rules, state, observed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		[]any{
			int64(snapshot.ActivationEpoch), snapshot.BucketFingerprint, snapshot.Metageneration,
			snapshot.PolicyHash, snapshot.LifecycleDeleteRules, string(snapshot.State),
			snapshot.ObservedAt.Time,
		}, &id); err != nil {
		return hangarConflict(err)
	}

	for _, finding := range findings {
		if err := finding.Validate(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hangar_policy_violations
				(activation_epoch, snapshot_id, violation, subject, detail)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (activation_epoch, violation, subject) WHERE resolved_at IS NULL
			DO NOTHING`,
			int64(snapshot.ActivationEpoch), id, string(finding.Violation),
			finding.Subject, finding.Detail); err != nil {
			return hangarConflict(err)
		}
	}

	return nil
}

// Deferred: the reclaim-admission violation gate is wired later in this
// revision, under R1-F15
//
// OpenPolicyViolations reads what is still unreconciled for one epoch.
//
// A fresh safe attestation does not close these, and that is the whole reason
// this read exists: an operator who fixed the bucket and re-attested has a
// plane that admits work again and a list of what was wrong while it did not.
func (repository *HangarOutputRepository) OpenPolicyViolations(ctx context.Context, tx output.Tx, epoch int64) ([]output.PolicyFinding, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT violation, subject, detail FROM hangar_policy_violations
		 WHERE activation_epoch = $1 AND resolved_at IS NULL
		 ORDER BY observed_at, id`, epoch)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var findings []output.PolicyFinding
	for rows.Next() {
		var violation string
		var finding output.PolicyFinding
		if err := rows.Scan(&violation, &finding.Subject, &finding.Detail); err != nil {
			return nil, err
		}
		parsed, err := output.ParsePolicyViolation(violation)
		if err != nil {
			return nil, err
		}
		finding.Violation = parsed
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return findings, nil
}

// Deferred: the operator status and diagnosis surface is Phase 8's; no running
// process reads it yet
//
// ReconcilePolicyViolation closes one finding.
//
// It is one-way and the schema says so: a reopened finding is a reconciliation
// that never happened, and an audit reading these rows has to be able to tell
// "this was fixed" from "this was fixed, unfixed, and marked fixed again".
func (repository *HangarOutputRepository) ReconcilePolicyViolation(ctx context.Context, tx output.Tx, epoch int64, violation output.PolicyViolation, subject string) error {
	if err := violation.Validate(); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_policy_violations SET resolved_at = now()
		 WHERE activation_epoch = $1 AND violation = $2 AND subject = $3 AND resolved_at IS NULL`,
		epoch, string(violation), subject)
	if err != nil {
		return hangarConflict(err)
	}
	closed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if closed == 0 {
		return fmt.Errorf("%w: no open %s violation for %q at epoch %d",
			output.ErrNotFound, violation, subject, epoch)
	}

	return nil
}
