package outputplane

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// readWarrantRecordName is where the spent read warrants live. Like the
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
// unavailable (503) does not spend it, because the managed-input init retries
// that answer with the one warrant minted for its Pod.
//
// A warrant is also refused while a read under it is still in flight, so two
// presentations at once cannot both proceed.
//
// The set is keyed by read lease id, persisted in the control store, and
// pruned at each warrant's own expiry: a warrant past its window authorizes
// nothing anyway.
type spentReads struct {
	mu       sync.Mutex
	store    *controlStore
	spent    map[string]time.Time
	inFlight map[string]bool
	clock    func() time.Time
}

func openSpentReads(store *controlStore, clock func() time.Time) (*spentReads, error) {
	var recorded map[string]time.Time
	found, err := store.get(readWarrantRecordName, &recorded)
	if err != nil {
		return nil, err
	}
	reads := &spentReads{store: store, spent: map[string]time.Time{},
		inFlight: map[string]bool{}, clock: clock}
	if found {
		now := clock()
		for lease, expiresAt := range recorded {
			if now.Before(expiresAt) {
				reads.spent[lease] = expiresAt
			}
		}
	}

	return reads, nil
}

// begin admits one read under a warrant, or refuses it as unauthorized when
// the warrant was already spent or is in use.
func (reads *spentReads) begin(claims output.ReadWarrantClaims) error {
	reads.mu.Lock()
	defer reads.mu.Unlock()

	lease := string(claims.ReadLeaseID)
	if _, spent := reads.spent[lease]; spent || reads.inFlight[lease] {
		return fmt.Errorf("%w: the read warrant was already used on this node", output.ErrUnauthorized)
	}
	reads.inFlight[lease] = true

	return nil
}

// end closes the read begin admitted, and spends the warrant unless the read's
// own error is one a retry can change.
func (reads *spentReads) end(claims output.ReadWarrantClaims, readErr error) error {
	reads.mu.Lock()
	defer reads.mu.Unlock()

	lease := string(claims.ReadLeaseID)
	delete(reads.inFlight, lease)
	if readErr != nil && readRefusalClass(readErr) == output.ErrInfrastructure &&
		!errors.Is(readErr, output.ErrCorrupt) && !errors.Is(readErr, hangar.ErrCorrupt) {
		return nil
	}

	now := reads.clock()
	next := make(map[string]time.Time, len(reads.spent)+1)
	for spent, expiresAt := range reads.spent {
		if now.Before(expiresAt) {
			next[spent] = expiresAt
		}
	}
	next[lease] = claims.ExpiresAt.UTC()
	if err := reads.store.put(readWarrantRecordName, next); err != nil {
		return err
	}
	reads.spent = next

	return nil
}
