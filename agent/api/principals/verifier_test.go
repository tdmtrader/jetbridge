package principals_test

import (
	"errors"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/principals/principalstest"
	"testing"
	"time"
)

func mustCreate(t *testing.T, store *principalstest.MemoryStore, spec principals.CreateSpec) (principals.Principal, string) {
	t.Helper()
	p, token, err := store.Create(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p, token
}

func TestVerifyHappyPath(t *testing.T) {
	store := principalstest.NewMemoryStore()
	created, token := mustCreate(t, store, principals.CreateSpec{Name: "reviewer", Scopes: []string{principals.ScopeTicketsRead}})

	v := principals.NewVerifier(store)
	p, err := v.Verify(token, principals.ScopeTicketsRead)
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
	store := principalstest.NewMemoryStore()
	_, token := mustCreate(t, store, principals.CreateSpec{Name: "reviewer", Scopes: []string{principals.ScopeTicketsRead}})

	v := principals.NewVerifier(store)

	if _, err := v.Verify("not-a-token", principals.ScopeTicketsRead); !errors.Is(err, principals.ErrInvalidToken) {
		t.Errorf("garbage token: err = %v, want principals.ErrInvalidToken", err)
	}
	if _, err := v.Verify("cap1.999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", principals.ScopeTicketsRead); !errors.Is(err, principals.ErrInvalidToken) {
		t.Errorf("unknown id: err = %v, want principals.ErrInvalidToken", err)
	}
	if _, err := v.Verify(token+"x", principals.ScopeTicketsRead); !errors.Is(err, principals.ErrInvalidToken) {
		t.Errorf("wrong secret: err = %v, want principals.ErrInvalidToken", err)
	}
	if _, err := v.Verify(token, principals.ScopeTicketsWrite); !errors.Is(err, principals.ErrMissingScope) {
		t.Errorf("missing scope: err = %v, want principals.ErrMissingScope", err)
	}
}

func TestVerifyRevokedBeforeFirstUse(t *testing.T) {
	store := principalstest.NewMemoryStore()
	created, token := mustCreate(t, store, principals.CreateSpec{Name: "r", Scopes: []string{principals.ScopeTicketsRead}})
	store.Revoke(created.ID)

	v := principals.NewVerifier(store)
	if _, err := v.Verify(token, principals.ScopeTicketsRead); !errors.Is(err, principals.ErrRevoked) {
		t.Errorf("err = %v, want principals.ErrRevoked", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	store := principalstest.NewMemoryStore()
	past := time.Now().Add(-time.Hour).Unix()
	_, token := mustCreate(t, store, principals.CreateSpec{Name: "r", Scopes: []string{principals.ScopeTicketsRead}, ExpiresAt: &past})

	v := principals.NewVerifier(store)
	if _, err := v.Verify(token, principals.ScopeTicketsRead); !errors.Is(err, principals.ErrExpired) {
		t.Errorf("err = %v, want principals.ErrExpired", err)
	}
}
