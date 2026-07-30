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
