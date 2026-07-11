package principals

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid agent principal token")
	ErrRevoked      = errors.New("agent principal is revoked")
	ErrExpired      = errors.New("agent principal token is expired")
	ErrMissingScope = errors.New("agent principal lacks required scope")
)

// cacheTTL: verification is one indexed SELECT with a 60s in-memory
// cache (00-shared-contracts.md §1.2 decision). Consequence: revocation
// may take up to 60s to be seen by a running web node.
const cacheTTL = 60 * time.Second

type cacheEntry struct {
	principal Principal
	fetchedAt time.Time
}

// Verifier checks cap1 wire tokens against the Store.
type Verifier struct {
	store Store
	now   func() time.Time

	mu    sync.Mutex
	cache map[int]cacheEntry
}

func NewVerifier(store Store) *Verifier {
	return &Verifier{store: store, now: time.Now, cache: map[int]cacheEntry{}}
}

// Verify parses the wire token, loads the principal (cached up to 60s),
// constant-time compares the hash, and checks revocation, expiry, and
// scope. All failures map to 401 at the HTTP layer.
func (v *Verifier) Verify(token, scope string) (Principal, error) {
	id, ok := ParseTokenID(token)
	if !ok {
		return Principal{}, ErrInvalidToken
	}

	p, found, err := v.lookup(id)
	if err != nil {
		return Principal{}, err
	}
	if !found {
		return Principal{}, ErrInvalidToken
	}

	if subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(p.TokenHash)) != 1 {
		return Principal{}, ErrInvalidToken
	}
	if p.RevokedAt != nil {
		return Principal{}, ErrRevoked
	}
	if p.ExpiresAt != nil && *p.ExpiresAt <= v.now().Unix() {
		return Principal{}, ErrExpired
	}
	if !p.HasScope(scope) {
		return Principal{}, ErrMissingScope
	}
	return p, nil
}

func (v *Verifier) lookup(id int) (Principal, bool, error) {
	v.mu.Lock()
	entry, ok := v.cache[id]
	v.mu.Unlock()
	if ok && v.now().Sub(entry.fetchedAt) < cacheTTL {
		return entry.principal, true, nil
	}

	p, found, err := v.store.Get(id)
	if err != nil || !found {
		return Principal{}, found, err
	}

	v.mu.Lock()
	v.cache[id] = cacheEntry{principal: p, fetchedAt: v.now()}
	v.mu.Unlock()

	// Best-effort usage stamp, at most once per cache fill.
	_ = v.store.RecordUse(id, v.now())

	return p, true, nil
}
