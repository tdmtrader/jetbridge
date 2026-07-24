package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing (mirrors
// agent/api/reviews.MemoryStore).
type MemoryStore struct {
	mu                 sync.Mutex
	nextID             int
	defs               []*Definition
	promotionValidator PromotionValidator
}

func NewMemoryStore(promotionValidators ...PromotionValidator) *MemoryStore {
	if len(promotionValidators) > 1 {
		panic("workflow: NewMemoryStore accepts at most one promotion validator")
	}
	store := &MemoryStore{}
	if len(promotionValidators) == 1 {
		store.promotionValidator = promotionValidators[0]
	}
	return store
}

func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*Definition, error) {
	return m.ImportManifest(name, Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

func (m *MemoryStore) ImportManifest(name string, src Manifest, createdBy string) (*Definition, error) {
	if err := src.Validate(); err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	raw, ok := src["workflow.yml"]
	if !ok {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("workflow: manifest has no workflow.yml")}
	}
	if err := RequireSchemaVersion3([]byte(raw)); err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	compiled, err := CompileDefinition(src)
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	if compiled.Name != name {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", compiled.Name, name)}
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
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
			if d.SchemaVersion != metadata.SchemaVersion || d.SignatureVersion != metadata.SignatureVersion {
				return nil, fmt.Errorf("workflow: stored metadata for %q version %d does not match compiled source", name, d.Version)
			}
			return cloneMemoryDefinition(d, true) // idempotent on hash
		}
		if d.Version > maxVersion {
			maxVersion = d.Version
		}
	}
	if metadata.SignatureVersion > 0 {
		candidate, err := compiled.PublicSignature()
		if err != nil {
			return nil, InvalidDefinitionError{Err: err}
		}
		for _, existing := range m.defs {
			if existing.Name != name || existing.SignatureVersion != metadata.SignatureVersion {
				continue
			}
			prior, err := existing.Compiled.PublicSignature()
			if err != nil {
				return nil, fmt.Errorf("workflow: stored signature for %q version %d is invalid", name, existing.Version)
			}
			if !candidate.Equal(prior) {
				return nil, InvalidDefinitionError{Err: fmt.Errorf("workflow %q signature_version %d is incompatible with version %d", name, metadata.SignatureVersion, existing.Version)}
			}
			break
		}
	}

	stored := Manifest{}
	for p, c := range src {
		stored[p] = c
	}
	m.nextID++
	def := &Definition{
		ID:               m.nextID,
		Name:             name,
		Version:          maxVersion + 1,
		SchemaVersion:    metadata.SchemaVersion,
		SignatureVersion: metadata.SignatureVersion,
		ContentHash:      hash,
		Description:      compiled.Description,
		CreatedBy:        createdBy,
		CreatedAt:        time.Now().Unix(),
		Compiled:         *compiled,
		RawYAML:          src["workflow.yml"],
		SourceManifest:   stored,
	}
	if compiled.Legacy != nil {
		def.Config = *compiled.Legacy
	}
	m.defs = append(m.defs, def)
	return cloneMemoryDefinition(def, true)
}

func (m *MemoryStore) Get(name string, version int) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			cp, err := cloneMemoryDefinition(d, true)
			return cp, true, err
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Live(name string) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Live {
			cp, err := cloneMemoryDefinition(d, true)
			return cp, true, err
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
	cp, err := cloneMemoryDefinition(latest, true)
	return cp, true, err
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
		cp, err := cloneMemoryDefinition(d, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Versions(ctx context.Context, name string, request VersionPageRequest) (VersionPage, error) {
	if ctx == nil {
		return VersionPage{}, errors.New("workflow: version page context is required")
	}
	if err := ctx.Err(); err != nil {
		return VersionPage{}, err
	}
	if request.Limit <= 0 || request.Limit > MaxVersionPageSize ||
		request.Cursor < 0 || request.Cursor > MaxWorkflowVersion {
		return VersionPage{}, ErrInvalidVersionPage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	page := VersionPage{Definitions: []Definition{}}
	candidates := []*Definition{}
	for _, d := range m.defs {
		if d.Name == name {
			page.Found = true
			if request.Cursor == 0 || d.Version < request.Cursor {
				candidates = append(candidates, d)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Version > candidates[j].Version })
	if len(candidates) > request.Limit {
		candidates = candidates[:request.Limit]
		page.NextCursor = candidates[len(candidates)-1].Version
	}
	for index := len(candidates) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return VersionPage{}, err
		}
		cp, err := cloneMemoryDefinition(candidates[index], false)
		if err != nil {
			return VersionPage{}, err
		}
		page.Definitions = append(page.Definitions, *cp)
	}
	return page, nil
}

func (m *MemoryStore) Promote(name string, version int, promotedBy string) (PromotionResult, error) {
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
		return PromotionResult{}, ErrVersionNotFound
	}
	if target.SchemaVersion != 3 {
		return PromotionResult{}, InvalidPromotionError{
			Err: UnsupportedSchemaVersionError{Got: target.SchemaVersion},
		}
	}
	if m.promotionValidator == nil {
		return PromotionResult{}, InvalidPromotionError{Err: ErrPromotionValidatorRequired}
	}
	candidate, err := cloneMemoryDefinition(target, true)
	if err != nil {
		return PromotionResult{}, InvalidPromotionError{Err: err}
	}
	if err := m.promotionValidator.ValidatePromotion(*candidate); err != nil {
		return PromotionResult{}, InvalidPromotionError{Err: err}
	}
	var previous *Definition
	for _, d := range m.defs {
		if d.Name == name && d.Live {
			previous = d
			break
		}
	}
	for _, d := range m.defs {
		if d.Name == name {
			d.Live = false
		}
	}
	target.Live = true
	_ = promotedBy // persisted by the DB store; the memory store only flips flags
	result := PromotionResult{Target: target.VersionMetadata()}
	if previous != nil {
		metadata := previous.VersionMetadata()
		result.PreviousLive = &metadata
		result.SignatureChanged = metadata.SignatureVersion != result.Target.SignatureVersion
	}
	return result, nil
}

// cloneMemoryDefinition prevents maps, slices, and concrete step pointers in a
// returned compiled definition from mutating the in-memory store's authority.
func cloneMemoryDefinition(definition *Definition, includeContent bool) (*Definition, error) {
	clone := *definition
	clone.Config = Config{}
	clone.Compiled = CompiledDefinition{}
	clone.RawYAML = ""
	clone.SourceManifest = nil
	if !includeContent {
		return &clone, nil
	}
	source := make(Manifest, len(definition.SourceManifest))
	for path, content := range definition.SourceManifest {
		source[path] = content
	}
	compiled, err := CompileDefinition(source)
	if err != nil {
		return nil, fmt.Errorf("workflow: stored definition %q version %d no longer compiles: %w", definition.Name, definition.Version, err)
	}
	clone.Compiled = *compiled
	if compiled.Legacy != nil {
		clone.Config = *compiled.Legacy
	}
	clone.RawYAML = source["workflow.yml"]
	clone.SourceManifest = source
	return &clone, nil
}
