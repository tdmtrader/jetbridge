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
	mu        sync.Mutex
	nextID    int
	defs      []*Definition
	lifecycle map[string]lifecycleEntry
}

type lifecycleEntry struct {
	hidden     bool
	annotation string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*Definition, error) {
	return m.ImportManifest(name, Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

func (m *MemoryStore) ImportManifest(name string, src Manifest, createdBy string) (*Definition, error) {
	cfg, err := Compile(src)
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := src.Hash()

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

	stored := Manifest{}
	for p, c := range src {
		stored[p] = c
	}
	m.nextID++
	def := &Definition{
		ID:             m.nextID,
		Name:           name,
		Version:        maxVersion + 1,
		ContentHash:    hash,
		Description:    cfg.Description,
		CreatedBy:      createdBy,
		CreatedAt:      time.Now().Unix(),
		Config:         *cfg,
		RawYAML:        src["workflow.yml"],
		SourceManifest: stored,
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
			cp := m.decorate(*d)
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
			cp := m.decorate(*d)
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Latest(name string) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Definition
	for _, d := range m.defs {
		if d.Name == name && (latest == nil || d.Version > latest.Version) {
			latest = d
		}
	}
	if latest == nil {
		return nil, false, nil
	}
	cp := m.decorate(*latest)
	return &cp, true, nil
}

func (m *MemoryStore) LiveVersions() (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, d := range m.defs {
		if d.Live {
			out[d.Name] = d.Version
		}
	}
	return out, nil
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
		cp.SourceManifest = nil
		out = append(out, m.decorate(cp))
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
			cp.SourceManifest = nil
			out = append(out, m.decorate(cp))
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

func (m *MemoryStore) exists(name string) bool {
	for _, d := range m.defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (m *MemoryStore) Annotate(name, annotation, updatedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exists(name) {
		return ErrVersionNotFound
	}
	if m.lifecycle == nil {
		m.lifecycle = map[string]lifecycleEntry{}
	}
	e := m.lifecycle[name]
	e.annotation = annotation
	m.lifecycle[name] = e
	_ = updatedBy
	return nil
}

func (m *MemoryStore) SetHidden(name string, hidden bool, updatedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exists(name) {
		return ErrVersionNotFound
	}
	if m.lifecycle == nil {
		m.lifecycle = map[string]lifecycleEntry{}
	}
	e := m.lifecycle[name]
	e.hidden = hidden
	m.lifecycle[name] = e
	_ = updatedBy
	return nil
}

// decorate stamps the name-level lifecycle metadata onto a returned copy.
func (m *MemoryStore) decorate(d Definition) Definition {
	if e, ok := m.lifecycle[d.Name]; ok {
		d.Hidden = e.hidden
		d.Annotation = e.annotation
	}
	return d
}
