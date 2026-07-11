package workflow

import "errors"

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (contracts §6). ContentHash is
// hex(sha256(raw YAML bytes)) — identical fn to ci-agent/phaseconfig.Hash
// (see Hash in this package).
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

	// RawYAML is the exact stored definition bytes (the hashed provenance
	// unit). Populated by Get and Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`
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
	Import(name string, rawYAML []byte, createdBy string) (*Definition, error) // idempotent on hash
	Get(name string, version int) (*Definition, bool, error)
	Live(name string) (*Definition, bool, error)
	List() ([]Definition, error) // latest version per name + live marker
	Versions(name string) ([]Definition, error)
	Promote(name string, version int, promotedBy string) error // atomically swaps the live flag
}
