package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pagination"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// StageUploadRequest is caller-owned pre-persistence state. StageUpload
// allocates the durable ID and creation timestamp and returns a StagedUpload.
type StageUploadRequest struct {
	Digest         Digest    `json:"digest"`
	TeamID         int       `json:"team_id"`
	Attempt        string    `json:"attempt"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

func (r StageUploadRequest) Validate() error {
	if err := r.Digest.Validate(); err != nil {
		return err
	}
	if r.TeamID <= 0 {
		return fmt.Errorf("snapshot: stage team ID must be positive")
	}
	if strings.TrimSpace(r.Attempt) == "" {
		return fmt.Errorf("snapshot: stage attempt is required")
	}
	if r.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("snapshot: stage lease expiry is required")
	}
	return nil
}

func (r StageUploadRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire StageUploadRequest
	return json.Marshal(wire(r))
}

func (r *StageUploadRequest) UnmarshalJSON(data []byte) error {
	type wire StageUploadRequest
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := StageUploadRequest(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*r = parsed
	return nil
}

type StagedUpload struct {
	ID             int64     `json:"id"`
	Digest         Digest    `json:"digest"`
	TeamID         int       `json:"team_id"`
	Attempt        string    `json:"attempt"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s StagedUpload) Validate() error {
	if s.ID <= 0 {
		return fmt.Errorf("snapshot: staged upload ID must be positive")
	}
	if err := (StageUploadRequest{
		Digest: s.Digest, TeamID: s.TeamID, Attempt: s.Attempt, LeaseExpiresAt: s.LeaseExpiresAt,
	}).Validate(); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: staged upload creation time is required")
	}
	return nil
}

func (s StagedUpload) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type wire StagedUpload
	return json.Marshal(wire(s))
}

func (s *StagedUpload) UnmarshalJSON(data []byte) error {
	type wire StagedUpload
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := StagedUpload(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*s = parsed
	return nil
}

// RetentionSpec is a pre-persistence policy input. Run-scoped retention names
// its already-durable workflow owner, but the spec contains no claim ID,
// SnapshotID, or creation timestamp.
type RetentionSpec struct {
	Class         RetentionClass `json:"class"`
	WorkflowRunID *WorkflowRunID `json:"workflow_run_id,omitempty"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	Actor         string         `json:"actor,omitempty"`
	Reason        string         `json:"reason"`
}

func (s RetentionSpec) Validate() error {
	if err := s.Class.Validate(); err != nil {
		return err
	}
	if (s.Class == RetentionClassRun) != (s.WorkflowRunID != nil) {
		return fmt.Errorf("snapshot: run retention requires exactly one workflow run ID")
	}
	if s.WorkflowRunID != nil {
		if err := s.WorkflowRunID.Validate(); err != nil {
			return err
		}
		if s.ExpiresAt != nil {
			return fmt.Errorf("snapshot: run retention cannot expire while its workflow run is active")
		}
	}
	if strings.TrimSpace(s.Reason) == "" {
		return fmt.Errorf("snapshot: retention reason is required")
	}
	return nil
}

func (s RetentionSpec) Clone() RetentionSpec {
	s.WorkflowRunID = cloneWorkflowRunID(s.WorkflowRunID)
	s.ExpiresAt = cloneTime(s.ExpiresAt)
	return s
}

func (s RetentionSpec) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type wire RetentionSpec
	return json.Marshal(wire(s))
}

func (s *RetentionSpec) UnmarshalJSON(data []byte) error {
	type wire RetentionSpec
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := RetentionSpec(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*s = parsed.Clone()
	return nil
}

// SealCommitOutput correlates one caller result key and declared output port
// with durable bytes. The metadata transaction allocates Snapshot, Production,
// Grant, RetentionClaim, LineageEdge, and workflow-binding IDs.
type SealCommitOutput struct {
	ClientKey         string          `json:"client_key"`
	Port              Port            `json:"port"`
	WorkflowPort      string          `json:"workflow_port,omitempty"`
	Digest            Digest          `json:"digest"`
	ByteSize          int64           `json:"byte_size"`
	FileCount         int64           `json:"file_count"`
	Representation    string          `json:"representation"`
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
	StagedUploadID    int64           `json:"staged_upload_id"`
	Locations         []Location      `json:"locations"`
	Retention         []RetentionSpec `json:"retention"`
	SourceMetadata    json.RawMessage `json:"source_metadata,omitempty"`
}

func (o SealCommitOutput) Validate() error {
	if strings.TrimSpace(o.ClientKey) == "" {
		return fmt.Errorf("snapshot: seal client key is required")
	}
	if err := o.Port.Validate(); err != nil {
		return err
	}
	if err := o.Digest.Validate(); err != nil {
		return err
	}
	if o.ByteSize < 0 || o.FileCount < 0 {
		return fmt.Errorf("snapshot: commit byte and file counts must not be negative")
	}
	if strings.TrimSpace(o.Representation) == "" {
		return fmt.Errorf("snapshot: commit representation is required")
	}
	if err := validateRawMessage(o.IntrinsicMetadata); err != nil {
		return fmt.Errorf("snapshot: intrinsic metadata: %w", err)
	}
	if o.StagedUploadID <= 0 {
		return fmt.Errorf("snapshot: staged upload ID must be positive")
	}
	if len(o.Locations) == 0 {
		return fmt.Errorf("snapshot: at least one durable location is required")
	}
	for _, location := range o.Locations {
		if err := location.Validate(); err != nil {
			return err
		}
		if location.Digest != o.Digest {
			return fmt.Errorf("snapshot: location digest does not match seal output")
		}
	}
	if len(o.Retention) == 0 {
		return fmt.Errorf("snapshot: at least one retention policy is required")
	}
	hasWorkflowRetention := false
	for _, retention := range o.Retention {
		if err := retention.Validate(); err != nil {
			return err
		}
		if retention.Class == RetentionClassWorkflow {
			hasWorkflowRetention = true
		}
	}
	if (strings.TrimSpace(o.WorkflowPort) != "") != hasWorkflowRetention {
		return fmt.Errorf("snapshot: workflow port and workflow retention must be provided together")
	}
	if err := validateRawMessage(o.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: source metadata: %w", err)
	}
	return nil
}

func (o SealCommitOutput) Clone() SealCommitOutput {
	o.IntrinsicMetadata = cloneRaw(o.IntrinsicMetadata)
	o.Locations = append([]Location(nil), o.Locations...)
	if o.Retention != nil {
		retention := make([]RetentionSpec, len(o.Retention))
		for i, spec := range o.Retention {
			retention[i] = spec.Clone()
		}
		o.Retention = retention
	}
	o.SourceMetadata = cloneRaw(o.SourceMetadata)
	return o
}

// CommitOutput moves validated candidate manifest data into the persistence
// phase without carrying the private ArchivePath across that boundary.
func (o CandidateOutput) CommitOutput(
	clientKey string,
	workflowPort string,
	stage StagedUpload,
	locations []Location,
	retention []RetentionSpec,
	sourceMetadata json.RawMessage,
) (SealCommitOutput, error) {
	if err := o.Validate(); err != nil {
		return SealCommitOutput{}, err
	}
	if err := stage.Validate(); err != nil {
		return SealCommitOutput{}, err
	}
	if stage.Digest != o.Digest {
		return SealCommitOutput{}, fmt.Errorf("snapshot: staged upload digest does not match candidate")
	}
	output := SealCommitOutput{
		ClientKey: clientKey, Port: o.Port, WorkflowPort: workflowPort, Digest: o.Digest,
		ByteSize: o.ByteSize, FileCount: o.FileCount, Representation: o.Representation,
		IntrinsicMetadata: o.IntrinsicMetadata, StagedUploadID: stage.ID,
		Locations: locations, Retention: retention, SourceMetadata: sourceMetadata,
	}.Clone()
	if err := output.Validate(); err != nil {
		return SealCommitOutput{}, err
	}
	return output, nil
}

func (o SealCommitOutput) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wire SealCommitOutput
	return json.Marshal(wire(o))
}

func (o *SealCommitOutput) UnmarshalJSON(data []byte) error {
	type wire SealCommitOutput
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SealCommitOutput(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*o = parsed.Clone()
	return nil
}

// SealCommit is the sole metadata mutation that exposes a seal batch. Its
// fields are all pre-persistence DTOs or IDs returned by StageUpload.
type SealCommit struct {
	Context SealCommitContext  `json:"context"`
	Outputs []SealCommitOutput `json:"outputs"`
}

func (c SealCommit) Validate() error {
	if err := c.Context.Validate(); err != nil {
		return err
	}
	if len(c.Outputs) != len(c.Context.ExpectedOutputs) {
		return fmt.Errorf("snapshot: commit output count does not match the complete expected output set")
	}
	expected := make(map[string]Port, len(c.Context.ExpectedOutputs))
	for _, port := range c.Context.ExpectedOutputs {
		expected[port.Name] = port
	}
	clientKeys := make(map[string]struct{}, len(c.Outputs))
	ports := make(map[string]struct{}, len(c.Outputs))
	workflowPorts := make(map[string]struct{}, len(c.Outputs))
	stageDigests := make(map[int64]Digest, len(c.Outputs))
	type physicalMetadata struct {
		byteSize       int64
		fileCount      int64
		representation string
	}
	physicalByDigest := make(map[Digest]physicalMetadata, len(c.Outputs))
	type semanticIdentity struct {
		typeRef TypeRef
		digest  Digest
	}
	intrinsicByIdentity := make(map[semanticIdentity]json.RawMessage, len(c.Outputs))
	for _, output := range c.Outputs {
		if err := output.Validate(); err != nil {
			return err
		}
		for _, retention := range output.Retention {
			if retention.Class != RetentionClassRun {
				continue
			}
			if c.Context.Build == nil || c.Context.Build.WorkflowRunID == nil ||
				retention.WorkflowRunID == nil ||
				*retention.WorkflowRunID != *c.Context.Build.WorkflowRunID {
				return fmt.Errorf("snapshot: run retention must name the exact producing workflow run")
			}
		}
		if _, duplicate := clientKeys[output.ClientKey]; duplicate {
			return fmt.Errorf("snapshot: duplicate seal client key %q", output.ClientKey)
		}
		clientKeys[output.ClientKey] = struct{}{}
		if _, duplicate := ports[output.Port.Name]; duplicate {
			return fmt.Errorf("snapshot: duplicate committed output port %q", output.Port.Name)
		}
		ports[output.Port.Name] = struct{}{}
		if output.WorkflowPort != "" {
			if _, duplicate := workflowPorts[output.WorkflowPort]; duplicate {
				return fmt.Errorf("snapshot: duplicate workflow port %q", output.WorkflowPort)
			}
			workflowPorts[output.WorkflowPort] = struct{}{}
		}
		expectedPort, found := expected[output.Port.Name]
		if !found || expectedPort.Type != output.Port.Type || expectedPort.Optional != output.Port.Optional {
			return fmt.Errorf("snapshot: committed output %q does not match the expected output set", output.Port.Name)
		}
		if digest, found := stageDigests[output.StagedUploadID]; found && digest != output.Digest {
			return fmt.Errorf("snapshot: staged upload %d is correlated with multiple digests", output.StagedUploadID)
		}
		stageDigests[output.StagedUploadID] = output.Digest
		physical := physicalMetadata{output.ByteSize, output.FileCount, output.Representation}
		if existing, found := physicalByDigest[output.Digest]; found && existing != physical {
			return fmt.Errorf("snapshot: outputs sharing digest %s have contradictory physical metadata", output.Digest)
		}
		physicalByDigest[output.Digest] = physical
		identity := semanticIdentity{typeRef: output.Port.Type, digest: output.Digest}
		if existing, found := intrinsicByIdentity[identity]; found && !rawJSONEqual(existing, output.IntrinsicMetadata) {
			return fmt.Errorf("snapshot: outputs sharing type %s and digest %s have contradictory intrinsic metadata", output.Port.Type, output.Digest)
		}
		intrinsicByIdentity[identity] = output.IntrinsicMetadata
	}
	return nil
}

func (c SealCommit) Clone() SealCommit {
	c.Context = c.Context.Clone()
	if c.Outputs != nil {
		outputs := make([]SealCommitOutput, len(c.Outputs))
		for i, output := range c.Outputs {
			outputs[i] = output.Clone()
		}
		c.Outputs = outputs
	}
	return c
}

func (c SealCommit) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	type wire SealCommit
	return json.Marshal(wire(c))
}

func (c *SealCommit) UnmarshalJSON(data []byte) error {
	type wire SealCommit
	var value wire
	if err := strictUnmarshal(data, &value); err != nil {
		return err
	}
	parsed := SealCommit(value)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*c = parsed.Clone()
	return nil
}

type SnapshotListFilter struct {
	Type         TypeRef
	ContentState ContentState
	CreatedAfter *time.Time
	Before       *pagination.Cursor
	Limit        int
}

type ProductionKind string

const (
	ProductionKindBuild  ProductionKind = "build"
	ProductionKindUpload ProductionKind = "upload"
)

func (k ProductionKind) Validate() error {
	switch k {
	case ProductionKindBuild, ProductionKindUpload:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid production kind %q", k)
	}
}

// ProductionInput is an authorization-filtered lineage edge. The referenced
// snapshot is present only because the same team has a grant to it.
type ProductionInput struct {
	Position int         `json:"position"`
	Port     string      `json:"port"`
	Snapshot SnapshotRef `json:"snapshot"`
}

func (i ProductionInput) Validate() error {
	if i.Position < 0 || strings.TrimSpace(i.Port) == "" {
		return fmt.Errorf("snapshot: production input position and port are required")
	}
	return i.Snapshot.Validate()
}

// ProductionDetail deliberately omits team IDs, upload idempotency keys, and
// physical storage details. Upload identity remains private persistence state.
type ProductionDetail struct {
	ID             DatabaseID        `json:"id"`
	Kind           ProductionKind    `json:"kind"`
	CreatedBy      string            `json:"created_by"`
	Build          *BuildOccurrence  `json:"build,omitempty"`
	OutputPort     string            `json:"output_port,omitempty"`
	SourceMetadata json.RawMessage   `json:"source_metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	Inputs         []ProductionInput `json:"inputs"`
}

func (p ProductionDetail) Validate() error {
	if p.ID <= 0 || strings.TrimSpace(p.CreatedBy) == "" || p.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot: production detail identity, creator, and creation time are required")
	}
	if err := p.Kind.Validate(); err != nil {
		return err
	}
	if p.Kind == ProductionKindBuild {
		if p.Build == nil || strings.TrimSpace(p.OutputPort) == "" {
			return fmt.Errorf("snapshot: build production detail requires build provenance and output port")
		}
		if err := p.Build.Validate(); err != nil {
			return err
		}
	} else if p.Build != nil || p.OutputPort != "" || len(p.Inputs) != 0 {
		return fmt.Errorf("snapshot: upload production detail cannot expose build provenance")
	}
	if err := validateRawMessage(p.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: production detail source metadata: %w", err)
	}
	for _, input := range p.Inputs {
		if err := input.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p ProductionDetail) Clone() ProductionDetail {
	if p.Build != nil {
		build := p.Build.Clone()
		p.Build = &build
	}
	p.SourceMetadata = cloneRaw(p.SourceMetadata)
	p.Inputs = append([]ProductionInput(nil), p.Inputs...)
	return p
}

type ProductionSummary struct {
	ID         DatabaseID       `json:"id"`
	Kind       ProductionKind   `json:"kind"`
	CreatedBy  string           `json:"created_by"`
	Build      *BuildOccurrence `json:"build,omitempty"`
	OutputPort string           `json:"output_port,omitempty"`
	Snapshot   SnapshotRef      `json:"snapshot"`
	CreatedAt  time.Time        `json:"created_at"`
}

func (p ProductionSummary) Validate() error {
	detail := ProductionDetail{
		ID: p.ID, Kind: p.Kind, CreatedBy: p.CreatedBy, Build: p.Build,
		OutputPort: p.OutputPort, CreatedAt: p.CreatedAt, Inputs: []ProductionInput{},
	}
	if err := detail.Validate(); err != nil {
		return err
	}
	return p.Snapshot.Validate()
}

func (p ProductionSummary) Clone() ProductionSummary {
	if p.Build != nil {
		build := p.Build.Clone()
		p.Build = &build
	}
	return p
}

// Detail is the safe, team-scoped read model for one authorized snapshot.
type Detail struct {
	Manifest        Snapshot            `json:"manifest"`
	ReplicaCount    int                 `json:"replica_count"`
	RetentionClaims []RetentionClaim    `json:"retention_claims"`
	Productions     []ProductionDetail  `json:"productions"`
	Downstream      []ProductionSummary `json:"downstream"`
}

func (d Detail) Validate() error {
	if err := d.Manifest.Validate(); err != nil {
		return err
	}
	if d.ReplicaCount < 0 {
		return fmt.Errorf("snapshot: replica count must not be negative")
	}
	for _, claim := range d.RetentionClaims {
		if err := claim.Validate(); err != nil || claim.SnapshotID != d.Manifest.ID {
			return fmt.Errorf("snapshot: invalid detail retention claim")
		}
	}
	for _, production := range d.Productions {
		if err := production.Validate(); err != nil {
			return err
		}
	}
	for _, production := range d.Downstream {
		if err := production.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d Detail) Clone() Detail {
	d.Manifest = d.Manifest.Clone()
	if d.RetentionClaims != nil {
		claims := make([]RetentionClaim, len(d.RetentionClaims))
		for i, claim := range d.RetentionClaims {
			claims[i] = claim.Clone()
		}
		d.RetentionClaims = claims
	}
	if d.Productions != nil {
		productions := make([]ProductionDetail, len(d.Productions))
		for i, production := range d.Productions {
			productions[i] = production.Clone()
		}
		d.Productions = productions
	}
	if d.Downstream != nil {
		downstream := make([]ProductionSummary, len(d.Downstream))
		for i, production := range d.Downstream {
			downstream[i] = production.Clone()
		}
		d.Downstream = downstream
	}
	return d
}

func (f SnapshotListFilter) Validate() error {
	if f.Type != "" {
		if err := f.Type.Validate(); err != nil {
			return err
		}
	}
	if f.ContentState != "" {
		if err := f.ContentState.Validate(); err != nil {
			return err
		}
	}
	if f.Before != nil {
		if err := f.Before.Validate(); err != nil {
			return err
		}
	}
	if f.Limit < 0 || f.Limit > 1001 {
		return fmt.Errorf("snapshot: list fetch limit must be between 0 and 1001")
	}
	return nil
}

func (f SnapshotListFilter) Clone() SnapshotListFilter {
	f.CreatedAfter = cloneTime(f.CreatedAfter)
	if f.Before != nil {
		before := *f.Before
		f.Before = &before
	}
	return f
}

type LifecycleCandidateKind string

const (
	LifecycleCandidateOrphan LifecycleCandidateKind = "orphan"
	LifecycleCandidateExpiry LifecycleCandidateKind = "expiry"
	LifecycleCandidateRepair LifecycleCandidateKind = "repair"
)

func (k LifecycleCandidateKind) Validate() error {
	switch k {
	case LifecycleCandidateOrphan, LifecycleCandidateExpiry, LifecycleCandidateRepair:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid lifecycle candidate kind %q", k)
	}
}

// LifecycleCursor is the canonical `<digest>|<kind>` position of the final
// candidate returned by a page. The empty cursor starts discovery or signals
// termination when returned as LifecycleCandidatePage.Next.
type LifecycleCursor string

func (c LifecycleCursor) Validate() error {
	if c == "" {
		return nil
	}
	parts := strings.Split(string(c), "|")
	if len(parts) != 2 {
		return fmt.Errorf("snapshot: invalid lifecycle cursor %q", c)
	}
	digest, err := ParseDigest(parts[0])
	if err != nil {
		return fmt.Errorf("snapshot: invalid lifecycle cursor %q", c)
	}
	kind := LifecycleCandidateKind(parts[1])
	if err := kind.Validate(); err != nil {
		return fmt.Errorf("snapshot: invalid lifecycle cursor %q", c)
	}
	if LifecycleCursor(digest.String()+"|"+string(kind)) != c {
		return fmt.Errorf("snapshot: non-canonical lifecycle cursor %q", c)
	}
	return nil
}

const MaxLifecyclePageSize = 1000

// LifecyclePageRequest bounds discovery. Results are ordered strictly by
// Digest and then Kind, after the exclusive cursor.
type LifecyclePageRequest struct {
	After LifecycleCursor
	Limit int
}

func (r LifecyclePageRequest) Validate() error {
	if err := r.After.Validate(); err != nil {
		return err
	}
	if r.Limit <= 0 || r.Limit > MaxLifecyclePageSize {
		return fmt.Errorf("snapshot: lifecycle page limit must be between 1 and %d", MaxLifecyclePageSize)
	}
	return nil
}

type LifecycleCandidate struct {
	Digest Digest                 `json:"digest"`
	Kind   LifecycleCandidateKind `json:"kind"`
}

func (c LifecycleCandidate) Validate() error {
	if err := c.Digest.Validate(); err != nil {
		return err
	}
	return c.Kind.Validate()
}

func (c LifecycleCandidate) Cursor() LifecycleCursor {
	return LifecycleCursor(c.Digest.String() + "|" + string(c.Kind))
}

type LifecycleCandidatePage struct {
	Candidates []LifecycleCandidate
	Next       LifecycleCursor
}

func (p LifecycleCandidatePage) Terminal() bool { return p.Next == "" }

func (p LifecycleCandidatePage) Validate(request LifecyclePageRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(p.Candidates) > request.Limit {
		return fmt.Errorf("snapshot: lifecycle page exceeds requested limit")
	}
	previous := request.After
	for _, candidate := range p.Candidates {
		if err := candidate.Validate(); err != nil {
			return err
		}
		cursor := candidate.Cursor()
		if cursor <= previous {
			return fmt.Errorf("snapshot: lifecycle candidates are not in stable cursor order")
		}
		previous = cursor
	}
	if err := p.Next.Validate(); err != nil {
		return err
	}
	if p.Next != "" {
		if len(p.Candidates) == 0 || p.Next != p.Candidates[len(p.Candidates)-1].Cursor() {
			return fmt.Errorf("snapshot: next lifecycle cursor must identify the final candidate")
		}
	}
	return nil
}

// DigestState is the authoritative digest-wide recheck. Snapshots contains
// every semantic (type,digest) manifest sharing these physical bytes.
type DigestState struct {
	Digest             Digest
	Snapshots          []Snapshot
	Stages             []StagedUpload
	Locations          []Location
	HasActiveRetention bool
}

func (s DigestState) Validate() error {
	if err := s.Digest.Validate(); err != nil {
		return err
	}
	var physical *struct {
		byteSize       int64
		fileCount      int64
		representation string
		contentState   ContentState
	}
	for _, snapshot := range s.Snapshots {
		if err := snapshot.Validate(); err != nil {
			return err
		}
		if snapshot.Digest != s.Digest {
			return fmt.Errorf("snapshot: digest state contains a manifest for another digest")
		}
		current := struct {
			byteSize       int64
			fileCount      int64
			representation string
			contentState   ContentState
		}{snapshot.ByteSize, snapshot.FileCount, snapshot.Representation, snapshot.ContentState}
		if physical != nil && *physical != current {
			return fmt.Errorf("snapshot: digest state contains contradictory physical manifest metadata")
		}
		physical = &current
	}
	for _, stage := range s.Stages {
		if err := stage.Validate(); err != nil {
			return err
		}
		if stage.Digest != s.Digest {
			return fmt.Errorf("snapshot: digest state contains a sibling stage for another digest")
		}
	}
	for _, location := range s.Locations {
		if err := location.Validate(); err != nil {
			return err
		}
		if location.Digest != s.Digest {
			return fmt.Errorf("snapshot: digest state contains a location for another digest")
		}
	}
	return nil
}

func (s DigestState) HasManifest() bool { return len(s.Snapshots) > 0 }

func (s DigestState) Available() bool {
	return s.Validate() == nil && s.HasManifest() && s.Snapshots[0].ContentState == ContentStateAvailable
}

func (s DigestState) Reusable() bool { return s.Available() && len(s.Locations) > 0 }

// ValidateCommit is the authoritative cross-transaction digest compatibility
// rule. Expired coherent manifests may be resealed, but physical metadata must
// agree across every semantic type and intrinsic metadata must agree for an
// existing identical (TypeRef,Digest) identity.
func (s DigestState) ValidateCommit(output SealCommitOutput) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := output.Validate(); err != nil {
		return err
	}
	if output.Digest != s.Digest {
		return fmt.Errorf("snapshot: commit output digest does not match digest state")
	}
	for _, manifest := range s.Snapshots {
		if manifest.ByteSize != output.ByteSize || manifest.FileCount != output.FileCount || manifest.Representation != output.Representation {
			return fmt.Errorf("snapshot: commit output conflicts with persisted physical digest metadata")
		}
		if manifest.Type == output.Port.Type && !rawJSONEqual(manifest.IntrinsicMetadata, output.IntrinsicMetadata) {
			return fmt.Errorf("snapshot: commit output conflicts with intrinsic metadata for the same type and digest")
		}
	}
	return nil
}

func (s DigestState) Accepts(output SealCommitOutput) bool {
	return s.ValidateCommit(output) == nil
}

func (s DigestState) HasUnexpiredStage(now time.Time) bool {
	for _, stage := range s.Stages {
		if stage.LeaseExpiresAt.After(now) {
			return true
		}
	}
	return false
}

func (s DigestState) CanExpire(now time.Time) bool {
	return s.Available() && !s.HasActiveRetention && !s.HasUnexpiredStage(now) && len(s.Locations) == 0
}

func (s DigestState) Clone() DigestState {
	if s.Snapshots != nil {
		snapshots := make([]Snapshot, len(s.Snapshots))
		for i, snapshot := range s.Snapshots {
			snapshots[i] = snapshot.Clone()
		}
		s.Snapshots = snapshots
	}
	s.Stages = append([]StagedUpload(nil), s.Stages...)
	s.Locations = append([]Location(nil), s.Locations...)
	return s
}

// MetadataStore separates bounded discovery from authoritative lease-scoped
// mutations and rechecks. Every method accepting DigestLease must reject a
// lease that does not cover the digest involved.
//
//counterfeiter:generate . MetadataStore
type MetadataStore interface {
	StageUpload(context.Context, DigestLease, StageUploadRequest) (StagedUpload, error)
	// CommitSealBatch verifies every persisted stage's digest, team, and attempt
	// against the commit context and verifies lease coverage for every output
	// digest inside the same transaction that allocates and connects all rows.
	// For each digest it loads all persisted sibling manifests and applies
	// DigestState.ValidateCommit. When coherent expired bytes are reuploaded, the
	// transaction transitions every semantic sibling manifest for that digest to
	// available; it never revives only one (TypeRef,Digest) row.
	CommitSealBatch(context.Context, DigestLease, SealCommit) (map[string]SealedOutput, error)

	DiscoverLifecycleCandidates(context.Context, LifecyclePageRequest) (LifecycleCandidatePage, error)
	DigestState(context.Context, DigestLease, Digest, time.Time) (DigestState, error)
	RemoveStagedUploads(context.Context, DigestLease, Digest, []int64) error

	GetAuthorized(context.Context, int, SnapshotID) (Snapshot, bool, error)
	GetAuthorizedDetail(context.Context, int, SnapshotID) (Detail, bool, error)
	ListAuthorized(context.Context, int, SnapshotListFilter) ([]Snapshot, error)
	// Pin and Unpin perform the final authorization, identity, and availability
	// recheck while holding a lease that covers ref.Digest.
	Pin(context.Context, DigestLease, int, string, SnapshotRef, string) (RetentionClaim, error)
	Unpin(context.Context, DigestLease, int, string, SnapshotRef) error

	// MarkDigestExpired is compare-and-set-like: under the supplied lease it
	// returns false unless every semantic manifest sharing digest has no active
	// claim, no sibling stage has LeaseExpiresAt after now, and digest has zero
	// recorded locations at transaction time.
	MarkDigestExpired(context.Context, DigestLease, Digest, time.Time) (bool, error)
	AddLocation(context.Context, DigestLease, Location) error
	RemoveLocation(context.Context, DigestLease, Location) error
}

// ContentStore owns immutable bytes independently of MetadataStore.
//
//counterfeiter:generate . ContentStore
type ContentStore interface {
	Put(context.Context, Digest, io.Reader) ([]Location, error)
	// Open resolves storage locations through dependencies injected into the
	// concrete content store. The core contract remains independent of metadata.
	Open(context.Context, Snapshot) (io.ReadCloser, error)
	// Exists returns true only after reading the complete object and verifying
	// its bytes against Location.Digest. Presence-only probes are insufficient:
	// callers use this result to decide whether immutable content may be reused.
	Exists(context.Context, Location) (bool, error)
	DeleteLocation(context.Context, Location) error
	DeleteAll(context.Context, Digest) error
}

// DigestLease is a session-scoped lease whose coverage is testable.
//
//counterfeiter:generate . DigestLease
type DigestLease interface {
	Covers(Digest) bool
	Close() error
}

// DigestLockManager acquires the already-sorted unique digests for one lease.
// On partial acquisition it may return both a non-nil lease and an error;
// callers must close that partial lease.
//
//counterfeiter:generate . DigestLockManager
type DigestLockManager interface {
	AcquireMany(context.Context, []Digest) (DigestLease, error)
}

func RequireDigestLease(lease DigestLease, digest Digest) error {
	if err := digest.Validate(); err != nil {
		return err
	}
	if lease == nil || !lease.Covers(digest) {
		return fmt.Errorf("snapshot: digest lease does not cover %s", digest)
	}
	return nil
}

// WithDigestLease is the sequencing seam shared by future sealer and lifecycle
// code. Close runs after the whole callback (stage -> content I/O -> commit, or
// final recheck -> delete -> stage cleanup), including callback errors.
func WithDigestLease(
	ctx context.Context,
	manager DigestLockManager,
	digests []Digest,
	callback func(DigestLease) error,
) (err error) {
	if manager == nil || callback == nil {
		return fmt.Errorf("snapshot: digest lock manager and callback are required")
	}
	if len(digests) == 0 {
		return fmt.Errorf("snapshot: at least one digest lease is required")
	}
	unique := make(map[Digest]struct{}, len(digests))
	for _, digest := range digests {
		if err := digest.Validate(); err != nil {
			return err
		}
		unique[digest] = struct{}{}
	}
	sorted := make([]Digest, 0, len(unique))
	for digest := range unique {
		sorted = append(sorted, digest)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	lease, err := manager.AcquireMany(ctx, sorted)
	if err != nil {
		if lease != nil {
			return errors.Join(err, lease.Close())
		}
		return err
	}
	if lease == nil {
		return fmt.Errorf("snapshot: lock manager returned a nil digest lease")
	}
	defer func() {
		err = errors.Join(err, lease.Close())
	}()
	for _, digest := range sorted {
		if err := RequireDigestLease(lease, digest); err != nil {
			return err
		}
	}
	return callback(lease)
}
