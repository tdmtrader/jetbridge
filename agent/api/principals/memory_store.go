package principals

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]Principal
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{nextID: 1, rows: map[int]Principal{}}
}

func (m *MemoryStore) Create(spec CreateSpec) (Principal, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.nextID
	m.nextID++

	token, prefix, hash, err := MintToken(id)
	if err != nil {
		return Principal{}, "", err
	}

	teamName := spec.TeamName
	if teamName == "" {
		teamName = "main"
	}

	p := Principal{
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

func (m *MemoryStore) List() ([]Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Principal{}
	for id := 1; id < m.nextID; id++ {
		if p, ok := m.rows[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *MemoryStore) Get(id int) (Principal, bool, error) {
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
