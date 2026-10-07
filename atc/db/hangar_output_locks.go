package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	Logical []HangarLogicalKey
	// CaptureRows are capture rows named by key, for a transaction that moves
	// one before it has a digest. Same class as Logical, taken after it.
	CaptureRows []output.CaptureKey
	Exact       []hangar.TreeRef
	Claims      []output.ClaimID
	ReadLeases  []output.ReadLeaseID
}

// HangarLocks is what the helper locked, in the order it locked it.
//
// Lifecycles maps each tree ref that exists to its lifecycle row id, because
// every caller wants it and re-reading it outside the lock would be reading a
// fact the lock was taken to freeze.
type HangarLocks struct {
	consumer   string
	Logical    []HangarLogicalKey
	Exact      []hangar.TreeRef
	Lifecycles map[hangar.TreeRef]int64
}

// LockHangarSuffix takes the complete Hangar lock suffix, in the one order this
// system has, after the caller's own domain prefix.
//
// The order is: the logical correlation -- capture rows and unregistered input
// publications sorted by scope bytes then digest bytes, then capture rows
// named by key; exact lifecycle rows sorted by scope, digest and numeric
// generation; then the subordinate claim and read-lease rows. Claimant and reader first
// makes a reclaimer recheck and skip; reclaimer first makes the claimant's
// transaction roll back without a usable binding or warrant. Both are correct
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
	}

	// 1. Logical correlation rows (capture rows and input publications), by
	// scope bytes then digest bytes.
	//
	// One statement per key rather than one IN list. ACROSS keys the order is
	// the CLIENT's, and it has to be: a single IN list would let the planner
	// produce the rows in whatever order it liked, and two transactions handed
	// the same two correlations could take them opposite ways round.
	//
	// WITHIN one key the ORDER BY is the order, and the earlier note here --
	// that an ORDER BY beside FOR NO KEY UPDATE is a hint about output only --
	// contradicted the statement three lines below it, which has always had
	// one. The statement is what is meant. A correlation can carry several
	// rows (one per capture or input publication at the same scope and
	// digest), PostgreSQL plans LockRows above Sort, and the rows are therefore
	// locked in the ORDER BY's order. It is a weaker guarantee than the loop above --
	// it rests on the plan shape rather than on the client -- and it is
	// sufficient, because the alternative it guards against is two transactions
	// taking the SAME key's rows in different orders, and both of them issue
	// this same statement.
	for _, key := range locks.Logical {
		// The tree itself, first: a capture moving to publishing onto this
		// (scope, digest) takes the same lock before its row has a digest to
		// be found by, so reclaim admission and the orphan sweep never decide
		// about a generation a capture is deduplicating onto.
		if err := hangarLockTree(ctx, tx, key.Scope, key.Digest); err != nil {
			return HangarLocks{}, err
		}
		// Every capture row resolved to this correlation: the publishing ones
		// reclaim admission must see, and the published ones a claim joins.
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_captures
			WHERE scope = $1 AND digest = $2
			ORDER BY execution_id, output_name
			FOR NO KEY UPDATE`, string(key.Scope), string(key.Digest)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_input_publications
			WHERE scope = $1 AND digest = $2
			ORDER BY reservation_id
			FOR NO KEY UPDATE`, string(key.Scope), string(key.Digest)); err != nil {
			return HangarLocks{}, hangarConflict(err)
		}
	}

	// 1b. Capture rows named by key, after every correlation's rows: a row a
	// correlation already locked is held, and a row it did not is taken in key
	// order behind it.
	for _, key := range sortedCaptureKeys(request.CaptureRows) {
		if _, err := tx.ExecContext(ctx, `
			SELECT 1 FROM hangar_captures
			WHERE execution_id = $1 AND output_name = $2
			FOR NO KEY UPDATE`, string(key.ExecutionID), string(key.Output)); err != nil {
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

	// 3. Subordinate rows: claims, read leases and the tombstones
	// that share their tables.
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

// LifecycleID is the locked lifecycle row for a tree ref, or ErrNotFound.
func (locks HangarLocks) LifecycleID(ref hangar.TreeRef) (int64, error) {
	id, ok := locks.Lifecycles[ref]
	if !ok {
		return 0, fmt.Errorf("%w: no lifecycle record for %s/%s/%d; a claim protects a registered "+
			"or adopted exact generation", output.ErrNotFound, ref.Scope, ref.Digest, ref.Generation)
	}

	return id, nil
}

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

func sortedCaptureKeys(keys []output.CaptureKey) []output.CaptureKey {
	sorted := slices.Clone(keys)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })

	return slices.Compact(sorted)
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
	slices.Sort(unique)

	return unique
}

// hangarLockEnabled is the in-service gate, taken FOR SHARE in the admitting
// transaction. It belongs to the outer prefix, before any consumer-domain or
// object-lifecycle lock; keeping it beside the suffix locks gives the guard one
// place to audit every Hangar row-lock acquisition.
//
// FOR SHARE and not a plain read: the web's startup write that turns the
// service off takes the row FOR UPDATE, so it waits for every admission already
// holding the row and every admission after it sees the new value. Nothing is
// admitted against a flag that moved underneath it.
func hangarLockEnabled(ctx context.Context, tx output.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT enabled FROM hangar_enabled WHERE singleton FOR SHARE`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var enabled bool
	if err := rows.Scan(&enabled); err != nil {
		return false, err
	}
	return enabled, rows.Err()
}

// hangarSetEnabled moves the in-service row, taking it FOR UPDATE first so it
// waits for every admission holding it FOR SHARE. It is the in-service
// prefix's one writer, beside the one reader above.
func hangarSetEnabled(ctx context.Context, tx output.Tx, enabled bool) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT enabled FROM hangar_enabled WHERE singleton FOR UPDATE`)
	if err != nil {
		return false, err
	}
	found, current := rows.Next(), false
	if found {
		if err := rows.Scan(&current); err != nil {
			rows.Close()
			return false, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	switch {
	case !found:
		_, err = tx.ExecContext(ctx, `INSERT INTO hangar_enabled (enabled) VALUES ($1)`, enabled)
	case current == enabled:
		return false, nil
	default:
		_, err = tx.ExecContext(ctx, `UPDATE hangar_enabled SET enabled = $1, updated_at = now() WHERE singleton`, enabled)
	}

	return err == nil, err
}

// hangarLockTree is the transaction-scoped advisory lock on one tree,
// (scope, digest). It is taken before any row lock: by LockHangarSuffix for
// every logical correlation, and by the move to publishing, whose row has no
// digest yet for a row lock to find.
func hangarLockTree(ctx context.Context, tx output.Tx, scope hangar.Scope, digest hangar.Digest) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hangar-tree:' || $1 || '/' || $2))`,
		string(scope), string(digest)); err != nil {
		return hangarConflict(err)
	}

	return nil
}
