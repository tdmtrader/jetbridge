package outputplane

import (
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// controlWarrantRecordName is where the spent control warrants live.
//
// It is in the same control directory as the ledgers and it deliberately
// carries neither record prefix: the two ledgers enumerate their own records by
// prefix at startup, and this is not one of theirs. It is a single record
// rather than one per nonce because the whole set is small -- bounded by
// hangar.MaxWarrantTTL, which is at most fifteen minutes of one node's control
// traffic -- and because replacing it is one atomic rename.
const controlWarrantRecordName = "control-warrants-spent.json"

// spentWarrants makes a control warrant single-use on this node, durably.
//
// A control warrant authorizes one operation; a second presentation of the
// same nonce is refused whether or not the first one succeeded, because a node
// that let the second through could not tell a retry from a captured warrant.
// The set is keyed by purpose and nonce -- the two control purposes keep
// separate namespaces -- and entries are dropped once past their expiry, so
// the record is bounded by the warrant TTL and not by uptime.
//
// It is kept in the control directory rather than in memory because without
// that the refusal was a property of one process's uptime: a crash, a rollout
// or an OOM kill inside a warrant's window made a captured warrant good for
// one more operation. The verifier (hangar.Verifier) is stateless by design;
// this daemon already owns a durable, atomically-replaced control directory,
// and a second persistence mechanism beside it would be a second thing to get
// wrong.
type spentWarrants struct {
	mu    sync.Mutex
	store *controlStore
	spent map[string]time.Time
	clock func() time.Time
}

// openSpentWarrants reads back what a previous process spent.
//
// Called once at startup, before the listener. A load that fails is not
// survivable by ignoring it: the plane would then admit every warrant spent
// before the restart, which is the defect the record exists to close.
func openSpentWarrants(store *controlStore, clock func() time.Time) (*spentWarrants, error) {
	var recorded map[string]time.Time
	found, err := store.get(controlWarrantRecordName, &recorded)
	if err != nil {
		return nil, err
	}
	warrants := &spentWarrants{store: store, spent: map[string]time.Time{}, clock: clock}
	if found {
		now := clock().UTC()
		for key, expiresAt := range recorded {
			// Pruned on the way in. A nonce past its expiry cannot authorize
			// anything, so keeping it would only make the record grow.
			if now.Before(expiresAt) {
				warrants.spent[key] = expiresAt
			}
		}
	}

	return warrants, nil
}

func spentKey(warrant hangar.Warrant) string { return string(warrant.Purpose) + "|" + warrant.Nonce }

// spend admits one verified control warrant, or refuses it as unauthorized
// when its nonce was already presented.
//
// It is recorded as spent BEFORE the operation runs, and a failure to record
// it refuses the operation. A node that admitted a warrant it could not
// remember would be one restart away from admitting it twice, and this is the
// direction that must fail closed.
func (warrants *spentWarrants) spend(warrant hangar.Warrant) error {
	warrants.mu.Lock()
	defer warrants.mu.Unlock()

	now := warrants.clock().UTC()
	key := spentKey(warrant)
	if _, replayed := warrants.spent[key]; replayed {
		return fmt.Errorf("%w: the %s warrant's nonce has already been presented; a warrant "+
			"authorizes one operation", output.ErrUnauthorized, warrant.Purpose)
	}
	next := make(map[string]time.Time, len(warrants.spent)+1)
	for other, expiresAt := range warrants.spent {
		if now.Before(expiresAt) {
			next[other] = expiresAt
		}
	}
	next[key] = time.Unix(0, warrant.ExpiresAt).UTC()
	if err := warrants.store.put(controlWarrantRecordName, next); err != nil {
		return fmt.Errorf("%w: this warrant could not be recorded as spent, and one that "+
			"is not recorded is one a restart would admit again: %v", output.ErrUnauthorized, err)
	}
	warrants.spent = next

	return nil
}
