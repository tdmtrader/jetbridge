package workflow

import "errors"

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (contracts §6). ContentHash is
// hex(sha256(Manifest.Canonical())) — raw-YAML imports hash their
// single-file wrapping, so the scheme is uniform (design 2026-07-17 §3;
// pre-slice rows carry the legacy raw-bytes hash and re-mint one
// version on their next import).
type Definition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Live        bool   `json:"live"`
	Description string `json:"description"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`

	Config Config `json:"config"` // parsed YAML, §6 grammar

	// RawYAML is the stored workflow.yml bytes. Populated by Get and
	// Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`

	// SourceManifest is the imported source tree (path -> content), the
	// hashed provenance unit for manifest imports. Populated by Get and
	// Live (like RawYAML); empty in List/Versions. Nil for legacy rows
	// imported before the source-format slice.
	SourceManifest Manifest `json:"source_manifest,omitempty"`
}

// ErrVersionNotFound is returned by Promote when (name, version) does
// not exist.
var ErrVersionNotFound = errors.New("workflow version not found")

// InvalidDefinitionError wraps parse/validation/name-mismatch failures
// so API handlers can map them to 400 responses.
type InvalidDefinitionError struct{ Err error }

func (e InvalidDefinitionError) Error() string { return e.Err.Error() }
func (e InvalidDefinitionError) Unwrap() error { return e.Err }

//counterfeiter:generate . Store
type Store interface {
	// Import wraps rawYAML into the single-file manifest
	// {"workflow.yml": rawYAML} and delegates to ImportManifest — the
	// degenerate case of the source format (design 2026-07-17 §2).
	Import(name string, rawYAML []byte, createdBy string) (*Definition, error)
	// ImportManifest compiles and stores a source tree; idempotent on
	// the canonical-manifest hash.
	ImportManifest(name string, m Manifest, createdBy string) (*Definition, error)
	Get(name string, version int) (*Definition, bool, error)
	Live(name string) (*Definition, bool, error)
	Latest(name string) (*Definition, bool, error) // highest version, live or not
	List() ([]Definition, error)                   // latest version per name + live marker
	LiveVersions() (map[string]int, error)         // name -> live version, one query for all names
	Versions(name string) ([]Definition, error)
	Promote(name string, version int, promotedBy string) error // atomically swaps the live flag
}
