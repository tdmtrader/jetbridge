package composition

import (
	"crypto/sha256"
	"fmt"
	"strconv"

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
// that will not be written at the same time: it is the server-scoped key the
// versioned port replays on, which is what makes the call table a join rather
// than a second dedup mechanism. That only works if there is one place the
// value is spelled.
//
// So it is a valid invocation key (requirement 14's alphabet and length,
// atc.ValidRunInvocationToken). A
// plan id is usually short hex and is kept verbatim; one the alphabet cannot
// carry (a derived id such as "5a/image-get" or a sidecar's) or one that would
// overflow the bound is replaced by its digest under a distinct marker, so the
// two spellings can never meet.
//
// Deliberately not the digest of anything the call sends: keying on the
// sealed-input digest would move the key when a prior step's pod is evicted,
// and one logical invocation would be admitted twice.
func ContractKey(buildID int, planID atc.PlanID) string {
	prefix := "build." + strconv.Itoa(buildID)
	if key := prefix + ".plan." + string(planID); atc.ValidRunInvocationToken(key) {
		return key
	}
	return fmt.Sprintf("%s.plan-sha256.%x", prefix, sha256.Sum256([]byte(planID)))
}
