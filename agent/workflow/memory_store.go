package workflow

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing (mirrors
// agent/api/reviews.MemoryStore).
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	defs   []*Definition
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*Definition, error) {
	cfg, err := Parse(rawYAML)
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := Hash(rawYAML)

	m.mu.Lock()
	defer m.mu.Unlock()

	maxVersion := 0
	for _, d := range m.defs {
		if d.Name != name {
			continue
		}
		if d.ContentHash == hash {
			cp := *d
			return &cp, nil // idempotent on hash
		}
		if d.Version > maxVersion {
			maxVersion = d.Version
		}
	}

	m.nextID++
	def := &Definition{
		ID:          m.nextID,
		Name:        name,
		Version:     maxVersion + 1,
		ContentHash: hash,
		Description: cfg.Description,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().Unix(),
		Config:      *cfg,
		RawYAML:     string(rawYAML),
	}
	m.defs = append(m.defs, def)
	cp := *def
	return &cp, nil
}

func (m *MemoryStore) Get(name string, version int) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			cp := *d
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Live(name string) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Live {
			cp := *d
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) List() ([]Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest := map[string]*Definition{}
	for _, d := range m.defs {
		if cur, ok := latest[d.Name]; !ok || d.Version > cur.Version {
			latest[d.Name] = d
		}
	}
	out := []Definition{}
	for _, d := range latest {
		cp := *d
		cp.RawYAML = "" // metadata-only listing
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Versions(name string) ([]Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Definition{}
	for _, d := range m.defs {
		if d.Name == name {
			cp := *d
			cp.RawYAML = ""
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (m *MemoryStore) Promote(name string, version int, promotedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *Definition
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			target = d
			break
		}
	}
	if target == nil {
		return ErrVersionNotFound
	}
	for _, d := range m.defs {
		if d.Name == name {
			d.Live = false
		}
	}
	target.Live = true
	_ = promotedBy // persisted by the DB store; the memory store only flips flags
	return nil
}
