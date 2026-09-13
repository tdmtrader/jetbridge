package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// atc/db.Tx is the transaction hangar/output composes with.
//
// The leaf declares output.Tx as the two methods it needs and nothing that can
// commit, precisely so that the transaction stays the caller's. This assertion
// is the other half of that claim: if atc/db.Tx ever stops satisfying it, the
// seam is broken here, in the package that owns the transaction, rather than in
// whichever caller happened to try composing next.
var _ output.Tx = Tx(nil)

// ErrHangarLockRetry is a derived fact that changed between the unlocked read
// that produced the candidate lock set and the revalidation under the locks.
//
// It is a retry rather than a failure because nothing is wrong: another actor
// legitimately moved the row while this transaction was choosing what to lock.
// The caller rolls back, re-derives and comes round again. It is typed so that
// a retry loop cannot be written by accident around a real conflict.
var ErrHangarLockRetry = errors.New("atc/db: hangar lock set derived from stale facts, retry")

// HangarConsumerPrefix is a caller's statement that its own domain locks are
// already held.
//
// Hangar cannot verify it -- a consumer's tables are not Hangar's to inspect,
// and the whole point of the boundary is that Hangar never learns what they
// are. What the token does is make the claim explicit and attributable: the
// consumer names itself, the name travels into every diagnostic the helper
// produces, and a caller that reached the suffix without thinking about its own
// prefix has to write down that it did.
type HangarConsumerPrefix struct {
	consumer string
}

// HangarConsumerPrefixHeld mints the token. The name is the consumer's, and it
// is required: an anonymous token would be a boolean with extra steps.
func HangarConsumerPrefixHeld(consumer string) (HangarConsumerPrefix, error) {
	if strings.TrimSpace(consumer) == "" {
		return HangarConsumerPrefix{}, fmt.Errorf("%w: a consumer prefix token names the consumer "+
			"that holds it; an anonymous one states nothing", output.ErrIncomplete)
	}

	return HangarConsumerPrefix{consumer: consumer}, nil
}

// Consumer is the name the token carries.
func (prefix HangarConsumerPrefix) Consumer() string { return prefix.consumer }

// HangarLogicalKey is one server-derived (scope, digest) correlation.
type HangarLogicalKey struct {
	Scope  hangar.Scope
	Digest hangar.Digest
}

// HangarLockRequest is the complete set of Hangar rows a transaction will
// touch, by class.
//
// Every field is a slice of typed identities. There is deliberately no query
// string, no predicate and no callback: a helper that took a closure would be a
// helper whose lock order depends on what the closure does, and a helper that
// took SQL would not be the only place Hangar rows are locked. Duplicates and
// order within a field do not matter -- the helper deduplicates and sorts,
// which is what makes two transactions given the same refs in opposite orders
// take them in the same order.
type HangarLockRequest struct {
	Logical    []HangarLogicalKey
	Exact      []hangar.TreeRef
	Captures   []output.ReservationID
	Receipts   []output.ReservationID
	Claims     []output.ClaimID
	ReadLeases []output.ReadLeaseID
}

// HangarLocks is what the helper locked, in the order it locked it.
//
// Lifecycles maps each exact ref that exists to its lifecycle row id, because
// every caller wants it and re-reading it outside the lock would be reading a
// fact the lock was taken to freeze.
type HangarLocks struct {
	consumer   string
	Logical    []HangarLogicalKey
	Exact      []hangar.TreeRef
	Lifecycles map[hangar.TreeRef]int64
	Captures   []output.ReservationID
}

// LockHangarSuffix takes the complete Hangar lock suffix, in the one order this
// system has, after the caller's own domain prefix.
//
// The order is: logical reservation rows sorted by scope bytes then digest
// bytes; exact lifecycle rows sorted by scope, digest and numeric generation;
// capture and reservation rows in stable capture-identity order; then the
// subordinate receipt, claim and read-lease rows. Claimant and reader first
// makes a reclaimer recheck and skip; reclaimer first makes the claimant's
// transaction roll back without a usable binding or grant. Both are correct
// outcomes and neither is a deadlock, which is the entire reason the order is
// an API and not a convention.
//
// It performs no network work, holds no lock across anything but its own
// statements, and never touches a consumer's tables.
func LockHangarSuffix(ctx context.Context, tx output.Tx, prefix HangarConsumerPrefix, request HangarLockRequest) (HangarLocks, error) {
	if prefix.consumer == "" {
		return HangarLocks{}, fmt.Errorf("%w: the Hangar lock suffix was entered with no consumer "+
			"prefix token; a consumer acquires its own creation or lifecycle prefix and then enters "+
			"this suffix", output.ErrIncomplete)
	}

	locks := HangarLocks{
		consumer:   prefix.consumer,
		Logical:    sortedLogicalKeys(request.Logical),
		Exact:      sortedExactRefs(request.Exact),
		Lifecycles: map[hangar.TreeRef]int64{},
		Captures:   sortedReservationIDs(request.Captures),
	}

	// 1. Logical reservation rows, by scope bytes then digest bytes.
	//
	// One statement per key rather than one IN list, because PostgreSQL locks
	// rows in whatever order the plan produced them and an ORDER BY beside FOR
	// NO KEY UPDATE is a hint about output, not about lock acquisition. The
	// order has to be the client's.
	for _, key := range locks.Logical {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_logical_reservations
			WHERE scope = $1 AND digest = $2
			ORDER BY reservation_id
			FOR NO KEY UPDATE`, string(key.Scope), string(key.Digest)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}

	// 2. Exact lifecycle rows, by scope, digest and numeric generation.
	for _, ref := range locks.Exact {
		rows, err := tx.QueryContext(ctx, `
			SELECT id FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3
			FOR NO KEY UPDATE`, string(ref.Scope), string(ref.Digest), ref.Generation)
		if err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
		var id int64
		found := rows.Next()
		if found {
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()

				return HangarLocks{}, err
			}
		}
		if err := rows.Close(); err != nil {
			return HangarLocks{}, err
		}
		if found {
			locks.Lifecycles[ref] = id
		}
	}

	// 3. Capture and reservation rows, in stable capture-identity order.
	for _, id := range locks.Captures {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_capture_reservations
			WHERE reservation_id = $1
			FOR NO KEY UPDATE`, string(id)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}

	// 4. Subordinate rows: receipts, claims, read leases and the tombstones
	// that share their tables.
	for _, id := range sortedReservationIDs(request.Receipts) {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_output_receipts WHERE reservation_id = $1 FOR UPDATE`,
			string(id)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}
	for _, id := range sortedClaimIDs(request.Claims) {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_claims WHERE claim_id = $1 FOR UPDATE`,
			string(id)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}
	for _, id := range sortedReadLeaseIDs(request.ReadLeases) {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_read_leases WHERE read_lease_id = $1 FOR UPDATE`,
			string(id)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}

	return locks, nil
}

// LifecycleID is the locked lifecycle row for an exact ref, or ErrNotFound.
func (locks HangarLocks) LifecycleID(ref hangar.TreeRef) (int64, error) {
	id, ok := locks.Lifecycles[ref]
	if !ok {
		return 0, fmt.Errorf("%w: no lifecycle record for %s/%s/%d; a claim protects a registered "+
			"or adopted exact generation", output.ErrNotFound, ref.Scope, ref.Digest, ref.Generation)
	}

	return id, nil
}

// HangarDerivation is a candidate lock set derived from facts read without row
// locks, together with the facts it was derived from.
//
// It exists because a caller that does not yet know a digest or a generation
// cannot name the rows it must lock, and reaching back to an earlier lock class
// after taking a later one is the deadlock this order exists to prevent. So the
// facts are read unlocked, the transaction restarts at the consumer's prefix,
// the earlier classes are taken, and then the derivation is checked against
// what the locks froze.
type HangarDerivation struct {
	ReservationID   output.ReservationID
	HandoffID       output.HandoffID
	ActivationEpoch uint64
	CaptureFence    output.CaptureFence
	Logical         HangarLogicalKey
	Resolved        bool
}

// DeriveHangarRefUnlocked reads the capture, reservation and receipt facts for
// one reservation with no row locks at all.
func DeriveHangarRefUnlocked(ctx context.Context, tx output.Tx, reservation output.ReservationID) (HangarDerivation, error) {
	if err := reservation.Validate(); err != nil {
		return HangarDerivation{}, err
	}

	derived := HangarDerivation{ReservationID: reservation}
	var (
		scope, digest sql.NullString
		epoch         int64
		fence         int64
		handoff       string
	)
	rows, err := tx.QueryContext(ctx, `
		SELECT r.handoff_id, r.activation_epoch, `+hangarCurrentCaptureFence+`,
		       g.scope, g.digest
		FROM hangar_capture_reservations r
		LEFT JOIN hangar_logical_reservations g ON g.reservation_id = r.reservation_id
		WHERE r.reservation_id = $1`, string(reservation))
	if err != nil {
		return HangarDerivation{}, hangarConflict(err)
	}
	defer rows.Close()

	if !rows.Next() {
		return HangarDerivation{}, fmt.Errorf("%w: no capture reservation %s",
			output.ErrNotFound, reservation)
	}
	if err := rows.Scan(&handoff, &epoch, &fence, &scope, &digest); err != nil {
		return HangarDerivation{}, err
	}

	derived.HandoffID = output.HandoffID(handoff)
	derived.ActivationEpoch = uint64(epoch)
	derived.CaptureFence = output.CaptureFence(fence)
	if scope.Valid && digest.Valid {
		derived.Resolved = true
		derived.Logical = HangarLogicalKey{
			Scope:  hangar.Scope(scope.String),
			Digest: hangar.Digest(digest.String),
		}
	}

	return derived, rows.Err()
}

// RevalidateDerivation re-reads the derived facts now that the locks are held.
//
// A mismatch is ErrHangarLockRetry, not a conflict: the caller rolls back,
// derives again from the newer truth and takes the suffix again. Nothing here
// reaches back to a lock class the caller has already passed.
func (locks HangarLocks) RevalidateDerivation(ctx context.Context, tx output.Tx, derived HangarDerivation) error {
	fresh, err := DeriveHangarRefUnlocked(ctx, tx, derived.ReservationID)
	if err != nil {
		return err
	}
	if fresh.HandoffID != derived.HandoffID {
		return fmt.Errorf("%w: reservation %s now belongs to handoff %s, not %s",
			ErrHangarLockRetry, derived.ReservationID, fresh.HandoffID, derived.HandoffID)
	}
	if fresh.ActivationEpoch != derived.ActivationEpoch {
		return fmt.Errorf("%w: reservation %s now names activation epoch %d, not %d",
			ErrHangarLockRetry, derived.ReservationID, fresh.ActivationEpoch, derived.ActivationEpoch)
	}
	if fresh.CaptureFence != derived.CaptureFence {
		return fmt.Errorf("%w: reservation %s is now owned at capture fence %d, not %d; the "+
			"derivation was made by an owner that has since been superseded",
			ErrHangarLockRetry, derived.ReservationID, fresh.CaptureFence, derived.CaptureFence)
	}
	if fresh.Resolved != derived.Resolved || fresh.Logical != derived.Logical {
		return fmt.Errorf("%w: reservation %s now resolves to %s/%s, not %s/%s",
			ErrHangarLockRetry, derived.ReservationID,
			fresh.Logical.Scope, fresh.Logical.Digest,
			derived.Logical.Scope, derived.Logical.Digest)
	}

	return nil
}

// The sorters. Each deduplicates first, so a batch that names one ref twice
// locks it once, and two transactions handed the same batch in opposite orders
// take the same locks in the same order.

func sortedLogicalKeys(keys []HangarLogicalKey) []HangarLogicalKey {
	seen := map[HangarLogicalKey]bool{}
	unique := make([]HangarLogicalKey, 0, len(keys))
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			unique = append(unique, key)
		}
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Scope != unique[j].Scope {
			return unique[i].Scope < unique[j].Scope
		}

		return unique[i].Digest < unique[j].Digest
	})

	return unique
}

func sortedExactRefs(refs []hangar.TreeRef) []hangar.TreeRef {
	seen := map[hangar.TreeRef]bool{}
	unique := make([]hangar.TreeRef, 0, len(refs))
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			unique = append(unique, ref)
		}
	}
	// Generation is compared numerically, not as text: "9" sorts after "10" as
	// bytes, and a lock order that depends on how a number was spelled is not
	// an order.
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Scope != unique[j].Scope {
			return unique[i].Scope < unique[j].Scope
		}
		if unique[i].Digest != unique[j].Digest {
			return unique[i].Digest < unique[j].Digest
		}

		return unique[i].Generation < unique[j].Generation
	})

	return unique
}

func sortedReservationIDs(ids []output.ReservationID) []output.ReservationID {
	return sortedOpaque(ids)
}

func sortedClaimIDs(ids []output.ClaimID) []output.ClaimID { return sortedOpaque(ids) }

func sortedReadLeaseIDs(ids []output.ReadLeaseID) []output.ReadLeaseID { return sortedOpaque(ids) }

func sortedOpaque[T ~string](ids []T) []T {
	seen := map[T]bool{}
	unique := make([]T, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })

	return unique
}
