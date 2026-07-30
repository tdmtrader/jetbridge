// Package workflowtest provides the in-memory workflow-definition store the
// workflow tests, the workflows API tests, and the atc/api suite run against.
// It lives outside the production package so no test double is compiled into
// the web binary.
package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"github.com/concourse/concourse/agent/workflow"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory workflow.Store (mirrors
// agent/api/reviews/reviewstest.MemoryStore).
type MemoryStore struct {
	mu                 sync.Mutex
	nextID             int
	defs               []*workflow.Definition
	promotionValidator workflow.PromotionValidator
	nodeResolver       workflow.NodeResolver
	lifecycle          map[string]lifecycleEntry
}

// lifecycleEntry is the name-keyed (not version-keyed) lifecycle metadata,
// mirroring the DB store's agent_workflow_lifecycle row.
type lifecycleEntry struct {
	hidden     bool
	annotation string
}

func NewMemoryStore(promotionValidators ...workflow.PromotionValidator) *MemoryStore {
	return newMemoryStore(nil, promotionValidators...)
}

// NewMemoryStoreWithNodeResolver enables exact released-node expansion for
// workflow imports while leaving NewMemoryStore's workflow-only behavior
// unchanged.
func NewMemoryStoreWithNodeResolver(resolver workflow.NodeResolver, promotionValidators ...workflow.PromotionValidator) *MemoryStore {
	if resolver == nil {
		panic("workflow: node-aware memory store requires a node resolver")
	}
	return newMemoryStore(resolver, promotionValidators...)
}

func newMemoryStore(resolver workflow.NodeResolver, promotionValidators ...workflow.PromotionValidator) *MemoryStore {
	if len(promotionValidators) > 1 {
		panic("workflow: NewMemoryStore accepts at most one promotion validator")
	}
	store := &MemoryStore{nodeResolver: resolver}
	if len(promotionValidators) == 1 {
		store.promotionValidator = promotionValidators[0]
	}
	return store
}

func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*workflow.Definition, error) {
	return m.ImportManifest(name, workflow.Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

func (m *MemoryStore) ImportManifest(name string, src workflow.Manifest, createdBy string) (*workflow.Definition, error) {
	outcome, err := m.ImportManifestWithOutcome(name, src, createdBy)
	if err != nil {
		return nil, err
	}
	return outcome.Definition, nil
}

func (m *MemoryStore) ImportManifestWithOutcome(
	name string,
	src workflow.Manifest,
	createdBy string,
) (workflow.ImportOutcome, error) {
	if err := src.Validate(); err != nil {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: err}
	}
	raw, ok := src.DefinitionSource()
	if !ok {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: fmt.Errorf("workflow: manifest has no %s (or legacy %s)", workflow.WorkflowFileName, workflow.LegacyWorkflowFileName)}
	}
	if err := workflow.RequireSchemaVersion3([]byte(raw)); err != nil {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: err}
	}
	compiled, err := m.compileDefinition(src)
	if err != nil {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: err}
	}
	if compiled.Name != name {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", compiled.Name, name)}
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil {
		return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: err}
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
				return workflow.ImportOutcome{}, fmt.Errorf("workflow: stored metadata for %q version %d does not match compiled source", name, d.Version)
			}
			definition, err := m.cloneMemoryDefinition(d, true)
			if err != nil {
				return workflow.ImportOutcome{}, err
			}
			return workflow.ImportOutcome{Definition: definition, Inserted: false}, nil
		}
		if d.Version > maxVersion {
			maxVersion = d.Version
		}
	}
	if metadata.SignatureVersion > 0 {
		candidate, err := compiled.PublicSignature()
		if err != nil {
			return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: err}
		}
		for _, existing := range m.defs {
			if existing.Name != name || existing.SignatureVersion != metadata.SignatureVersion {
				continue
			}
			prior, err := existing.Compiled.PublicSignature()
			if err != nil {
				return workflow.ImportOutcome{}, fmt.Errorf("workflow: stored signature for %q version %d is invalid", name, existing.Version)
			}
			if !candidate.Equal(prior) {
				return workflow.ImportOutcome{}, workflow.InvalidDefinitionError{Err: fmt.Errorf("workflow %q signature_version %d is incompatible with version %d", name, metadata.SignatureVersion, existing.Version)}
			}
			break
		}
	}

	stored := workflow.Manifest{}
	for p, c := range src {
		stored[p] = c
	}
	nextID := m.nextID + 1
	def := &workflow.Definition{
		ID:               nextID,
		Name:             name,
		Version:          maxVersion + 1,
		SchemaVersion:    metadata.SchemaVersion,
		SignatureVersion: metadata.SignatureVersion,
		ContentHash:      hash,
		Description:      compiled.Description,
		CreatedBy:        createdBy,
		CreatedAt:        time.Now().Unix(),
		Compiled:         *compiled,
		RawYAML:          raw,
		SourceManifest:   stored,
	}
	definition, err := m.cloneMemoryDefinition(def, true)
	if err != nil {
		return workflow.ImportOutcome{}, err
	}
	m.nextID = nextID
	m.defs = append(m.defs, def)
	return workflow.ImportOutcome{Definition: definition, Inserted: true}, nil
}

func (m *MemoryStore) compileDefinition(source workflow.Manifest) (*workflow.CompiledDefinition, error) {
	if m.nodeResolver == nil {
		return workflow.CompileDefinition(source)
	}
	compiled, _, err := workflow.CompileDefinitionWithNodes(source, m.nodeResolver)
	return compiled, err
}

func (m *MemoryStore) Get(name string, version int) (*workflow.Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			cp, err := m.cloneMemoryDefinition(d, true)
			if err != nil {
				return nil, false, err
			}
			*cp = m.decorate(*cp)
			return cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Live(name string) (*workflow.Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Live {
			cp, err := m.cloneMemoryDefinition(d, true)
			if err != nil {
				return nil, false, err
			}
			*cp = m.decorate(*cp)
			return cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Latest(name string) (*workflow.Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *workflow.Definition
	for _, d := range m.defs {
		if d.Name == name && (latest == nil || d.Version > latest.Version) {
			latest = d
		}
	}
	if latest == nil {
		return nil, false, nil
	}
	cp, err := m.cloneMemoryDefinition(latest, true)
	if err != nil {
		return nil, false, err
	}
	*cp = m.decorate(*cp)
	return cp, true, nil
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

func (m *MemoryStore) List() ([]workflow.Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest := map[string]*workflow.Definition{}
	for _, d := range m.defs {
		if cur, ok := latest[d.Name]; !ok || d.Version > cur.Version {
			latest[d.Name] = d
		}
	}
	out := []workflow.Definition{}
	for _, d := range latest {
		cp, err := m.cloneMemoryDefinition(d, false) // metadata-only listing
		if err != nil {
			return nil, err
		}
		out = append(out, m.decorate(*cp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Versions(ctx context.Context, name string, request workflow.VersionPageRequest) (workflow.VersionPage, error) {
	if ctx == nil {
		return workflow.VersionPage{}, errors.New("workflow: version page context is required")
	}
	if err := ctx.Err(); err != nil {
		return workflow.VersionPage{}, err
	}
	if request.Limit <= 0 || request.Limit > workflow.MaxVersionPageSize ||
		request.Cursor < 0 || request.Cursor > workflow.MaxWorkflowVersion {
		return workflow.VersionPage{}, workflow.ErrInvalidVersionPage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	page := workflow.VersionPage{Definitions: []workflow.Definition{}}
	candidates := []*workflow.Definition{}
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
			return workflow.VersionPage{}, err
		}
		cp, err := m.cloneMemoryDefinition(candidates[index], false)
		if err != nil {
			return workflow.VersionPage{}, err
		}
		page.Definitions = append(page.Definitions, m.decorate(*cp))
	}
	return page, nil
}

func (m *MemoryStore) Promote(name string, version int, promotedBy string) (workflow.PromotionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *workflow.Definition
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			target = d
			break
		}
	}
	if target == nil {
		return workflow.PromotionResult{}, workflow.ErrVersionNotFound
	}
	if target.SchemaVersion != 3 {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{
			Err: workflow.UnsupportedSchemaVersionError{Got: target.SchemaVersion},
		}
	}
	if m.promotionValidator == nil {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: workflow.ErrPromotionValidatorRequired}
	}
	candidate, err := m.cloneMemoryDefinition(target, true)
	if err != nil {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: err}
	}
	if err := m.promotionValidator.ValidatePromotion(*candidate); err != nil {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: err}
	}
	var previous *workflow.Definition
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
	result := workflow.PromotionResult{Target: target.VersionMetadata()}
	if previous != nil {
		metadata := previous.VersionMetadata()
		result.PreviousLive = &metadata
		result.SignatureChanged = metadata.SignatureVersion != result.Target.SignatureVersion
	}
	return result, nil
}

// cloneMemoryDefinition prevents maps, slices, and concrete step pointers in a
// returned compiled definition from mutating the in-memory store's authority.
func (m *MemoryStore) cloneMemoryDefinition(definition *workflow.Definition, includeContent bool) (*workflow.Definition, error) {
	clone := *definition
	clone.Compiled = workflow.CompiledDefinition{}
	clone.RawYAML = ""
	clone.SourceManifest = nil
	if !includeContent {
		return &clone, nil
	}
	if definition.SchemaVersion != 3 {
		clone.RawYAML = definition.RawYAML
		return &clone, nil
	}
	source := make(workflow.Manifest, len(definition.SourceManifest))
	for path, content := range definition.SourceManifest {
		source[path] = content
	}
	compiled, err := m.compileDefinition(source)
	if err != nil {
		return nil, fmt.Errorf("workflow: stored definition %q version %d no longer compiles: %w", definition.Name, definition.Version, err)
	}
	clone.Compiled = *compiled
	clone.RawYAML, _ = source.DefinitionSource()
	clone.SourceManifest = source
	return &clone, nil
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
		return workflow.ErrVersionNotFound
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
		return workflow.ErrVersionNotFound
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
func (m *MemoryStore) decorate(d workflow.Definition) workflow.Definition {
	if e, ok := m.lifecycle[d.Name]; ok {
		d.Hidden = e.hidden
		d.Annotation = e.annotation
	}
	return d
}
