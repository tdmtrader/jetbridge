// Package snapshot defines typed snapshot contracts without depending on ATC,
// a database, or a storage implementation.
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var typeRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*/v[1-9][0-9]*$`)

// ErrInternalOnly marks process-local values that deliberately have no JSON
// representation because they contain private filesystem paths.
var ErrInternalOnly = errors.New("snapshot: internal-only value has no JSON representation")

// Stable domain categories let transports map failures without matching error
// strings or disclosing storage/database details.
var (
	ErrInvalidArchive     = errors.New("snapshot: invalid archive")
	ErrLimitExceeded      = errors.New("snapshot: limit exceeded")
	ErrUnsupportedType    = errors.New("snapshot: unsupported type")
	ErrValidation         = errors.New("snapshot: validation failed")
	ErrConflict           = errors.New("snapshot: conflict")
	ErrNotFound           = errors.New("snapshot: not found")
	ErrExpired            = errors.New("snapshot: expired")
	ErrContentUnavailable = errors.New("snapshot: content unavailable")
)

// TypeRef is a versioned, server-validated snapshot contract name.
type TypeRef string

func ParseTypeRef(raw string) (TypeRef, error) {
	ref := TypeRef(raw)
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return ref, nil
}

func (r TypeRef) Validate() error {
	if !typeRefPattern.MatchString(string(r)) {
		return fmt.Errorf("snapshot: invalid type reference %q", r)
	}
	return nil
}

func (r TypeRef) String() string { return string(r) }

func (r TypeRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(r))
}

func (r *TypeRef) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("snapshot: type reference must be a JSON string: %w", err)
	}
	parsed, err := ParseTypeRef(raw)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// Digest is the canonical physical content identity.
type Digest string

func ParseDigest(raw string) (Digest, error) {
	digest := Digest(raw)
	if err := digest.Validate(); err != nil {
		return "", err
	}
	return digest, nil
}

func (d Digest) Validate() error {
	const prefix = "sha256:"
	raw := string(d)
	if len(raw) != len(prefix)+64 || !strings.HasPrefix(raw, prefix) {
		return fmt.Errorf("snapshot: digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	for _, c := range raw[len(prefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("snapshot: digest must be sha256 followed by 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func (d Digest) String() string { return string(d) }

func (d Digest) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(d))
}

func (d *Digest) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("snapshot: digest must be a JSON string: %w", err)
	}
	parsed, err := ParseDigest(raw)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Port is one ordered input or output in a workflow signature.
type Port struct {
	Name        string  `json:"name" yaml:"name"`
	Type        TypeRef `json:"type" yaml:"type"`
	Optional    bool    `json:"optional,omitempty" yaml:"optional,omitempty"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
}

func (p Port) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("snapshot: port name is required")
	}
	if err := p.Type.Validate(); err != nil {
		return fmt.Errorf("snapshot: port %q: %w", p.Name, err)
	}
	return nil
}

func (p Port) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire Port
	return json.Marshal(wire(p))
}

func (p *Port) UnmarshalJSON(data []byte) error {
	type wire Port
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := Port(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*p = parsed
	return nil
}

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

// DatabaseID is a positive signed 64-bit identity for durable records that do
// not have a more specific domain ID type. Its JSON representation is always
// a quoted canonical decimal so browser clients never lose precision.
type DatabaseID int64

func (id DatabaseID) Validate() error { return validatePositiveID(int64(id), "database ID") }

func (id DatabaseID) String() string {
	if id.Validate() != nil {
		return ""
	}
	return strconv.FormatInt(int64(id), 10)
}

func (id DatabaseID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return []byte(`"` + strconv.FormatInt(int64(id), 10) + `"`), nil
}

func (id *DatabaseID) UnmarshalJSON(data []byte) error {
	value, err := parseRawQuotedPositiveID(data, "database ID")
	if err != nil {
		return err
	}
	*id = DatabaseID(value)
	return nil
}

// SnapshotID is a positive signed 64-bit agent_snapshots primary key.
// Its JSON representation is always an unescaped quoted canonical decimal.
type SnapshotID int64

func NewSnapshotID(value int64) (SnapshotID, error) {
	id := SnapshotID(value)
	if err := id.Validate(); err != nil {
		return 0, err
	}
	return id, nil
}

func ParseSnapshotID(raw string) (SnapshotID, error) {
	value, err := parsePositiveID(raw, "snapshot ID")
	return SnapshotID(value), err
}

func (id SnapshotID) Validate() error { return validatePositiveID(int64(id), "snapshot ID") }

// String returns an empty string for an invalid ID. Use TemplateValue when an
// invalid value must be reported to a caller rather than silently suppressed.
func (id SnapshotID) String() string {
	if id.Validate() != nil {
		return ""
	}
	return strconv.FormatInt(int64(id), 10)
}

func (id SnapshotID) TemplateValue() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(id), 10), nil
}

func (id SnapshotID) MarshalJSON() ([]byte, error) {
	value, err := id.TemplateValue()
	if err != nil {
		return nil, err
	}
	return []byte(`"` + value + `"`), nil
}

func (id SnapshotID) MarshalText() ([]byte, error) {
	value, err := id.TemplateValue()
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

func (id *SnapshotID) UnmarshalText(data []byte) error {
	parsed, err := ParseSnapshotID(string(data))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id *SnapshotID) UnmarshalJSON(data []byte) error {
	value, err := parseRawQuotedPositiveID(data, "snapshot ID")
	if err != nil {
		return err
	}
	*id = SnapshotID(value)
	return nil
}

// WorkflowRunID is a positive signed 64-bit agent_workflow_runs primary key.
type WorkflowRunID int64

func ParseWorkflowRunID(raw string) (WorkflowRunID, error) {
	value, err := parsePositiveID(raw, "workflow run ID")
	return WorkflowRunID(value), err
}

func (id WorkflowRunID) Validate() error {
	return validatePositiveID(int64(id), "workflow run ID")
}

func (id WorkflowRunID) String() string {
	if id.Validate() != nil {
		return ""
	}
	return strconv.FormatInt(int64(id), 10)
}

func (id WorkflowRunID) TemplateValue() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(id), 10), nil
}

func (id WorkflowRunID) MarshalJSON() ([]byte, error) {
	value, err := id.TemplateValue()
	if err != nil {
		return nil, err
	}
	return []byte(`"` + value + `"`), nil
}

func (id WorkflowRunID) MarshalText() ([]byte, error) {
	value, err := id.TemplateValue()
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

func (id *WorkflowRunID) UnmarshalText(data []byte) error {
	parsed, err := ParseWorkflowRunID(string(data))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id *WorkflowRunID) UnmarshalJSON(data []byte) error {
	value, err := parseRawQuotedPositiveID(data, "workflow run ID")
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

func parsePositiveID(raw string, label string) (int64, error) {
	if raw == "" || raw[0] == '0' {
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

func parseRawQuotedPositiveID(data []byte, label string) (int64, error) {
	if len(data) < 3 || data[0] != '"' || data[len(data)-1] != '"' {
		return 0, fmt.Errorf("snapshot: %s must be an unescaped quoted canonical positive decimal string", label)
	}
	return parsePositiveID(string(data[1:len(data)-1]), label)
}

type ContentState string

const (
	ContentStateAvailable ContentState = "available"
	ContentStateExpired   ContentState = "expired"
)

func (s ContentState) Validate() error {
	switch s {
	case ContentStateAvailable, ContentStateExpired:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid content state %q", s)
	}
}

func (s ContentState) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s))
}

func (s *ContentState) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed := ContentState(raw)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*s = parsed
	return nil
}

// Snapshot is a persisted semantic value. Multiple snapshot types may share
// one physical Digest; lifecycle operations therefore aggregate by Digest.
type Snapshot struct {
	ID                SnapshotID      `json:"id"`
	Type              TypeRef         `json:"type"`
	Digest            Digest          `json:"digest"`
	ByteSize          int64           `json:"byte_size"`
	FileCount         int64           `json:"file_count"`
	Representation    string          `json:"representation"`
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
	ContentState      ContentState    `json:"content_state"`
	CreatedAt         time.Time       `json:"created_at"`
}

func (s Snapshot) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if err := s.Type.Validate(); err != nil {
		return err
	}
	if err := s.Digest.Validate(); err != nil {
		return err
	}
	if s.ByteSize < 0 || s.FileCount < 0 {
		return fmt.Errorf("snapshot: byte and file counts must not be negative")
	}
	if strings.TrimSpace(s.Representation) == "" {
		return fmt.Errorf("snapshot: representation is required")
	}
	if err := validateRawMessage(s.IntrinsicMetadata); err != nil {
		return fmt.Errorf("snapshot: intrinsic metadata: %w", err)
	}
	if err := s.ContentState.Validate(); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: creation time is required")
	}
	return nil
}

func (s Snapshot) Clone() Snapshot {
	s.IntrinsicMetadata = cloneRaw(s.IntrinsicMetadata)
	return s
}

func (s Snapshot) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type wire Snapshot
	return json.Marshal(wire(s))
}

func (s *Snapshot) UnmarshalJSON(data []byte) error {
	type wire Snapshot
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := Snapshot(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*s = parsed.Clone()
	return nil
}

// Location identifies physical content and deliberately has no SnapshotID:
// physical identity is the digest, while snapshots are (type,digest) values.
type Location struct {
	Digest Digest `json:"digest"`
	Driver string `json:"driver"`
	Key    string `json:"key"`
	Node   string `json:"node,omitempty"`
}

func (l Location) Validate() error {
	if err := l.Digest.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(l.Driver) == "" || strings.TrimSpace(l.Key) == "" {
		return fmt.Errorf("snapshot: location driver and key are required")
	}
	return nil
}

func (l Location) MarshalJSON() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	type wire Location
	return json.Marshal(wire(l))
}

func (l *Location) UnmarshalJSON(data []byte) error {
	type wire Location
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := Location(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*l = parsed
	return nil
}

type RetentionClass string

const (
	RetentionClassBinding  RetentionClass = "binding"
	RetentionClassRun      RetentionClass = "run"
	RetentionClassWorkflow RetentionClass = "workflow"
	RetentionClassFixture  RetentionClass = "fixture"
	RetentionClassPin      RetentionClass = "pin"
)

func (c RetentionClass) Validate() error {
	switch c {
	case RetentionClassBinding, RetentionClassRun, RetentionClassWorkflow, RetentionClassFixture, RetentionClassPin:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid retention class %q", c)
	}
}

func (c RetentionClass) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(c))
}

func (c *RetentionClass) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed := RetentionClass(raw)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*c = parsed
	return nil
}

// RetentionClaim is a persisted independent content-retention record.
type RetentionClaim struct {
	ID            DatabaseID     `json:"id"`
	SnapshotID    SnapshotID     `json:"snapshot_id"`
	Class         RetentionClass `json:"class"`
	WorkflowRunID *WorkflowRunID `json:"workflow_run_id,omitempty"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	Actor         string         `json:"actor,omitempty"`
	Reason        string         `json:"reason"`
	CreatedAt     time.Time      `json:"created_at"`
}

func (c RetentionClaim) Validate() error {
	if c.ID <= 0 {
		return fmt.Errorf("snapshot: retention claim ID must be positive")
	}
	if err := c.SnapshotID.Validate(); err != nil {
		return err
	}
	if err := c.Class.Validate(); err != nil {
		return err
	}
	if (c.Class == RetentionClassRun) != (c.WorkflowRunID != nil) {
		return fmt.Errorf("snapshot: run retention requires exactly one workflow run ID")
	}
	if c.WorkflowRunID != nil {
		if err := c.WorkflowRunID.Validate(); err != nil {
			return err
		}
		if c.ExpiresAt != nil {
			return fmt.Errorf("snapshot: run retention cannot expire while its workflow run is active")
		}
	}
	if strings.TrimSpace(c.Reason) == "" || c.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: retention reason and creation time are required")
	}
	return nil
}

func (c RetentionClaim) Clone() RetentionClaim {
	c.WorkflowRunID = cloneWorkflowRunID(c.WorkflowRunID)
	c.ExpiresAt = cloneTime(c.ExpiresAt)
	return c
}

func (c RetentionClaim) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	type wire RetentionClaim
	return json.Marshal(wire(c))
}

func (c *RetentionClaim) UnmarshalJSON(data []byte) error {
	type wire RetentionClaim
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := RetentionClaim(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*c = parsed.Clone()
	return nil
}

// Active is false for unknown classes and for expiry equal to now.
func (c RetentionClaim) Active(now time.Time) bool {
	return c.Class.Validate() == nil && (c.ExpiresAt == nil || c.ExpiresAt.After(now))
}

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

func retentionClassRank(class RetentionClass) int {
	switch class {
	case RetentionClassBinding:
		return 0
	case RetentionClassRun:
		return 1
	case RetentionClassWorkflow:
		return 2
	case RetentionClassFixture:
		return 3
	case RetentionClassPin:
		return 4
	default:
		return 5
	}
}

type SnapshotRef struct {
	ID     SnapshotID `json:"id"`
	Type   TypeRef    `json:"type"`
	Digest Digest     `json:"digest"`
}

func (r SnapshotRef) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.Type.Validate(); err != nil {
		return err
	}
	return r.Digest.Validate()
}

func (r SnapshotRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire SnapshotRef
	return json.Marshal(wire(r))
}

func (r *SnapshotRef) UnmarshalJSON(data []byte) error {
	type wire SnapshotRef
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SnapshotRef(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*r = parsed
	return nil
}

type CandidateOutput struct {
	Port              Port            `json:"port"`
	ArchivePath       string          `json:"-"`
	Digest            Digest          `json:"digest"`
	ByteSize          int64           `json:"byte_size"`
	FileCount         int64           `json:"file_count"`
	Representation    string          `json:"representation"`
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
}

// OutputSource is the process-local description of one output mount offered
// to the sealing boundary. OpenTar must return a fresh, uncompressed tar
// stream on every call; it and the source policy are deliberately never
// serialized or persisted as part of the snapshot value.
type OutputSource struct {
	ClientKey      string
	Port           Port
	OpenTar        func(context.Context) (io.ReadCloser, error)
	Retention      RetentionClass
	WorkflowPort   string
	SourceMetadata json.RawMessage
}

func (s OutputSource) Validate() error {
	if strings.TrimSpace(s.ClientKey) == "" {
		return fmt.Errorf("snapshot: output source client key is required")
	}
	if err := s.Port.Validate(); err != nil {
		return err
	}
	if s.OpenTar == nil {
		return fmt.Errorf("snapshot: output source %q tar opener is required", s.ClientKey)
	}
	if err := validateRawMessage(s.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: output source %q metadata: %w", s.ClientKey, err)
	}
	switch s.Retention {
	case "", RetentionClassBinding:
		if s.WorkflowPort != "" {
			return fmt.Errorf("snapshot: output source %q workflow port requires workflow retention", s.ClientKey)
		}
	case RetentionClassWorkflow:
		if strings.TrimSpace(s.WorkflowPort) == "" {
			return fmt.Errorf("snapshot: output source %q workflow retention requires a workflow port", s.ClientKey)
		}
	case RetentionClassFixture, RetentionClassPin:
		return fmt.Errorf("snapshot: output source %q cannot claim producer retention %q", s.ClientKey, s.Retention)
	default:
		return fmt.Errorf("snapshot: output source %q has unsupported producer retention %q", s.ClientKey, s.Retention)
	}
	return nil
}

func (s OutputSource) Clone() OutputSource {
	s.SourceMetadata = cloneRaw(s.SourceMetadata)
	return s
}

func (o CandidateOutput) Validate() error {
	if err := o.Port.Validate(); err != nil {
		return err
	}
	if err := o.Digest.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(o.ArchivePath) == "" {
		return fmt.Errorf("snapshot: candidate archive path is required")
	}
	if o.ByteSize < 0 || o.FileCount < 0 {
		return fmt.Errorf("snapshot: candidate byte and file counts must not be negative")
	}
	if strings.TrimSpace(o.Representation) == "" {
		return fmt.Errorf("snapshot: candidate representation is required")
	}
	return validateRawMessage(o.IntrinsicMetadata)
}

func (o CandidateOutput) Clone() CandidateOutput {
	o.IntrinsicMetadata = cloneRaw(o.IntrinsicMetadata)
	return o
}

func (o CandidateOutput) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("%w: CandidateOutput", ErrInternalOnly)
}

func (o *CandidateOutput) UnmarshalJSON(data []byte) error {
	return fmt.Errorf("%w: CandidateOutput", ErrInternalOnly)
}

type SealedOutput struct {
	Port     Port        `json:"port"`
	Snapshot SnapshotRef `json:"snapshot"`
}

func (o SealedOutput) Validate() error {
	if err := o.Port.Validate(); err != nil {
		return err
	}
	if err := o.Snapshot.Validate(); err != nil {
		return err
	}
	if o.Port.Type != o.Snapshot.Type {
		return fmt.Errorf("snapshot: sealed output port and snapshot types differ")
	}
	return nil
}

func (o SealedOutput) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wire SealedOutput
	return json.Marshal(wire(o))
}

func (o *SealedOutput) UnmarshalJSON(data []byte) error {
	type wire SealedOutput
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SealedOutput(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*o = parsed
	return nil
}

// BuildOccurrence is the immutable identity and provenance of one build step
// which produced a snapshot.
type BuildOccurrence struct {
	BuildID              int            `json:"build_id"`
	PlanID               string         `json:"plan_id"`
	Attempt              string         `json:"attempt"`
	StepKind             string         `json:"step_kind"`
	StepName             string         `json:"step_name"`
	WorkflowDefinitionID *int           `json:"workflow_definition_id,omitempty"`
	WorkflowRunID        *WorkflowRunID `json:"workflow_run_id,omitempty"`
	WorkflowName         string         `json:"workflow_name,omitempty"`
}

func (o BuildOccurrence) Validate() error {
	if o.BuildID <= 0 {
		return fmt.Errorf("snapshot: build ID must be positive")
	}
	for label, value := range map[string]string{
		"plan ID": o.PlanID, "attempt": o.Attempt, "step kind": o.StepKind, "step name": o.StepName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot: %s is required", label)
		}
	}
	if o.WorkflowDefinitionID != nil && *o.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("snapshot: workflow definition ID must be positive")
	}
	if o.WorkflowRunID != nil {
		if err := o.WorkflowRunID.Validate(); err != nil {
			return err
		}
	}
	if (o.WorkflowDefinitionID == nil) != (o.WorkflowRunID == nil) {
		return fmt.Errorf("snapshot: workflow definition and run IDs must be provided together")
	}
	if o.WorkflowName != "" {
		if strings.TrimSpace(o.WorkflowName) != o.WorkflowName || o.WorkflowDefinitionID == nil || o.WorkflowRunID == nil {
			return fmt.Errorf("snapshot: workflow name requires workflow definition and run IDs")
		}
	}
	return nil
}

func (o BuildOccurrence) Clone() BuildOccurrence {
	o.WorkflowDefinitionID = cloneInt(o.WorkflowDefinitionID)
	o.WorkflowRunID = cloneWorkflowRunID(o.WorkflowRunID)
	return o
}

// UploadOccurrence identifies an authenticated manual upload independently
// of build/plan provenance. The key is private persistence state and must not
// be exposed from authorization-filtered detail DTOs.
type UploadOccurrence struct {
	IdempotencyKey string `json:"idempotency_key"`
}

func (o UploadOccurrence) Validate() error {
	if strings.TrimSpace(o.IdempotencyKey) == "" {
		return fmt.Errorf("snapshot: upload idempotency key is required")
	}
	if len(o.IdempotencyKey) > 200 {
		return fmt.Errorf("snapshot: upload idempotency key must be at most 200 bytes")
	}
	return nil
}

// SealCommitContext is the pre-persistence occurrence and ordered lineage
// context. Exactly one occurrence kind is required, and it deliberately
// contains no pre-upload CandidateOutput values.
type SealCommitContext struct {
	TeamID     int                    `json:"team_id"`
	TeamName   string                 `json:"team_name"`
	CreatedBy  string                 `json:"created_by"`
	Build      *BuildOccurrence       `json:"build,omitempty"`
	Upload     *UploadOccurrence      `json:"upload,omitempty"`
	InputOrder []string               `json:"input_order"`
	Inputs     map[string]SnapshotRef `json:"inputs"`
	// InputExposures is the mount-time exposure lineage the executor captured
	// for the exposed inputs, keyed by input port. It is occurrence data
	// persisted alongside lineage and outside the sealed bytes, so it never
	// takes part in content identity. An absent entry means the whole tree was
	// exposed; see Exposures.
	InputExposures  map[string]InputExposure `json:"input_exposures,omitempty"`
	ExpectedOutputs []Port                   `json:"expected_outputs"`
}

func (c SealCommitContext) Validate() error {
	if c.TeamID <= 0 {
		return fmt.Errorf("snapshot: team ID must be positive")
	}
	for label, value := range map[string]string{
		"team name": c.TeamName, "creator": c.CreatedBy,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot: %s is required", label)
		}
	}
	if (c.Build == nil) == (c.Upload == nil) {
		return fmt.Errorf("snapshot: exactly one build or upload occurrence is required")
	}
	if c.Build != nil {
		if err := c.Build.Validate(); err != nil {
			return err
		}
	}
	if c.Upload != nil {
		if err := c.Upload.Validate(); err != nil {
			return err
		}
		if len(c.InputOrder) != 0 || len(c.Inputs) != 0 {
			return fmt.Errorf("snapshot: upload occurrence cannot declare lineage inputs")
		}
		if len(c.ExpectedOutputs) != 1 {
			return fmt.Errorf("snapshot: upload occurrence must commit exactly one output")
		}
	}
	if len(c.InputOrder) != len(c.Inputs) {
		return fmt.Errorf("snapshot: input order must contain every input exactly once")
	}
	for name, ref := range c.Inputs {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("snapshot: input port name is required")
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("snapshot: input %q: %w", name, err)
		}
	}
	seenInputs := make(map[string]struct{}, len(c.Inputs))
	for _, name := range c.InputOrder {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("snapshot: input order port name is required")
		}
		if _, found := c.Inputs[name]; !found {
			return fmt.Errorf("snapshot: input order contains unknown port %q", name)
		}
		if _, duplicate := seenInputs[name]; duplicate {
			return fmt.Errorf("snapshot: input order contains duplicate port %q", name)
		}
		seenInputs[name] = struct{}{}
	}
	if err := validateDeclaredExposures(c.InputExposures, c.Inputs); err != nil {
		return err
	}
	return ValidatePorts(c.ExpectedOutputs)
}

// Exposures returns one exposure per exposed input, defaulting an undeclared
// input to full-tree exposure. Callers persist this, so the lineage is total:
// there is no exposed input whose materialization is unrecorded.
func (c SealCommitContext) Exposures() map[string]InputExposure {
	return resolveExposures(c.InputExposures, c.Inputs)
}

func (c SealCommitContext) Clone() SealCommitContext {
	if c.Build != nil {
		cloned := c.Build.Clone()
		c.Build = &cloned
	}
	if c.Upload != nil {
		cloned := *c.Upload
		c.Upload = &cloned
	}
	c.InputOrder = append([]string(nil), c.InputOrder...)
	c.Inputs = cloneSnapshotRefs(c.Inputs)
	c.InputExposures = cloneInputExposures(c.InputExposures)
	c.ExpectedOutputs = append([]Port(nil), c.ExpectedOutputs...)
	return c
}

// StageAttempt is the immutable occurrence key used to bind a durable stage
// to its eventual metadata commit.
func (c SealCommitContext) StageAttempt() string {
	if c.Build != nil {
		return c.Build.Attempt
	}
	if c.Upload != nil {
		return "upload:" + c.Upload.IdempotencyKey
	}
	return ""
}

func (c SealCommitContext) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	type wire SealCommitContext
	return json.Marshal(wire(c))
}

func (c *SealCommitContext) UnmarshalJSON(data []byte) error {
	type wire SealCommitContext
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SealCommitContext(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*c = parsed.Clone()
	return nil
}

// SealRequest is the frozen pre-upload invocation shape. InputOrder is the
// exact permutation used to persist lineage positions.
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
	InputOrder           []string               `json:"input_order"`
	Inputs               map[string]SnapshotRef `json:"inputs"`
	// InputExposures is the exposure lineage the executor captured at mount
	// time, keyed by input port: the materialization mode and the mount path.
	// It is occurrence data — it never enters the sealed bytes and never enters
	// ValidationResult.IntrinsicMetadata.
	InputExposures map[string]InputExposure `json:"input_exposures,omitempty"`
	// ValidationAttestationAuthorities is process-only, server-derived authority
	// keyed by output port. It is intentionally absent from every wire format.
	ValidationAttestationAuthorities map[string]ValidationAttestationAuthority `json:"-"`
	OutputDeclarations               []Port                                    `json:"output_declarations"`
	Outputs                          []OutputSource                            `json:"-"`
}

// UploadRequest is the process-local description of one authenticated manual
// upload. Authority fields are supplied by the server, never decoded from the
// archive or client JSON.
type UploadRequest struct {
	TeamID         int
	TeamName       string
	UploadedBy     string
	Actor          string
	IdempotencyKey string
	Type           TypeRef
	OpenTar        func(context.Context) (io.ReadCloser, error)
	SourceMetadata json.RawMessage
}

func (r UploadRequest) Validate() error {
	if r.TeamID <= 0 {
		return fmt.Errorf("snapshot: upload team ID must be positive")
	}
	for label, value := range map[string]string{
		"team name": r.TeamName, "uploader": r.UploadedBy, "actor": r.Actor,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot: upload %s is required", label)
		}
		if len(value) > 500 {
			return fmt.Errorf("snapshot: upload %s must be at most 500 bytes", label)
		}
	}
	if err := (UploadOccurrence{IdempotencyKey: r.IdempotencyKey}).Validate(); err != nil {
		return err
	}
	if err := r.Type.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedType, err)
	}
	if r.OpenTar == nil {
		return fmt.Errorf("snapshot: upload tar opener is required")
	}
	if err := validateRawMessage(r.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: upload source metadata: %w", err)
	}
	return nil
}

func (r UploadRequest) Clone() UploadRequest {
	r.SourceMetadata = cloneRaw(r.SourceMetadata)
	return r
}

func (r UploadRequest) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("%w: UploadRequest", ErrInternalOnly)
}

func (r *UploadRequest) UnmarshalJSON([]byte) error {
	return fmt.Errorf("%w: UploadRequest", ErrInternalOnly)
}

func (r SealRequest) Validate() error {
	if err := r.commitContext().Validate(); err != nil {
		return err
	}
	if len(r.OutputDeclarations) == 0 {
		return fmt.Errorf("snapshot: at least one output declaration is required")
	}
	if err := ValidatePorts(r.OutputDeclarations); err != nil {
		return err
	}
	declarations := make(map[string]Port, len(r.OutputDeclarations))
	for _, declaration := range r.OutputDeclarations {
		declarations[declaration.Name] = declaration
	}
	for output, authority := range r.ValidationAttestationAuthorities {
		if _, found := declarations[output]; !found {
			return fmt.Errorf("snapshot: validation attestation authority names undeclared output %q", output)
		}
		if err := authority.Validate(); err != nil {
			return fmt.Errorf("snapshot: output %q validation attestation authority: %w", output, err)
		}
		candidate, found := r.Inputs[authority.CandidateInput]
		if !found || candidate != authority.Candidate {
			return fmt.Errorf("snapshot: output %q validation attestation candidate is not an exact exposed input", output)
		}
		for _, base := range authority.BaseInputs {
			exposed, found := r.Inputs[base.Input]
			if !found || exposed != base.Ref {
				return fmt.Errorf("snapshot: output %q validation attestation base input %q is not an exact exposed input", output, base.Input)
			}
		}
	}
	produced := make(map[string]struct{}, len(r.Outputs))
	clientKeys := make(map[string]struct{}, len(r.Outputs))
	workflowPorts := make(map[string]struct{}, len(r.Outputs))
	for i, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("snapshot: output %d: %w", i, err)
		}
		declaration, found := declarations[output.Port.Name]
		if !found {
			return fmt.Errorf("snapshot: candidate output %q is undeclared", output.Port.Name)
		}
		if output.Port.Type != declaration.Type || output.Port.Optional != declaration.Optional {
			return fmt.Errorf("snapshot: candidate output %q does not match its declaration", output.Port.Name)
		}
		if _, duplicate := produced[output.Port.Name]; duplicate {
			return fmt.Errorf("snapshot: duplicate output source port %q", output.Port.Name)
		}
		produced[output.Port.Name] = struct{}{}
		if _, duplicate := clientKeys[output.ClientKey]; duplicate {
			return fmt.Errorf("snapshot: duplicate output source client key %q", output.ClientKey)
		}
		clientKeys[output.ClientKey] = struct{}{}
		if output.Retention == RetentionClassWorkflow && r.WorkflowRunID == nil {
			return fmt.Errorf("snapshot: output source %q workflow retention requires workflow definition and run IDs", output.ClientKey)
		}
		if output.WorkflowPort != "" {
			if _, duplicate := workflowPorts[output.WorkflowPort]; duplicate {
				return fmt.Errorf("snapshot: duplicate workflow port %q", output.WorkflowPort)
			}
			workflowPorts[output.WorkflowPort] = struct{}{}
		}
	}
	for _, declaration := range r.OutputDeclarations {
		if _, found := produced[declaration.Name]; !found && !declaration.Optional {
			return fmt.Errorf("snapshot: required output %q was not produced", declaration.Name)
		}
	}
	return nil
}

func (r SealRequest) commitContext() SealCommitContext {
	return SealCommitContext{
		TeamID: r.TeamID, TeamName: r.TeamName, CreatedBy: r.CreatedBy,
		Build: &BuildOccurrence{
			BuildID: r.BuildID, PlanID: r.PlanID, Attempt: r.Attempt,
			StepKind: r.StepKind, StepName: r.StepName,
			WorkflowDefinitionID: r.WorkflowDefinitionID, WorkflowRunID: r.WorkflowRunID,
		},
		InputOrder: r.InputOrder, Inputs: r.Inputs, InputExposures: r.InputExposures,
	}
}

func (r SealRequest) CommitContext() (SealCommitContext, error) {
	if err := r.Validate(); err != nil {
		return SealCommitContext{}, err
	}
	context := r.commitContext()
	context.ExpectedOutputs = make([]Port, len(r.Outputs))
	for i, output := range r.Outputs {
		context.ExpectedOutputs[i] = output.Port
	}
	return context.Clone(), nil
}

func (r SealRequest) Clone() SealRequest {
	r.WorkflowDefinitionID = cloneInt(r.WorkflowDefinitionID)
	r.WorkflowRunID = cloneWorkflowRunID(r.WorkflowRunID)
	r.InputOrder = append([]string(nil), r.InputOrder...)
	r.Inputs = cloneSnapshotRefs(r.Inputs)
	r.InputExposures = cloneInputExposures(r.InputExposures)
	if r.ValidationAttestationAuthorities != nil {
		authorities := make(map[string]ValidationAttestationAuthority, len(r.ValidationAttestationAuthorities))
		for output, authority := range r.ValidationAttestationAuthorities {
			authorities[output] = cloneValidationAttestationAuthority(authority)
		}
		r.ValidationAttestationAuthorities = authorities
	}
	r.OutputDeclarations = append([]Port(nil), r.OutputDeclarations...)
	if r.Outputs != nil {
		outputs := make([]OutputSource, len(r.Outputs))
		for i, output := range r.Outputs {
			outputs[i] = output.Clone()
		}
		r.Outputs = outputs
	}
	return r
}

func (r SealRequest) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("%w: SealRequest", ErrInternalOnly)
}

func (r *SealRequest) UnmarshalJSON(data []byte) error {
	return fmt.Errorf("%w: SealRequest", ErrInternalOnly)
}

type OutputSealer interface {
	Seal(context.Context, SealRequest) (map[string]SealedOutput, error)
}

// SnapshotCreator is the shared canonicalize/validate/stage/upload/commit
// boundary used by build outputs and authenticated manual uploads.
type SnapshotCreator interface {
	OutputSealer
	Upload(context.Context, UploadRequest) (Snapshot, error)
}

func strictUnmarshal(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("snapshot: multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validateRawMessage(value json.RawMessage) error {
	if len(value) != 0 && !json.Valid(value) {
		return fmt.Errorf("invalid JSON")
	}
	return nil
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func rawJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	decode := func(value json.RawMessage) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkflowRunID(value *WorkflowRunID) *WorkflowRunID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSnapshotRefs(values map[string]SnapshotRef) map[string]SnapshotRef {
	if values == nil {
		return nil
	}
	cloned := make(map[string]SnapshotRef, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
