package workflow

import (
	"crypto/sha256"
	"fmt"
)

// Hash returns hex(sha256(raw)) over the exact definition bytes — the
// same function as ci-agent/phaseconfig.Hash, generalized here as the
// platform's content-hash provenance primitive (contracts §1.6/§2.2).
// ci-agent is a separate Go module with no dependency on this one, so
// the function is re-declared rather than imported; the fixed test
// vector in hash_test.go pins the semantics.
func Hash(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("%x", h)
}
