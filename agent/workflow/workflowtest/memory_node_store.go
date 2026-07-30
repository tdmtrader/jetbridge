package workflowtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/workflow"
)

// MemoryNodeStore is a real semantic test double for the durable node store.
type MemoryNodeStore struct {
	mu          sync.Mutex
	nextID      int
	definitions []*workflow.NodeDefinition
}

func NewMemoryNodeStore() *MemoryNodeStore { return &MemoryNodeStore{} }
func (s *MemoryNodeStore) ImportManifest(name string, source workflow.Manifest, by string) (*workflow.NodeDefinition, error) {
	if err := source.Validate(); err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	c, err := workflow.CompileNodeDefinition(source)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if c.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("node definition name %q does not match import name %q", c.Name, name)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	max := 0
	h := source.Hash()
	for _, d := range s.definitions {
		if d.Name == name {
			if d.ContentHash == h {
				return cloneMemoryNode(d, true)
			}
			if d.Version > max {
				max = d.Version
			}
		}
	}
	s.nextID++
	d := &workflow.NodeDefinition{ID: s.nextID, Name: name, Version: max + 1, ContentHash: h, Description: c.Description, CreatedBy: by, CreatedAt: time.Now().Unix(), Compiled: *c, SourceManifest: cloneNodeManifest(source)}
	s.definitions = append(s.definitions, d)
	return cloneMemoryNode(d, true)
}
func (s *MemoryNodeStore) Get(n string, v int) (*workflow.NodeDefinition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.definitions {
		if d.Name == n && d.Version == v {
			c, e := cloneMemoryNode(d, true)
			return c, true, e
		}
	}
	return nil, false, nil
}
func (s *MemoryNodeStore) Latest(n string) (*workflow.NodeDefinition, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var x *workflow.NodeDefinition
	for _, d := range s.definitions {
		if d.Name == n && (x == nil || d.Version > x.Version) {
			x = d
		}
	}
	if x == nil {
		return nil, false, nil
	}
	c, e := cloneMemoryNode(x, true)
	return c, true, e
}
func (s *MemoryNodeStore) List() ([]workflow.NodeDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := map[string]*workflow.NodeDefinition{}
	for _, d := range s.definitions {
		if x := latest[d.Name]; x == nil || x.Version < d.Version {
			latest[d.Name] = d
		}
	}
	out := make([]workflow.NodeDefinition, 0, len(latest))
	for _, d := range latest {
		c, e := cloneMemoryNode(d, true)
		if e != nil {
			return nil, e
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *MemoryNodeStore) Versions(ctx context.Context, n string, r workflow.VersionPageRequest) (workflow.NodeVersionPage, error) {
	if ctx == nil {
		return workflow.NodeVersionPage{}, fmt.Errorf("workflow: version page context is required")
	}
	if r.Limit <= 0 || r.Limit > workflow.MaxVersionPageSize || r.Cursor < 0 || r.Cursor > workflow.MaxWorkflowVersion {
		return workflow.NodeVersionPage{}, workflow.ErrInvalidVersionPage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := workflow.NodeVersionPage{Definitions: []workflow.NodeDefinition{}}
	for _, d := range s.definitions {
		if d.Name == n {
			p.Found = true
			if r.Cursor == 0 || d.Version < r.Cursor {
				c, e := cloneMemoryNode(d, true)
				if e != nil {
					return p, e
				}
				p.Definitions = append(p.Definitions, *c)
			}
		}
	}
	sort.Slice(p.Definitions, func(i, j int) bool { return p.Definitions[i].Version > p.Definitions[j].Version })
	if len(p.Definitions) > r.Limit {
		p.Definitions = p.Definitions[:r.Limit]
		p.NextCursor = p.Definitions[len(p.Definitions)-1].Version
	}
	for i, j := 0, len(p.Definitions)-1; i < j; i, j = i+1, j-1 {
		p.Definitions[i], p.Definitions[j] = p.Definitions[j], p.Definitions[i]
	}
	return p, nil
}
func (s *MemoryNodeStore) Released(n string, v int) (workflow.NodeDefinition, bool, error) {
	d, ok, e := s.Get(n, v)
	if e != nil || !ok || d.Release.ReleasedAt == 0 {
		return workflow.NodeDefinition{}, false, e
	}
	return *d, true, nil
}
func (s *MemoryNodeStore) Release(n string, v int, c workflow.ReleaseCompatibility, by string) (workflow.NodeRelease, error) {
	if c != workflow.ReleaseCompatible && c != workflow.ReleaseBreaking {
		return workflow.NodeRelease{}, workflow.ErrInvalidCompatibility
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var t, prior *workflow.NodeDefinition
	for _, d := range s.definitions {
		if d.Name == n && d.Version == v {
			t = d
		}
		if d.Name == n && d.Release.ReleasedAt != 0 && (prior == nil || d.Version > prior.Version) {
			prior = d
		}
	}
	if t == nil {
		return workflow.NodeRelease{}, workflow.ErrVersionNotFound
	}
	if t.Release.ReleasedAt != 0 {
		return t.Release, nil
	}
	if c == workflow.ReleaseCompatible && prior != nil && !workflow.NodeDefinitionsStructurallyCompatible(prior.Compiled, t.Compiled) {
		return workflow.NodeRelease{}, workflow.ErrInvalidCompatibility
	}
	t.Release = workflow.NodeRelease{ReleasedAt: time.Now().Unix(), ReleasedBy: by, Compatibility: c}
	if prior != nil {
		t.Release.PredecessorVersion = prior.Version
	}
	return t.Release, nil
}
func (s *MemoryNodeStore) Deprecate(n string, v int, deprecated bool, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.definitions {
		if d.Name == n && d.Version == v {
			if deprecated {
				d.DeprecatedAt = time.Now().Unix()
				d.DeprecatedBy = by
			} else {
				d.DeprecatedAt = 0
				d.DeprecatedBy = ""
			}
			return nil
		}
	}
	return workflow.ErrVersionNotFound
}
func cloneNodeManifest(m workflow.Manifest) workflow.Manifest {
	out := workflow.Manifest{}
	for p, c := range m {
		out[p] = c
	}
	return out
}
func cloneMemoryNode(d *workflow.NodeDefinition, content bool) (*workflow.NodeDefinition, error) {
	x := *d
	x.Compiled = workflow.CompiledNodeDefinition{}
	x.SourceManifest = nil
	if !content {
		return &x, nil
	}
	x.SourceManifest = cloneNodeManifest(d.SourceManifest)
	c, e := workflow.CompileNodeDefinition(x.SourceManifest)
	if e != nil {
		return nil, e
	}
	x.Compiled = *c
	return &x, nil
}
