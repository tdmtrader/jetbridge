package principals

import (
	"errors"
	"testing"
	"time"
)

// clockStore is the smallest Store a verifier needs, defined here rather than
// pulled from principalstest: this file is an INTERNAL test (it drives the
// verifier's unexported clock), and principalstest imports principals, so
// importing it back would be a cycle.
type clockStore struct {
	principal Principal
}

func (s *clockStore) Create(spec CreateSpec) (Principal, string, error) {
	token, prefix, hash, err := MintToken(1)
	if err != nil {
		return Principal{}, "", err
	}
	s.principal = Principal{
		ID: 1, Name: spec.Name, TokenPrefix: prefix, TokenHash: hash,
		Scopes: append([]string{}, spec.Scopes...), TeamName: "main",
		CreatedAt: time.Now().Unix(),
	}
	return s.principal, token, nil
}

func (s *clockStore) List() ([]Principal, error) { return []Principal{s.principal}, nil }

func (s *clockStore) Get(id int) (Principal, bool, error) {
	if id != s.principal.ID {
		return Principal{}, false, nil
	}
	return s.principal, true, nil
}

func (s *clockStore) Revoke(id int) (bool, error) {
	if id != s.principal.ID {
		return false, nil
	}
	if s.principal.RevokedAt == nil {
		now := time.Now().Unix()
		s.principal.RevokedAt = &now
	}
	return true, nil
}

func (s *clockStore) RecordUse(int, time.Time) error { return nil }

func TestVerifyCacheWindow(t *testing.T) {
	store := &clockStore{}
	created, token, err := store.Create(CreateSpec{Name: "r", Scopes: []string{ScopeTicketsRead}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	current := time.Now()
	v := NewVerifier(store)
	v.now = func() time.Time { return current }

	if _, err := v.Verify(token, ScopeTicketsRead); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	// Revocation lands after the row was cached: still accepted inside
	// the 60s window (documented staleness, contracts §1.2)...
	if _, err := store.Revoke(created.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := v.Verify(token, ScopeTicketsRead); err != nil {
		t.Errorf("within cache window: err = %v, want nil (60s staleness)", err)
	}

	// ...and rejected once the cache entry ages out.
	current = current.Add(61 * time.Second)
	if _, err := v.Verify(token, ScopeTicketsRead); !errors.Is(err, ErrRevoked) {
		t.Errorf("after cache window: err = %v, want ErrRevoked", err)
	}
}
