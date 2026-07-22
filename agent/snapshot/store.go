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

// RetentionSpec is a pre-persistence policy input. It contains no database ID,
// SnapshotID, or creation timestamp.
type RetentionSpec struct {
	Class     RetentionClass `json:"class"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Reason    string         `json:"reason"`
}

func (s RetentionSpec) Validate() error {
	if err := s.Class.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Reason) == "" {
		return fmt.Errorf("snapshot: retention reason is required")
	}
	return nil
}

func (s RetentionSpec) Clone() RetentionSpec {
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
	ClientKey      string          `json:"client_key"`
	OutputPort     string          `json:"output_port"`
	Digest         Digest          `json:"digest"`
	StagedUploadID int64           `json:"staged_upload_id"`
	Locations      []Location      `json:"locations"`
	Retention      []RetentionSpec `json:"retention"`
	SourceMetadata json.RawMessage `json:"source_metadata,omitempty"`
}

func (o SealCommitOutput) Validate() error {
	if strings.TrimSpace(o.ClientKey) == "" || strings.TrimSpace(o.OutputPort) == "" {
		return fmt.Errorf("snapshot: seal client key and output port are required")
	}
	if err := o.Digest.Validate(); err != nil {
		return err
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
	for _, retention := range o.Retention {
		if err := retention.Validate(); err != nil {
			return err
		}
	}
	if err := validateRawMessage(o.SourceMetadata); err != nil {
		return fmt.Errorf("snapshot: source metadata: %w", err)
	}
	return nil
}

func (o SealCommitOutput) Clone() SealCommitOutput {
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
	Request SealRequest        `json:"request"`
	Outputs []SealCommitOutput `json:"outputs"`
}

func (c SealCommit) Validate() error {
	if err := c.Request.Validate(); err != nil {
		return err
	}
	if len(c.Outputs) != len(c.Request.Outputs) {
		return fmt.Errorf("snapshot: commit output count does not match request")
	}
	candidates := make(map[string]CandidateOutput, len(c.Request.Outputs))
	for _, candidate := range c.Request.Outputs {
		candidates[candidate.Port.Name] = candidate
	}
	clientKeys := make(map[string]struct{}, len(c.Outputs))
	ports := make(map[string]struct{}, len(c.Outputs))
	for _, output := range c.Outputs {
		if err := output.Validate(); err != nil {
			return err
		}
		if _, duplicate := clientKeys[output.ClientKey]; duplicate {
			return fmt.Errorf("snapshot: duplicate seal client key %q", output.ClientKey)
		}
		clientKeys[output.ClientKey] = struct{}{}
		if _, duplicate := ports[output.OutputPort]; duplicate {
			return fmt.Errorf("snapshot: duplicate committed output port %q", output.OutputPort)
		}
		ports[output.OutputPort] = struct{}{}
		candidate, found := candidates[output.OutputPort]
		if !found {
			return fmt.Errorf("snapshot: commit references undeclared output port %q", output.OutputPort)
		}
		if candidate.Digest != output.Digest {
			return fmt.Errorf("snapshot: commit digest does not match candidate for port %q", output.OutputPort)
		}
	}
	return nil
}

func (c SealCommit) Clone() SealCommit {
	c.Request = c.Request.Clone()
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
	Limit        int
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
	if f.Limit < 0 || f.Limit > 1000 {
		return fmt.Errorf("snapshot: list limit must be between 0 and 1000")
	}
	return nil
}

func (f SnapshotListFilter) Clone() SnapshotListFilter {
	f.CreatedAfter = cloneTime(f.CreatedAfter)
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

// LifecycleCursor is opaque to callers. Discovery is only a bounded hint;
// every candidate must be rechecked with DigestState under a matching lease.
type LifecycleCursor string

type LifecycleCandidate struct {
	Digest Digest                 `json:"digest"`
	Kind   LifecycleCandidateKind `json:"kind"`
}

type LifecycleCandidatePage struct {
	Candidates []LifecycleCandidate
	Next       LifecycleCursor
}

// DigestState is the authoritative digest-wide recheck. Snapshots contains
// every semantic (type,digest) manifest sharing these physical bytes.
type DigestState struct {
	Digest             Digest
	Snapshots          []Snapshot
	Stages             []StagedUpload
	Locations          []Location
	HasActiveRetention bool
	HasUnexpiredStage  bool
}

func (s DigestState) Validate() error {
	if err := s.Digest.Validate(); err != nil {
		return err
	}
	for _, snapshot := range s.Snapshots {
		if err := snapshot.Validate(); err != nil {
			return err
		}
		if snapshot.Digest != s.Digest {
			return fmt.Errorf("snapshot: digest state contains a manifest for another digest")
		}
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

func (s DigestState) Committed() bool { return len(s.Snapshots) > 0 }

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
	CommitSealBatch(context.Context, DigestLease, SealCommit) (map[string]SealedOutput, error)

	DiscoverLifecycleCandidates(context.Context, LifecycleCursor, int) (LifecycleCandidatePage, error)
	DigestState(context.Context, DigestLease, Digest, time.Time) (DigestState, error)
	RemoveStagedUploads(context.Context, DigestLease, Digest, []int64) error

	GetAuthorized(context.Context, int, SnapshotID) (Snapshot, bool, error)
	ListAuthorized(context.Context, int, SnapshotListFilter) ([]Snapshot, error)
	// Pin and Unpin perform the final authorization, identity, and availability
	// recheck while holding a lease that covers ref.Digest.
	Pin(context.Context, DigestLease, int, string, SnapshotRef, string) (RetentionClaim, error)
	Unpin(context.Context, DigestLease, int, string, SnapshotRef) error

	// MarkDigestExpired is compare-and-set-like: it returns false unless every
	// semantic manifest sharing digest has no active claim and digest has zero
	// recorded locations at the time of the transaction.
	MarkDigestExpired(context.Context, DigestLease, Digest, time.Time) (bool, error)
	AddLocation(context.Context, DigestLease, Location) error
	RemoveLocation(context.Context, DigestLease, Location) error
}

// ContentStore owns immutable bytes independently of MetadataStore.
//
//counterfeiter:generate . ContentStore
type ContentStore interface {
	Put(context.Context, Digest, io.Reader) ([]Location, error)
	// Open uses only the recorded locations supplied by the authorized metadata
	// lookup; a guessed SnapshotID or live peer does not authorize a read.
	Open(context.Context, Snapshot, []Location) (io.ReadCloser, error)
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
