package workflow

import (
	"context"
	"errors"
)

type ReleaseCompatibility string

const (
	ReleaseCompatible ReleaseCompatibility = "compatible"
	ReleaseBreaking   ReleaseCompatibility = "breaking"
)

var ErrInvalidCompatibility = errors.New("workflow: node release is not structurally compatible")

type NodeRelease struct {
	ReleasedAt         int64                `json:"released_at,omitempty"`
	ReleasedBy         string               `json:"released_by,omitempty"`
	PredecessorVersion int                  `json:"predecessor_version,omitempty"`
	Compatibility      ReleaseCompatibility `json:"compatibility,omitempty"`
}

type NodeDefinition struct {
	ID             int                    `json:"id"`
	Name           string                 `json:"name"`
	Version        int                    `json:"version"`
	ContentHash    string                 `json:"content_hash"`
	Description    string                 `json:"description"`
	CreatedBy      string                 `json:"created_by"`
	CreatedAt      int64                  `json:"created_at"`
	Compiled       CompiledNodeDefinition `json:"compiled"`
	SourceManifest Manifest               `json:"source_manifest,omitempty"`
	Release        NodeRelease            `json:"release"`
	DeprecatedAt   int64                  `json:"deprecated_at,omitempty"`
	DeprecatedBy   string                 `json:"deprecated_by,omitempty"`
}

type NodeVersionPage struct {
	Definitions []NodeDefinition
	NextCursor  int
	Found       bool
}

// NodeDefinitionsStructurallyCompatible reports whether every invocation and
// output contract accepted by previous remains valid for candidate.
func NodeDefinitionsStructurallyCompatible(previous, candidate CompiledNodeDefinition) bool {
	candidateInputs := map[string]struct {
		Type     string
		Optional bool
	}{}
	for _, input := range candidate.Function.Inputs {
		candidateInputs[input.Name] = struct {
			Type     string
			Optional bool
		}{string(input.Type), input.Optional}
	}
	previousInputs := map[string]struct{}{}
	for _, input := range previous.Function.Inputs {
		previousInputs[input.Name] = struct{}{}
		replacement, found := candidateInputs[input.Name]
		if !found || replacement.Type != string(input.Type) || replacement.Optional != input.Optional {
			return false
		}
	}
	for _, input := range candidate.Function.Inputs {
		if _, existed := previousInputs[input.Name]; !existed && !input.Optional {
			return false
		}
	}

	candidateOutputs := map[string]struct {
		Type     string
		Optional bool
	}{}
	for _, output := range candidate.Function.Outputs {
		candidateOutputs[output.Name] = struct {
			Type     string
			Optional bool
		}{string(output.Type), output.Optional}
	}
	for _, output := range previous.Function.Outputs {
		replacement, found := candidateOutputs[output.Name]
		if !found || replacement.Type != string(output.Type) || replacement.Optional != output.Optional {
			return false
		}
	}

	candidateParameters := map[string]*string{}
	for _, parameter := range candidate.Parameters {
		candidateParameters[parameter.Name] = parameter.Default
	}
	previousParameters := map[string]struct{}{}
	for _, parameter := range previous.Parameters {
		previousParameters[parameter.Name] = struct{}{}
		replacementDefault, found := candidateParameters[parameter.Name]
		if !found || parameter.Default != nil && replacementDefault == nil {
			return false
		}
	}
	for _, parameter := range candidate.Parameters {
		if _, existed := previousParameters[parameter.Name]; !existed && parameter.Default == nil {
			return false
		}
	}
	return true
}

//counterfeiter:generate . NodeStore
type NodeStore interface {
	ImportManifest(name string, source Manifest, createdBy string) (*NodeDefinition, error)
	Get(name string, version int) (*NodeDefinition, bool, error)
	Latest(name string) (*NodeDefinition, bool, error)
	List() ([]NodeDefinition, error)
	Versions(context.Context, string, VersionPageRequest) (NodeVersionPage, error)
	Released(name string, version int) (NodeDefinition, bool, error)
	Release(name string, version int, compatibility ReleaseCompatibility, releasedBy string) (NodeRelease, error)
	Deprecate(name string, version int, deprecated bool, deprecatedBy string) error
}
