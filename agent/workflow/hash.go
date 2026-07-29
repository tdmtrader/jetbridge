package workflow

import (
	"crypto/sha256"
	"fmt"
)

// Hash returns hex(sha256(raw)) over the exact definition bytes — the
// platform's content-hash provenance primitive (contracts §1.6/§2.2).
// The fixed test vector in hash_test.go pins the semantics.
func Hash(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("%x", h)
}

func hashDomainSeparated(domain string, payload []byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write(payload)
	return fmt.Sprintf("%x", hasher.Sum(nil))
}
