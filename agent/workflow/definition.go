package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// CompiledDefinition is the parsed representation of a schema-version-3
// workflow function.
type CompiledDefinition struct {
	SchemaVersion int             `json:"schema_version" yaml:"schema_version"`
	Name          string          `json:"name" yaml:"name"`
	Description   string          `json:"description,omitempty" yaml:"description,omitempty"`
	Function      *FunctionConfig `json:"function,omitempty" yaml:"function,omitempty"`
}

// VersionMetadata derives the durable schema/signature identity from the
// validated compiled definition. Callers must never accept these values from an
// import request independently of the compiled source.
func (definition CompiledDefinition) VersionMetadata() (VersionMetadata, error) {
	if err := definition.Validate(); err != nil {
		return VersionMetadata{}, err
	}
	metadata := VersionMetadata{
		SchemaVersion:    definition.SchemaVersion,
		SignatureVersion: definition.Function.SignatureVersion,
	}
	if metadata.SignatureVersion <= 0 {
		return VersionMetadata{}, fmt.Errorf("workflow: schema_version 3 requires a positive signature_version")
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
	if _, err := definition.VersionMetadata(); err != nil {
		return PublicSignature{}, err
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

// Validate enforces the v3 model invariant for values constructed in Go or
// decoded from the compiled-model representation.
func (definition CompiledDefinition) Validate() error {
	if definition.SchemaVersion != 3 {
		return fmt.Errorf("workflow: schema_version must be 3, got %d", definition.SchemaVersion)
	}
	if strings.TrimSpace(definition.Name) == "" {
		return fmt.Errorf("workflow: name is required")
	}
	if definition.Function == nil {
		return fmt.Errorf("workflow: schema_version 3 requires a function definition")
	}
	return definition.Function.Validate()
}

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (contracts §6). ContentHash is
// hex(sha256(Manifest.Canonical())) — raw-YAML imports hash their
// single-file wrapping, so the scheme is uniform (design 2026-07-17 §3).
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

	// Compiled is the authoritative schema-version-3 representation.
	Compiled CompiledDefinition `json:"compiled"`

	// Hidden/Annotation are workflow-NAME-level lifecycle metadata (S-6),
	// stored in agent_workflow_lifecycle and joined onto every version row on
	// read. Hidden deprecates a workflow from default listings; Annotation is
	// a human operator note distinct from the per-version YAML Description.
	// They are name-scoped, so they are NOT part of the version's content hash
	// or its public signature.
	Hidden     bool   `json:"hidden"`
	Annotation string `json:"annotation,omitempty"`

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

// ErrPromotionValidatorRequired prevents schema-v3 definitions from becoming
// live unless the caller supplied the trusted renderer used by workflow-run
// binding. Import intentionally remains more permissive so authors can inspect
// and iterate on definitions which are not executable yet.
var ErrPromotionValidatorRequired = errors.New("workflow: schema-v3 promotion requires an authoritative target validator")

// InvalidPromotionError identifies a stored version which exists but cannot
// become a runnable live target. Stores must return this before changing the
// current live version.
type InvalidPromotionError struct{ Err error }

func (e InvalidPromotionError) Error() string {
	return fmt.Sprintf("workflow: version is not runnable: %v", e.Err)
}

func (e InvalidPromotionError) Unwrap() error { return e.Err }

// PromotionValidator is implemented by the trusted workflow-run renderer.
// Validation is invoked by Store.Promote while the store's per-workflow
// serialization lock is held, immediately before the atomic live swap.
type PromotionValidator interface {
	ValidatePromotion(Definition) error
}

const (
	DefaultVersionPageSize = 50
	MaxVersionPageSize     = 100
	MaxWorkflowVersion     = 2_147_483_647
)

var ErrInvalidVersionPage = errors.New("workflow: invalid version page")

// VersionPageRequest is a stable keyset cursor over immutable workflow
// version numbers. Cursor is exclusive: a non-zero value requests versions
// older than that version. Limit is always explicit so no Store
// implementation can accidentally perform an unbounded history read.
type VersionPageRequest struct {
	Cursor int
	Limit  int
}

type VersionPage struct {
	Definitions []Definition
	NextCursor  int
	Found       bool
}

// InvalidDefinitionError wraps admission, parse, validation, and name-mismatch
// failures. API handlers map ordinary failures to 400 and unsupported schema
// versions to their stable typed 422 response.
type InvalidDefinitionError struct{ Err error }

func (e InvalidDefinitionError) Error() string { return e.Err.Error() }
func (e InvalidDefinitionError) Unwrap() error { return e.Err }

type UnsupportedSchemaVersionError struct {
	Got int
}

func (e UnsupportedSchemaVersionError) Error() string {
	return fmt.Sprintf(
		"workflow: unsupported schema_version %d; only schema_version 3 is supported",
		e.Got,
	)
}

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
	Versions(context.Context, string, VersionPageRequest) (VersionPage, error)
	// Promote validates schema-v3 definitions with the store's authoritative
	// PromotionValidator and atomically swaps the live flag. Validation and
	// the swap are serialized with imports and other promotions for name.
	Promote(name string, version int, promotedBy string) (PromotionResult, error)
	// Annotate sets the workflow's operator note (name-level). Returns
	// ErrVersionNotFound if no version of name exists.
	Annotate(name, annotation, updatedBy string) error
	// SetHidden deprecates (hidden=true) or restores (false) a workflow from
	// default listings. Returns ErrVersionNotFound if no version exists.
	SetHidden(name string, hidden bool, updatedBy string) error
}
