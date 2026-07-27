// Package principalstest provides the in-memory agent-principal store the
// principals API tests, the atc auth/wrappa suites, and the atc/api suite run
// against. It lives outside the production package so no test double is
// compiled into the web binary.
package principalstest

import (
	"github.com/concourse/concourse/agent/api/principals"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]principals.Principal
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{nextID: 1, rows: map[int]principals.Principal{}}
}

func (m *MemoryStore) Create(spec principals.CreateSpec) (principals.Principal, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.nextID
	m.nextID++

	token, prefix, hash, err := principals.MintToken(id)
	if err != nil {
		return principals.Principal{}, "", err
	}

	teamName := spec.TeamName
	if teamName == "" {
		teamName = "main"
	}

	p := principals.Principal{
		ID:          id,
		Name:        spec.Name,
		Description: spec.Description,
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      append([]string{}, spec.Scopes...),
		TeamName:    teamName,
		CreatedBy:   spec.CreatedBy,
		CreatedAt:   time.Now().Unix(),
		ExpiresAt:   spec.ExpiresAt,
	}
	m.rows[id] = p
	return p, token, nil
}

func (m *MemoryStore) List() ([]principals.Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []principals.Principal{}
	for id := 1; id < m.nextID; id++ {
		if p, ok := m.rows[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *MemoryStore) Get(id int) (principals.Principal, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	return p, ok, nil
}

func (m *MemoryStore) Revoke(id int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	if !ok {
		return false, nil
	}
	if p.RevokedAt == nil {
		now := time.Now().Unix()
		p.RevokedAt = &now
		m.rows[id] = p
	}
	return true, nil
}

func (m *MemoryStore) RecordUse(id int, usedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.rows[id]; ok {
		epoch := usedAt.Unix()
		p.LastUsedAt = &epoch
		m.rows[id] = p
	}
	return nil
}
