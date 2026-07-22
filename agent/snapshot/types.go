// Package snapshot defines typed snapshot contracts without depending on ATC,
// a database, or a storage implementation.
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var typeRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)*/v[1-9][0-9]*$`)

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

func NewWorkflowRunID(value int64) (WorkflowRunID, error) {
	id := WorkflowRunID(value)
	if err := id.Validate(); err != nil {
		return 0, err
	}
	return id, nil
}

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

// Production is one persisted invocation's provenance for a snapshot value.
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

func (p Production) Validate() error {
	if p.ID <= 0 || p.BuildID <= 0 || p.TeamID <= 0 {
		return fmt.Errorf("snapshot: production, build, and team IDs must be positive")
	}
	if err := p.SnapshotID.Validate(); err != nil {
		return err
	}
	for label, value := range map[string]string{
		"team name": p.TeamName, "creator": p.CreatedBy, "plan ID": p.PlanID,
		"attempt": p.Attempt, "step kind": p.StepKind, "step name": p.StepName,
		"output port": p.OutputPort,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot: production %s is required", label)
		}
	}
	if p.WorkflowDefinitionID != nil && *p.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("snapshot: workflow definition ID must be positive")
	}
	if p.WorkflowRunID != nil {
		if err := p.WorkflowRunID.Validate(); err != nil {
			return err
		}
	}
	if err := validateRawMessage(p.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: source metadata: %w", err)
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: production creation time is required")
	}
	return nil
}

func (p Production) Clone() Production {
	p.WorkflowDefinitionID = cloneInt(p.WorkflowDefinitionID)
	p.WorkflowRunID = cloneWorkflowRunID(p.WorkflowRunID)
	p.SourceMetadata = cloneRaw(p.SourceMetadata)
	return p
}

func (p Production) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire Production
	return json.Marshal(wire(p))
}

func (p *Production) UnmarshalJSON(data []byte) error {
	type wire Production
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := Production(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*p = parsed.Clone()
	return nil
}

// Grant authorizes a team to read a snapshot. It never retains bytes.
type Grant struct {
	ID         int64      `json:"id"`
	SnapshotID SnapshotID `json:"snapshot_id"`
	TeamID     int        `json:"team_id"`
	GrantedBy  string     `json:"granted_by"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (g Grant) Validate() error {
	if g.ID <= 0 || g.TeamID <= 0 {
		return fmt.Errorf("snapshot: grant and team IDs must be positive")
	}
	if err := g.SnapshotID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(g.GrantedBy) == "" || strings.TrimSpace(g.Reason) == "" || g.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: grantor, reason, and creation time are required")
	}
	return nil
}

func (g Grant) MarshalJSON() ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	type wire Grant
	return json.Marshal(wire(g))
}

func (g *Grant) UnmarshalJSON(data []byte) error {
	type wire Grant
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := Grant(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*g = parsed
	return nil
}

type RetentionClass string

const (
	RetentionClassBinding  RetentionClass = "binding"
	RetentionClassWorkflow RetentionClass = "workflow"
	RetentionClassFixture  RetentionClass = "fixture"
	RetentionClassPin      RetentionClass = "pin"
)

func (c RetentionClass) Validate() error {
	switch c {
	case RetentionClassBinding, RetentionClassWorkflow, RetentionClassFixture, RetentionClassPin:
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
	ID         int64          `json:"id"`
	SnapshotID SnapshotID     `json:"snapshot_id"`
	Class      RetentionClass `json:"class"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Actor      string         `json:"actor,omitempty"`
	Reason     string         `json:"reason"`
	CreatedAt  time.Time      `json:"created_at"`
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
	if strings.TrimSpace(c.Reason) == "" || c.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: retention reason and creation time are required")
	}
	return nil
}

func (c RetentionClaim) Clone() RetentionClaim {
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

func EffectiveRetentionClaim(claims []RetentionClaim, now time.Time) (RetentionClaim, bool) {
	active := make([]RetentionClaim, 0, len(claims))
	for _, claim := range claims {
		if claim.Active(now) {
			active = append(active, claim.Clone())
		}
	}
	if len(active) == 0 {
		return RetentionClaim{}, false
	}
	SortRetentionClaims(active)
	return active[0].Clone(), true
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

// LineageEdge is a persisted ordered production/input relationship.
type LineageEdge struct {
	ProductionID    int64      `json:"production_id"`
	Position        int        `json:"position"`
	InputPort       string     `json:"input_port"`
	InputSnapshotID SnapshotID `json:"input_snapshot_id"`
}

func (e LineageEdge) Validate() error {
	if e.ProductionID <= 0 || e.Position < 0 {
		return fmt.Errorf("snapshot: lineage production ID must be positive and position non-negative")
	}
	if strings.TrimSpace(e.InputPort) == "" {
		return fmt.Errorf("snapshot: lineage input port is required")
	}
	return e.InputSnapshotID.Validate()
}

func (e LineageEdge) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire LineageEdge
	return json.Marshal(wire(e))
}

func (e *LineageEdge) UnmarshalJSON(data []byte) error {
	type wire LineageEdge
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := LineageEdge(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*e = parsed
	return nil
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

func (o CandidateOutput) Validate() error {
	if err := o.Port.Validate(); err != nil {
		return err
	}
	if err := o.Digest.Validate(); err != nil {
		return err
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
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wire CandidateOutput
	return json.Marshal(wire(o))
}

func (o *CandidateOutput) UnmarshalJSON(data []byte) error {
	type wire CandidateOutput
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := CandidateOutput(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*o = parsed.Clone()
	return nil
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

// SealRequest is the invocation context. InputOrder is the exact permutation
// used to persist lineage positions; maps alone cannot preserve source order.
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
	Outputs              []CandidateOutput      `json:"outputs"`
}

func (r SealRequest) Validate() error {
	if r.BuildID <= 0 || r.TeamID <= 0 {
		return fmt.Errorf("snapshot: build and team IDs must be positive")
	}
	for label, value := range map[string]string{
		"team name": r.TeamName, "creator": r.CreatedBy, "plan ID": r.PlanID,
		"attempt": r.Attempt, "step kind": r.StepKind, "step name": r.StepName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("snapshot: %s is required", label)
		}
	}
	if r.WorkflowDefinitionID != nil && *r.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("snapshot: workflow definition ID must be positive")
	}
	if r.WorkflowRunID != nil {
		if err := r.WorkflowRunID.Validate(); err != nil {
			return err
		}
	}
	if len(r.InputOrder) != len(r.Inputs) {
		return fmt.Errorf("snapshot: input order must contain every input exactly once")
	}
	seenInputs := make(map[string]struct{}, len(r.Inputs))
	for _, name := range r.InputOrder {
		ref, found := r.Inputs[name]
		if !found {
			return fmt.Errorf("snapshot: input order contains unknown port %q", name)
		}
		if _, duplicate := seenInputs[name]; duplicate {
			return fmt.Errorf("snapshot: input order contains duplicate port %q", name)
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("snapshot: input %q: %w", name, err)
		}
		seenInputs[name] = struct{}{}
	}
	if len(r.Outputs) == 0 {
		return fmt.Errorf("snapshot: at least one output is required")
	}
	ports := make([]Port, len(r.Outputs))
	for i, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("snapshot: output %d: %w", i, err)
		}
		ports[i] = output.Port
	}
	return ValidatePorts(ports)
}

func (r SealRequest) Clone() SealRequest {
	r.WorkflowDefinitionID = cloneInt(r.WorkflowDefinitionID)
	r.WorkflowRunID = cloneWorkflowRunID(r.WorkflowRunID)
	r.InputOrder = append([]string(nil), r.InputOrder...)
	r.Inputs = cloneSnapshotRefs(r.Inputs)
	r.Outputs = cloneCandidates(r.Outputs)
	return r
}

func (r SealRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire SealRequest
	return json.Marshal(wire(r))
}

func (r *SealRequest) UnmarshalJSON(data []byte) error {
	type wire SealRequest
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SealRequest(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*r = parsed.Clone()
	return nil
}

type OutputSealer interface {
	Seal(context.Context, SealRequest) (map[string]SealedOutput, error)
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

func cloneCandidates(values []CandidateOutput) []CandidateOutput {
	if values == nil {
		return nil
	}
	cloned := make([]CandidateOutput, len(values))
	for i, value := range values {
		cloned[i] = value.Clone()
	}
	return cloned
}
