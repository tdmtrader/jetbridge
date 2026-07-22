package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// CompiledDefinition is the tagged parsed representation shared by legacy
// workflow definitions and schema-version-3 workflow functions. Exactly one
// arm is populated. Definition.Config remains the legacy store/API
// compatibility field until the version-3 persistence work lands.
type CompiledDefinition struct {
	SchemaVersion int             `json:"schema_version" yaml:"schema_version"`
	Name          string          `json:"name" yaml:"name"`
	Description   string          `json:"description,omitempty" yaml:"description,omitempty"`
	Legacy        *Config         `json:"legacy,omitempty" yaml:"legacy,omitempty"`
	Function      *FunctionConfig `json:"function,omitempty" yaml:"function,omitempty"`
}

// VersionMetadata derives the durable schema/signature identity from the
// validated tagged definition. Callers must never accept these values from an
// import request independently of the compiled source.
func (definition CompiledDefinition) VersionMetadata() (VersionMetadata, error) {
	if strings.TrimSpace(definition.Name) == "" {
		return VersionMetadata{}, fmt.Errorf("workflow: name is required")
	}
	metadata := VersionMetadata{SchemaVersion: definition.SchemaVersion}
	switch definition.SchemaVersion {
	case 1, 2:
		// Legacy compilation deliberately retains source-reference fields while
		// also materializing their values. Re-running source-form Validate here
		// would reject that established compiled representation, so validate the
		// tagged identity without changing legacy compile bytes.
		if definition.Legacy == nil || definition.Function != nil ||
			definition.Legacy.SchemaVersion != definition.SchemaVersion ||
			definition.Legacy.Name != definition.Name {
			return VersionMetadata{}, fmt.Errorf("workflow: invalid schema_version %d compiled definition", definition.SchemaVersion)
		}
		metadata.SignatureVersion = 0
	case 3:
		if definition.Legacy != nil || definition.Function == nil {
			return VersionMetadata{}, fmt.Errorf("workflow: schema_version 3 requires exactly the function definition arm")
		}
		if err := definition.Function.Validate(); err != nil {
			return VersionMetadata{}, err
		}
		metadata.SignatureVersion = definition.Function.SignatureVersion
		if metadata.SignatureVersion <= 0 {
			return VersionMetadata{}, fmt.Errorf("workflow: schema_version 3 requires a positive signature_version")
		}
	default:
		return VersionMetadata{}, fmt.Errorf("workflow: unsupported schema_version %d", definition.SchemaVersion)
	}
	return metadata, nil
}

// PublicSignature is the ordered, implementation-independent contract of a
// schema-version-3 workflow. Descriptions and output source mappings are not
// contract identity.
type PublicSignature struct {
	Inputs  []SignaturePort `json:"inputs"`
	Outputs []SignaturePort `json:"outputs"`
}

type SignaturePort struct {
	Name     string           `json:"name"`
	Type     snapshot.TypeRef `json:"type"`
	Optional bool             `json:"optional"`
}

func (definition CompiledDefinition) PublicSignature() (PublicSignature, error) {
	metadata, err := definition.VersionMetadata()
	if err != nil {
		return PublicSignature{}, err
	}
	if metadata.SchemaVersion != 3 {
		return PublicSignature{}, fmt.Errorf("workflow: schema_version %d has no typed public signature", metadata.SchemaVersion)
	}
	signature := PublicSignature{
		Inputs:  make([]SignaturePort, len(definition.Function.Inputs)),
		Outputs: make([]SignaturePort, len(definition.Function.Outputs)),
	}
	for index, port := range definition.Function.Inputs {
		signature.Inputs[index] = SignaturePort{Name: port.Name, Type: port.Type, Optional: port.Optional}
	}
	for index, output := range definition.Function.Outputs {
		port := output.Port
		signature.Outputs[index] = SignaturePort{Name: port.Name, Type: port.Type, Optional: port.Optional}
	}
	return signature, nil
}

func (signature PublicSignature) Equal(other PublicSignature) bool {
	if len(signature.Inputs) != len(other.Inputs) || len(signature.Outputs) != len(other.Outputs) {
		return false
	}
	for index := range signature.Inputs {
		if signature.Inputs[index] != other.Inputs[index] {
			return false
		}
	}
	for index := range signature.Outputs {
		if signature.Outputs[index] != other.Outputs[index] {
			return false
		}
	}
	return true
}

// Validate enforces the tagged-union invariant for values constructed in Go
// or decoded from the compiled-model representation.
func (definition CompiledDefinition) Validate() error {
	if strings.TrimSpace(definition.Name) == "" {
		return fmt.Errorf("workflow: name is required")
	}

	switch definition.SchemaVersion {
	case 1, 2:
		if definition.Legacy == nil || definition.Function != nil {
			return fmt.Errorf("workflow: schema_version %d requires exactly the legacy definition arm", definition.SchemaVersion)
		}
		if definition.Legacy.SchemaVersion != definition.SchemaVersion {
			return fmt.Errorf("workflow: schema_version %d does not match legacy schema_version %d", definition.SchemaVersion, definition.Legacy.SchemaVersion)
		}
		if definition.Legacy.Name != definition.Name {
			return fmt.Errorf("workflow: name %q does not match legacy name %q", definition.Name, definition.Legacy.Name)
		}
		if definition.Legacy.Description != definition.Description {
			return fmt.Errorf("workflow: description does not match the legacy definition arm")
		}
		return definition.Legacy.Validate()
	case 3:
		if definition.Legacy != nil || definition.Function == nil {
			return fmt.Errorf("workflow: schema_version 3 requires exactly the function definition arm")
		}
		return definition.Function.Validate()
	default:
		return fmt.Errorf("workflow: schema_version must be 1, 2, or 3, got %d", definition.SchemaVersion)
	}
}

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (contracts §6). ContentHash is
// hex(sha256(Manifest.Canonical())) — raw-YAML imports hash their
// single-file wrapping, so the scheme is uniform (design 2026-07-17 §3;
// pre-slice rows carry the legacy raw-bytes hash and re-mint one
// version on their next import).
type Definition struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	Version          int    `json:"version"`
	SchemaVersion    int    `json:"schema_version"`
	SignatureVersion int    `json:"signature_version"`
	ContentHash      string `json:"content_hash"`
	Live             bool   `json:"live"`
	Description      string `json:"description"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        int64  `json:"created_at"`

	// Compiled is the authoritative tagged representation. Config remains the
	// legacy compatibility accessor and is populated only for schema 1/2.
	Compiled CompiledDefinition `json:"compiled"`
	Config   Config             `json:"config"`

	// RawYAML is the stored workflow.yml bytes. Populated by Get and
	// Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`

	// SourceManifest is the imported source tree (path -> content), the
	// hashed provenance unit for manifest imports. Populated by Get and
	// Live (like RawYAML); empty in List/Versions. Nil for legacy rows
	// imported before the source-format slice.
	SourceManifest Manifest `json:"source_manifest,omitempty"`
}

type VersionMetadata struct {
	Version          int `json:"version"`
	SchemaVersion    int `json:"schema_version"`
	SignatureVersion int `json:"signature_version"`
}

func (definition Definition) VersionMetadata() VersionMetadata {
	return VersionMetadata{
		Version:          definition.Version,
		SchemaVersion:    definition.SchemaVersion,
		SignatureVersion: definition.SignatureVersion,
	}
}

type PromotionResult struct {
	PreviousLive     *VersionMetadata `json:"previous_live,omitempty"`
	Target           VersionMetadata  `json:"target"`
	SignatureChanged bool             `json:"signature_changed"`
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
	Promote(name string, version int, promotedBy string) (PromotionResult, error) // atomically swaps the live flag
}
