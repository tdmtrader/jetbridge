package principals

import (
	"errors"
	"testing"
	"time"
)

func mustCreate(t *testing.T, store *MemoryStore, spec CreateSpec) (Principal, string) {
	t.Helper()
	p, token, err := store.Create(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p, token
}

func TestVerifyHappyPath(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "reviewer", Scopes: []string{ScopeTicketsRead}})

	v := NewVerifier(store)
	p, err := v.Verify(token, ScopeTicketsRead)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.ID != created.ID || p.Name != "reviewer" {
		t.Errorf("verified principal = %+v", p)
	}

	got, _, _ := store.Get(created.ID)
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at recorded on first verification")
	}
}

func TestVerifyRejections(t *testing.T) {
	store := NewMemoryStore()
	_, token := mustCreate(t, store, CreateSpec{Name: "reviewer", Scopes: []string{ScopeTicketsRead}})

	v := NewVerifier(store)

	if _, err := v.Verify("not-a-token", ScopeTicketsRead); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("garbage token: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify("cap1.999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ScopeTicketsRead); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("unknown id: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify(token+"x", ScopeTicketsRead); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify(token, ScopeTicketsWrite); !errors.Is(err, ErrMissingScope) {
		t.Errorf("missing scope: err = %v, want ErrMissingScope", err)
	}
}

func TestVerifyRevokedBeforeFirstUse(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeTicketsRead}})
	store.Revoke(created.ID)

	v := NewVerifier(store)
	if _, err := v.Verify(token, ScopeTicketsRead); !errors.Is(err, ErrRevoked) {
		t.Errorf("err = %v, want ErrRevoked", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	store := NewMemoryStore()
	past := time.Now().Add(-time.Hour).Unix()
	_, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeTicketsRead}, ExpiresAt: &past})

	v := NewVerifier(store)
	if _, err := v.Verify(token, ScopeTicketsRead); !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
}

func TestVerifyCacheWindow(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeTicketsRead}})

	current := time.Now()
	v := NewVerifier(store)
	v.now = func() time.Time { return current }

	if _, err := v.Verify(token, ScopeTicketsRead); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	// Revocation lands after the row was cached: still accepted inside
	// the 60s window (documented staleness, contracts §1.2)...
	store.Revoke(created.ID)
	if _, err := v.Verify(token, ScopeTicketsRead); err != nil {
		t.Errorf("within cache window: err = %v, want nil (60s staleness)", err)
	}

	// ...and rejected once the cache entry ages out.
	current = current.Add(61 * time.Second)
	if _, err := v.Verify(token, ScopeTicketsRead); !errors.Is(err, ErrRevoked) {
		t.Errorf("after cache window: err = %v, want ErrRevoked", err)
	}
}
