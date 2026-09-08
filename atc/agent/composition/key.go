package composition

import (
	"fmt"

	"github.com/concourse/concourse/atc"
)

// ContractKey is the value this package supplies as the port's contract key.
//
// It is the canonicalized call identity and nothing else: the build the caller
// runs in, and the caller's node in that build's plan. Derived, not minted --
// no server round trip, no identifier assigned after the fact -- so the same
// node asking twice produces the same key without anything having to remember
// it.
//
// One exported function, because the value has to be the same in two places
// that will not be written at the same time. When the run contract's
// server-scoped key lands, the value this package already passes is the value
// it will pass then, and the call table becomes a join rather than a second
// dedup mechanism. That only works if there is one place the value is spelled.
//
// Deliberately not the digest of anything: keying on the sealed-input digest
// would move the key when a prior step's pod is evicted, and one logical
// invocation would be admitted twice.
func ContractKey(buildID int, planID atc.PlanID) string {
	return fmt.Sprintf("composition/build/%d/plan/%s", buildID, planID)
}
