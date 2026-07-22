// Package snapshot defines immutable, typed snapshot contracts. It deliberately
// depends only on the Go standard library so the semantic model can sit above
// ATC runtime and database implementations.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var typeRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*/v[1-9][0-9]*$`)

// TypeRef is a versioned, server-validated snapshot contract name.
type TypeRef string

// ParseTypeRef validates raw and returns its canonical form.
func ParseTypeRef(raw string) (TypeRef, error) {
	ref := TypeRef(raw)
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return ref, nil
}

// Validate ensures the reference is <name>/v<positive integer>.
func (r TypeRef) Validate() error {
	if !typeRefPattern.MatchString(string(r)) {
		return fmt.Errorf("snapshot: invalid type reference %q", r)
	}
	return nil
}

// String returns the canonical serialized type reference.
func (r TypeRef) String() string { return string(r) }

// Port is one ordered input or output in a workflow signature.
type Port struct {
	Name        string  `json:"name" yaml:"name"`
	Type        TypeRef `json:"type" yaml:"type"`
	Optional    bool    `json:"optional,omitempty" yaml:"optional,omitempty"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
}

// Validate checks that a port has a name and an authoritative type reference.
func (p Port) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("snapshot: port name is required")
	}
	if err := p.Type.Validate(); err != nil {
		return fmt.Errorf("snapshot: port %q: %w", p.Name, err)
	}
	return nil
}

// ValidatePorts checks the ordered signature ports without changing their order.
func ValidatePorts(ports []Port) error {
	seen := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if err := port.Validate(); err != nil {
			return err
		}
		if _, found := seen[port.Name]; found {
			return fmt.Errorf("snapshot: duplicate port %q", port.Name)
		}
		seen[port.Name] = struct{}{}
	}
	return nil
}

// SnapshotID is a positive signed 64-bit agent_snapshots primary key.
// Its JSON representation is always a quoted canonical decimal string.
type SnapshotID int64

func (id SnapshotID) Validate() error { return validatePositiveID(int64(id), "snapshot ID") }
func (id SnapshotID) String() string  { return strconv.FormatInt(int64(id), 10) }

func (id SnapshotID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *SnapshotID) UnmarshalJSON(data []byte) error {
	value, err := parseQuotedPositiveID(data, "snapshot ID")
	if err != nil {
		return err
	}
	*id = SnapshotID(value)
	return nil
}

// WorkflowRunID is a positive signed 64-bit agent_workflow_runs primary key.
// Its JSON representation is always a quoted canonical decimal string.
type WorkflowRunID int64

func (id WorkflowRunID) Validate() error { return validatePositiveID(int64(id), "workflow run ID") }
func (id WorkflowRunID) String() string  { return strconv.FormatInt(int64(id), 10) }

func (id WorkflowRunID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *WorkflowRunID) UnmarshalJSON(data []byte) error {
	value, err := parseQuotedPositiveID(data, "workflow run ID")
	if err != nil {
		return err
	}
	*id = WorkflowRunID(value)
	return nil
}

func validatePositiveID(value int64, label string) error {
	if value <= 0 {
		return fmt.Errorf("snapshot: %s must be positive", label)
	}
	return nil
}

func parseQuotedPositiveID(data []byte, label string) (int64, error) {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, fmt.Errorf("snapshot: %s must be a quoted decimal string: %w", label, err)
	}
	if raw == "" || raw[0] == '+' || raw[0] == '-' || (len(raw) > 1 && raw[0] == '0') {
		return 0, fmt.Errorf("snapshot: %s must be a canonical positive decimal string", label)
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("snapshot: %s must be a canonical positive decimal string", label)
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("snapshot: %s must be a canonical positive decimal string", label)
	}
	return value, nil
}

type ContentState string

const (
	ContentStateAvailable ContentState = "available"
	ContentStateExpired   ContentState = "expired"
)

// Snapshot is the immutable semantic value deduplicated by type and digest.
// Productions, grants, and retention claims are separate histories.
type Snapshot struct {
	ID                SnapshotID      `json:"id"`
	Type              TypeRef         `json:"type"`
	Digest            string          `json:"digest"`
	ByteSize          int64           `json:"byte_size"`
	FileCount         int64           `json:"file_count"`
	Representation    string          `json:"representation"`
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
	ContentState      ContentState    `json:"content_state"`
	CreatedAt         time.Time       `json:"created_at"`
}

// Location identifies one immutable replica of snapshot content.
type Location struct {
	SnapshotID SnapshotID `json:"snapshot_id,omitempty"`
	Digest     string     `json:"digest"`
	Driver     string     `json:"driver"`
	Key        string     `json:"key"`
	Node       string     `json:"node,omitempty"`
}

// Production is one invocation's provenance for a snapshot value.
type Production struct {
	ID                   int64           `json:"id"`
	SnapshotID           SnapshotID      `json:"snapshot_id"`
	BuildID              int             `json:"build_id"`
	TeamID               int             `json:"team_id"`
	TeamName             string          `json:"team_name"`
	CreatedBy            string          `json:"created_by"`
	PlanID               string          `json:"plan_id"`
	Attempt              string          `json:"attempt"`
	StepKind             string          `json:"step_kind"`
	StepName             string          `json:"step_name"`
	OutputPort           string          `json:"output_port"`
	WorkflowDefinitionID *int            `json:"workflow_definition_id,omitempty"`
	WorkflowRunID        *WorkflowRunID  `json:"workflow_run_id,omitempty"`
	SourceMetadata       json.RawMessage `json:"source_metadata,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
}

// Grant authorizes a team to read a snapshot. A grant never retains bytes.
type Grant struct {
	ID         int64      `json:"id"`
	SnapshotID SnapshotID `json:"snapshot_id"`
	TeamID     int        `json:"team_id"`
	GrantedBy  string     `json:"granted_by"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
}

// RetentionClass categorizes a durable reference to snapshot content.
// Grants remain authorization records and are deliberately not a class.
type RetentionClass string

const (
	RetentionClassBinding  RetentionClass = "binding"
	RetentionClassWorkflow RetentionClass = "workflow"
	RetentionClassFixture  RetentionClass = "fixture"
	RetentionClassPin      RetentionClass = "pin"
)

// RetentionClaim independently keeps content available. Removing one claim
// must not weaken any other actor or binding's claim.
type RetentionClaim struct {
	ID         int64          `json:"id"`
	SnapshotID SnapshotID     `json:"snapshot_id"`
	Class      RetentionClass `json:"class"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Actor      string         `json:"actor,omitempty"`
	Reason     string         `json:"reason"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Active reports whether the claim currently retains content.
func (c RetentionClaim) Active(now time.Time) bool {
	return c.ExpiresAt == nil || c.ExpiresAt.After(now)
}

// SortRetentionClaims orders claims stably by effective strength and durable
// identity. It is used only for deterministic selection/display; all active
// claims remain independently effective.
func SortRetentionClaims(claims []RetentionClaim) {
	sort.SliceStable(claims, func(i, j int) bool {
		left, right := claims[i], claims[j]
		if retentionClassRank(left.Class) != retentionClassRank(right.Class) {
			return retentionClassRank(left.Class) < retentionClassRank(right.Class)
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		if left.Actor != right.Actor {
			return left.Actor < right.Actor
		}
		return left.ID < right.ID
	})
}

// EffectiveRetentionClaim returns the strongest active claim, or nil when no
// retention claim remains. It never considers grants.
func EffectiveRetentionClaim(claims []RetentionClaim, now time.Time) *RetentionClaim {
	active := make([]RetentionClaim, 0, len(claims))
	for _, claim := range claims {
		if claim.Active(now) {
			active = append(active, claim)
		}
	}
	if len(active) == 0 {
		return nil
	}
	SortRetentionClaims(active)
	return &active[0]
}

func retentionClassRank(class RetentionClass) int {
	switch class {
	case RetentionClassBinding:
		return 0
	case RetentionClassWorkflow:
		return 1
	case RetentionClassFixture:
		return 2
	case RetentionClassPin:
		return 3
	default:
		return 4
	}
}

// LineageEdge records the input snapshot consumed at one input port by one
// production. InputPort preserves the declared source order at query time.
type LineageEdge struct {
	ProductionID    int64      `json:"production_id"`
	InputPort       string     `json:"input_port"`
	InputSnapshotID SnapshotID `json:"input_snapshot_id"`
}

// SnapshotRef is the typed immutable reference carried between workflow nodes.
type SnapshotRef struct {
	ID     SnapshotID `json:"id"`
	Type   TypeRef    `json:"type"`
	Digest string     `json:"digest"`
}

// CandidateOutput is a validated output candidate before a seal batch commits.
// ArchivePath is private spool storage; it is never a published artifact.
type CandidateOutput struct {
	Port              Port            `json:"port"`
	ArchivePath       string          `json:"-"`
	Digest            string          `json:"digest"`
	ByteSize          int64           `json:"byte_size"`
	FileCount         int64           `json:"file_count"`
	Representation    string          `json:"representation"`
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
}

// SealedOutput is an output that became visible only through CommitSealBatch.
type SealedOutput struct {
	Port     Port        `json:"port"`
	Snapshot SnapshotRef `json:"snapshot"`
}

// SealRequest is the complete, immutable invocation context for one output
// seal batch. No partial candidate set may be exposed.
type SealRequest struct {
	BuildID              int                    `json:"build_id"`
	TeamID               int                    `json:"team_id"`
	TeamName             string                 `json:"team_name"`
	CreatedBy            string                 `json:"created_by"`
	PlanID               string                 `json:"plan_id"`
	Attempt              string                 `json:"attempt"`
	StepKind             string                 `json:"step_kind"`
	StepName             string                 `json:"step_name"`
	WorkflowDefinitionID *int                   `json:"workflow_definition_id,omitempty"`
	WorkflowRunID        *WorkflowRunID         `json:"workflow_run_id,omitempty"`
	Inputs               map[string]SnapshotRef `json:"inputs"`
	Outputs              []CandidateOutput      `json:"outputs"`
}

// OutputSealer validates and atomically seals every requested output.
type OutputSealer interface {
	Seal(context.Context, SealRequest) (map[string]SealedOutput, error)
}
