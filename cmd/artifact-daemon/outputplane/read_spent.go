package outputplane

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// readWarrantRecordName is where the used read warrants live. Like the
// capability replay record it carries neither ledger's record prefix, so
// neither ledger, nor the artifact daemon's classifier, enumerates it.
const readWarrantRecordName = "read-warrants-spent.json"

// spentReads makes a read warrant single-use on this node, durably.
//
// It is what the read lease's release used to buy, kept on the node now that
// the node no longer asks the web. A read's protection was given back when the
// read ended, so the warrant bound to it could not be spent again; the web
// still gives the LEASE back (or its abandoned-lease cleaner closes it at
// expiry), and this is the warrant's half: once a read under it has ended --
// verified, refused, or failed for a reason a retry cannot change -- the same
// warrant opens nothing more here. A failure this daemon answers as
// unavailable (503) gives the warrant back, because the managed-input init
// retries that answer with the one warrant minted for its Pod.
//
// The warrant is recorded IN FLIGHT, durably, before its read begins. A second
// presentation while it is in flight is refused, and so is one after a crash
// mid-read: a read this process may already have completed is not one a
// restarted process can tell from one it did not, so the record fails closed
// and the warrant counts as used. (The managed-input init checks its own sealed
// receipt before it asks again, so a materialization that finished before the
// crash still completes.)
//
// The set is keyed by read lease id and pruned at each warrant's own expiry: a
// warrant past its window authorizes nothing anyway.
type spentReads struct {
	mu    sync.Mutex
	store *controlStore
	used  map[string]usedWarrant
	clock func() time.Time
}

// usedWarrant is one record entry.
type usedWarrant struct {
	ExpiresAt time.Time `json:"expires_at"`
	// InFlight is true from begin until end. Read back after a restart it
	// still means "used": see spentReads.
	InFlight bool `json:"in_flight"`
}

func openSpentReads(store *controlStore, clock func() time.Time) (*spentReads, error) {
	var recorded map[string]usedWarrant
	found, err := store.get(readWarrantRecordName, &recorded)
	if err != nil {
		return nil, err
	}
	reads := &spentReads{store: store, used: map[string]usedWarrant{}, clock: clock}
	if found {
		now := clock()
		for lease, entry := range recorded {
			if now.Before(entry.ExpiresAt) {
				// An entry left in flight by a previous process counts as used.
				entry.InFlight = false
				reads.used[lease] = entry
			}
		}
	}

	return reads, nil
}

// begin admits one read under a warrant -- recording it in flight before
// returning -- or refuses it as unauthorized when the warrant was already used
// or is in use.
func (reads *spentReads) begin(claims output.ReadWarrantClaims) error {
	reads.mu.Lock()
	defer reads.mu.Unlock()

	lease := string(claims.ReadLeaseID)
	if _, used := reads.used[lease]; used {
		return fmt.Errorf("%w: the read warrant was already used on this node", output.ErrUnauthorized)
	}

	return reads.save(lease, &usedWarrant{ExpiresAt: claims.ExpiresAt.UTC(), InFlight: true})
}

// end closes the read begin admitted: the warrant stays used unless the read's
// own error is one a retry can change, in which case it is given back.
func (reads *spentReads) end(claims output.ReadWarrantClaims, readErr error) error {
	reads.mu.Lock()
	defer reads.mu.Unlock()

	lease := string(claims.ReadLeaseID)
	if readErr != nil && readRefusalClass(readErr) == output.ErrInfrastructure &&
		!errors.Is(readErr, output.ErrCorrupt) && !errors.Is(readErr, hangar.ErrCorrupt) {
		return reads.save(lease, nil)
	}

	return reads.save(lease, &usedWarrant{ExpiresAt: claims.ExpiresAt.UTC()})
}

// save writes the set with one entry set (or removed, for nil), pruning
// expired entries, and adopts it only once it is durable.
func (reads *spentReads) save(lease string, entry *usedWarrant) error {
	now := reads.clock()
	next := make(map[string]usedWarrant, len(reads.used)+1)
	for other, existing := range reads.used {
		if other != lease && now.Before(existing.ExpiresAt) {
			next[other] = existing
		}
	}
	if entry != nil {
		next[lease] = *entry
	}
	if err := reads.store.put(readWarrantRecordName, next); err != nil {
		return err
	}
	reads.used = next

	return nil
}
