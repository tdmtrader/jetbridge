// Package checkpoint defines the durable recovery contracts shared by the
// runner and ATC.  The package deliberately contains no database or Hangar
// client implementation: PostgreSQL chooses the latest committed manifest and
// Hangar owns the immutable archive bytes.
package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
)

var (
	// ErrConflict marks a stale compare-and-swap, an immutable journal conflict,
	// or another state transition which cannot be safely retried unchanged.
	ErrConflict = errors.New("checkpoint: conflict")
	// ErrNotFound marks a durable recovery record which was not found.
	ErrNotFound = errors.New("checkpoint: not found")
	// ErrExpired marks a staged checkpoint or upload ticket which is no longer
	// eligible to be completed.
	ErrExpired = errors.New("checkpoint: expired")
)

type FallbackMode string

const (
	FallbackNativeResume   FallbackMode = "native_resume"
	FallbackWorkspaceOnly  FallbackMode = "workspace_only"
	FallbackCheckpointZero FallbackMode = "checkpoint_zero"
	FallbackManualReview   FallbackMode = "manual_review_required"
)

type EffectState string

const (
	EffectBegun     EffectState = "begun"
	EffectCommitted EffectState = "committed"
)

// Effect is an ATC-observed external-tool transition.  ToolCallID is scoped by
// Identity and ExecutionAttempt, never by a provider session alone.
type Effect struct {
	ToolCallID          string      `json:"tool_call_id"`
	ToolName            string      `json:"tool_name"`
	Provider            string      `json:"provider"`
	AdapterVersion      string      `json:"adapter_version"`
	IdempotencyKey      string      `json:"idempotency_key,omitempty"`
	IdempotencyContract string      `json:"idempotency_contract,omitempty"`
	ReadOnly            bool        `json:"read_only"`
	State               EffectState `json:"state"`
}

func (effect Effect) Validate() error {
	if strings.TrimSpace(effect.ToolCallID) == "" {
		return fmt.Errorf("checkpoint: effect tool call ID is required")
	}
	if strings.TrimSpace(effect.ToolName) == "" {
		return fmt.Errorf("checkpoint: effect tool name is required")
	}
	if strings.TrimSpace(effect.Provider) == "" {
		return fmt.Errorf("checkpoint: effect provider is required")
	}
	if strings.TrimSpace(effect.AdapterVersion) == "" {
		return fmt.Errorf("checkpoint: effect adapter version is required")
	}
	switch effect.State {
	case EffectBegun, EffectCommitted:
		return nil
	default:
		return fmt.Errorf("checkpoint: invalid effect state %q", effect.State)
	}
}

// Identity is immutable execution provenance. BuildID intentionally is not a
// foreign key: build reaping must not erase recovery evidence.
type Identity struct {
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	BuildID       int64                   `json:"build_id"`
	PlanID        string                  `json:"plan_id"`
	FunctionID    string                  `json:"function_id"`
}

// Clone prevents callers from retaining a mutable pointer to durable workflow
// identity after it has crossed the checkpoint boundary.
func (identity Identity) Clone() Identity {
	cloned := identity
	if identity.WorkflowRunID != nil {
		value := *identity.WorkflowRunID
		cloned.WorkflowRunID = &value
	}
	return cloned
}

func (identity Identity) Validate() error {
	if identity.WorkflowRunID != nil && int64(*identity.WorkflowRunID) <= 0 {
		return fmt.Errorf("checkpoint: workflow run ID must be positive")
	}
	if identity.BuildID <= 0 {
		return fmt.Errorf("checkpoint: build ID must be positive")
	}
	if strings.TrimSpace(identity.PlanID) == "" {
		return fmt.Errorf("checkpoint: plan ID is required")
	}
	if strings.TrimSpace(identity.FunctionID) == "" {
		return fmt.Errorf("checkpoint: function ID is required")
	}
	return nil
}

// Manifest is the atomic durable recovery boundary. A nil Archive describes
// checkpoint zero only; it cannot carry a provider session.
type Manifest struct {
	Version              int                     `json:"version"`
	CheckpointID         int64                   `json:"checkpoint_id"`
	Generation           int                     `json:"generation"`
	ExecutionAttempt     int                     `json:"execution_attempt"`
	WorkflowRunID        *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	BuildID              int64                   `json:"build_id"`
	PlanID               string                  `json:"plan_id"`
	FunctionID           string                  `json:"function_id"`
	Provider             string                  `json:"provider"`
	RuntimeImage         string                  `json:"runtime_image"`
	Model                string                  `json:"model"`
	ConfigDigest         string                  `json:"config_digest"`
	InputDigest          string                  `json:"input_digest"`
	MCPDigest            string                  `json:"mcp_digest"`
	SkillDigest          string                  `json:"skill_digest"`
	Archive              *hangar.ObjectRef       `json:"archive,omitempty"`
	SessionID            string                  `json:"session_id,omitempty"`
	TranscriptCursor     int64                   `json:"transcript_cursor"`
	CompletedToolCallIDs []string                `json:"completed_tool_call_ids"`
	Effects              []Effect                `json:"effects"`
	SafeAt               time.Time               `json:"safe_at"`
}

func (manifest Manifest) Identity() Identity {
	return Identity{
		WorkflowRunID: manifest.WorkflowRunID,
		BuildID:       manifest.BuildID,
		PlanID:        manifest.PlanID,
		FunctionID:    manifest.FunctionID,
	}
}

func (manifest Manifest) Clone() Manifest {
	cloned := manifest
	cloned.WorkflowRunID = manifest.Identity().Clone().WorkflowRunID
	cloned.CompletedToolCallIDs = append([]string(nil), manifest.CompletedToolCallIDs...)
	cloned.Effects = append([]Effect(nil), manifest.Effects...)
	if manifest.Archive != nil {
		archive := *manifest.Archive
		cloned.Archive = &archive
	}
	return cloned
}

// Canonicalized returns a deep-enough copy for stable JSON persistence. It
// orders all effect-related collections by tool-call ID, while Validate
// rejects duplicate IDs instead of silently discarding evidence.
func (manifest Manifest) Canonicalized() Manifest {
	canonical := manifest.Clone()
	sort.Strings(canonical.CompletedToolCallIDs)
	sort.SliceStable(canonical.Effects, func(left, right int) bool {
		return canonical.Effects[left].ToolCallID < canonical.Effects[right].ToolCallID
	})
	return canonical
}

func (manifest Manifest) Validate() error {
	if manifest.Version <= 0 {
		return fmt.Errorf("checkpoint: manifest version must be positive")
	}
	if manifest.CheckpointID <= 0 {
		return fmt.Errorf("checkpoint: manifest checkpoint ID must be positive")
	}
	if manifest.Generation <= 0 {
		return fmt.Errorf("checkpoint: manifest generation must be positive")
	}
	if manifest.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: manifest execution attempt must be positive")
	}
	if err := manifest.Identity().Validate(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"provider": manifest.Provider, "runtime image": manifest.RuntimeImage, "model": manifest.Model,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("checkpoint: manifest %s is required", name)
		}
	}
	for name, value := range map[string]string{
		"config digest": manifest.ConfigDigest, "input digest": manifest.InputDigest,
		"MCP digest": manifest.MCPDigest, "skill digest": manifest.SkillDigest,
	} {
		if err := hangar.Digest(value).Validate(); err != nil {
			return fmt.Errorf("checkpoint: manifest %s: %w", name, err)
		}
	}
	if manifest.TranscriptCursor < 0 {
		return fmt.Errorf("checkpoint: transcript cursor cannot be negative")
	}
	if manifest.SafeAt.IsZero() {
		return fmt.Errorf("checkpoint: safe boundary time is required")
	}
	if manifest.Archive == nil && manifest.SessionID != "" {
		return fmt.Errorf("checkpoint: session ID requires a combined checkpoint archive")
	}
	if manifest.Archive != nil {
		if err := validateCheckpointArchive(*manifest.Archive); err != nil {
			return err
		}
	}
	completed := make(map[string]struct{}, len(manifest.CompletedToolCallIDs))
	for _, toolID := range manifest.CompletedToolCallIDs {
		if strings.TrimSpace(toolID) == "" {
			return fmt.Errorf("checkpoint: completed tool call ID is required")
		}
		if _, found := completed[toolID]; found {
			return fmt.Errorf("checkpoint: duplicate completed tool call ID %q", toolID)
		}
		completed[toolID] = struct{}{}
	}
	effects := make(map[string]struct{}, len(manifest.Effects))
	for _, effect := range manifest.Effects {
		if err := effect.Validate(); err != nil {
			return err
		}
		if _, found := effects[effect.ToolCallID]; found {
			return fmt.Errorf("checkpoint: duplicate effect tool call ID %q", effect.ToolCallID)
		}
		effects[effect.ToolCallID] = struct{}{}
		if _, found := completed[effect.ToolCallID]; !found {
			return fmt.Errorf("checkpoint: effect %q is absent from completed tool calls", effect.ToolCallID)
		}
	}
	return nil
}

func validateCheckpointArchive(archive hangar.ObjectRef) error {
	if archive.Kind != hangar.KindCheckpoint {
		return fmt.Errorf("checkpoint: archive must use Hangar checkpoint kind")
	}
	if archive.Generation <= 0 {
		return fmt.Errorf("checkpoint: archive generation must be positive")
	}
	key, err := hangar.Key(archive.Kind, archive.Digest)
	if err != nil {
		return fmt.Errorf("checkpoint: archive: %w", err)
	}
	if archive.Key != key {
		return fmt.Errorf("checkpoint: archive key does not match its content identity")
	}
	return nil
}

// Capabilities is the recovery-relevant subset of a provider adapter probe.
// It intentionally lives here until the provider package lands; the provider
// package can translate its richer probe result without importing checkpoint.
type Capabilities struct {
	SafeBoundary  bool
	EffectJournal bool
	SessionExport bool
	NativeResume  bool
	ReplaySafety  bool
	Version       string
}

type EffectSafetyRegistry interface {
	ReplaySafe(Effect) bool
}

// AutomaticRecoveryMode is deliberately conservative. Automatic recovery
// requires the ATC-observed effect journal; a nonzero checkpoint additionally
// requires a provider-declared safe boundary. External writes always require
// manual review because neither a propagated key nor a registry entry proves
// the remote receiver's state after process loss.
func (manifest Manifest) AutomaticRecoveryMode(capabilities Capabilities, _ EffectSafetyRegistry) FallbackMode {
	if !capabilities.EffectJournal {
		return FallbackManualReview
	}
	for _, effect := range manifest.Effects {
		if effect.State != EffectCommitted {
			return FallbackManualReview
		}
		if !effect.ReadOnly {
			return FallbackManualReview
		}
		if effect.Provider != manifest.Provider || capabilities.Version == "" ||
			effect.AdapterVersion != capabilities.Version {
			return FallbackManualReview
		}
	}
	if manifest.Archive == nil {
		return FallbackCheckpointZero
	}
	if !capabilities.SafeBoundary {
		return FallbackManualReview
	}
	if manifest.SessionID != "" && capabilities.SessionExport && capabilities.NativeResume {
		return FallbackNativeResume
	}
	return FallbackWorkspaceOnly
}

type CheckpointStatus string

const (
	CheckpointStaged     CheckpointStatus = "staged"
	CheckpointCommitted  CheckpointStatus = "committed"
	CheckpointSuperseded CheckpointStatus = "superseded"
	CheckpointAborted    CheckpointStatus = "aborted"
	CheckpointExpired    CheckpointStatus = "expired"
)

type BeginRequest struct {
	Identity Identity
	Fence    FenceClaim
	// ExecutionAttempt is retained only for source compatibility; Fence is the
	// sole mutation authority.
	ExecutionAttempt int
}

func (request BeginRequest) Clone() BeginRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request BeginRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt != 0 && request.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: execution attempt does not match fence")
	}
	return nil
}

type StagedCheckpoint struct {
	ID                         int64
	HeadID                     int64
	Identity                   Identity
	Generation                 int
	ExpectedPreviousGeneration int
	ExecutionAttempt           int
	Fence                      FenceClaim
	StageExpiresAt             time.Time
}

func (staged StagedCheckpoint) Clone() StagedCheckpoint {
	staged.Identity = staged.Identity.Clone()
	return staged
}

type AbortRequest struct {
	StagedCheckpointID int64
	Fence              FenceClaim
}

func (request AbortRequest) Validate() error {
	if request.StagedCheckpointID <= 0 {
		return fmt.Errorf("checkpoint: staged checkpoint ID must be positive")
	}
	return request.Fence.Validate()
}

type PrepareObjectUploadRequest struct {
	StagedCheckpointID int64
	Digest             hangar.Digest
	Key                string
	Fence              FenceClaim
}

func (request PrepareObjectUploadRequest) Validate() error {
	if request.StagedCheckpointID <= 0 {
		return fmt.Errorf("checkpoint: staged checkpoint ID must be positive")
	}
	key, err := hangar.Key(hangar.KindCheckpoint, request.Digest)
	if err != nil {
		return fmt.Errorf("checkpoint: upload digest: %w", err)
	}
	if request.Key != key {
		return fmt.Errorf("checkpoint: upload key does not match its content identity")
	}
	return request.Fence.Validate()
}

type ObjectUploadTicket struct {
	ObjectID            int64
	StagedCheckpointID  int64
	Kind                hangar.Kind
	Digest              hangar.Digest
	Key                 string
	UploadToken         string
	ExpiresAt           time.Time
	AlreadyAvailable    bool
	AvailableGeneration int64
}

func (ticket ObjectUploadTicket) Clone() ObjectUploadTicket { return ticket }

type CompleteObjectUploadRequest struct {
	Ticket ObjectUploadTicket
	Object hangar.ObjectRef
	Fence  FenceClaim
}

func (request CompleteObjectUploadRequest) Clone() CompleteObjectUploadRequest { return request }

func (request CompleteObjectUploadRequest) Validate() error {
	if request.Ticket.ObjectID <= 0 || request.Ticket.StagedCheckpointID <= 0 {
		return fmt.Errorf("checkpoint: upload ticket is invalid")
	}
	if strings.TrimSpace(request.Ticket.UploadToken) == "" && !request.Ticket.AlreadyAvailable {
		return fmt.Errorf("checkpoint: upload ticket token is required")
	}
	if err := validateCheckpointArchive(request.Object); err != nil {
		return err
	}
	if request.Object.Kind != request.Ticket.Kind || request.Object.Digest != request.Ticket.Digest || request.Object.Key != request.Ticket.Key {
		return fmt.Errorf("checkpoint: uploaded object does not match upload ticket")
	}
	return request.Fence.Validate()
}

type CommitRequest struct {
	StagedCheckpointID         int64
	ExpectedPreviousGeneration int
	Manifest                   Manifest
	Fence                      FenceClaim
}

func (request CommitRequest) Clone() CommitRequest {
	request.Manifest = request.Manifest.Clone()
	return request
}

func (request CommitRequest) Validate() error {
	if request.StagedCheckpointID <= 0 {
		return fmt.Errorf("checkpoint: staged checkpoint ID must be positive")
	}
	if request.ExpectedPreviousGeneration < 0 {
		return fmt.Errorf("checkpoint: expected previous generation cannot be negative")
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if err := request.Manifest.Validate(); err != nil {
		return err
	}
	if request.Manifest.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: manifest execution attempt does not match fence")
	}
	return nil
}

type ExpirationClaim struct {
	CheckpointID    int64
	HeadID          int64
	ArchiveObjectID *int64
	Generation      int
	Token           string
}

func (claim ExpirationClaim) Clone() ExpirationClaim {
	if claim.ArchiveObjectID != nil {
		value := *claim.ArchiveObjectID
		claim.ArchiveObjectID = &value
	}
	return claim
}

type ObjectDeleteClaim struct {
	ObjectID              int64
	Object                hangar.ObjectRef
	Token                 string
	NeedsUploadInspection bool
	UploadTicket          ObjectUploadTicket
}

func (claim ObjectDeleteClaim) Clone() ObjectDeleteClaim {
	claim.UploadTicket = claim.UploadTicket.Clone()
	return claim
}

// Store is PostgreSQL's durable checkpoint authority. The caller performs
// Hangar I/O between PrepareObjectUpload and CompleteObjectUpload, then commits
// the manifest through the generation CAS.
type Store interface {
	Begin(context.Context, BeginRequest) (StagedCheckpoint, error)
	Abort(context.Context, AbortRequest) error
	PrepareObjectUpload(context.Context, PrepareObjectUploadRequest) (ObjectUploadTicket, error)
	CompleteObjectUpload(context.Context, CompleteObjectUploadRequest) (hangar.ObjectRef, error)
	Commit(context.Context, CommitRequest) (Manifest, error)
	Latest(context.Context, Identity) (Manifest, bool, error)
	MarkTerminal(context.Context, Identity, time.Time) error
	ClaimCheckpointExpirations(context.Context, int) ([]ExpirationClaim, error)
	FinalizeCheckpointExpiration(context.Context, ExpirationClaim) error
	ClaimUnreferencedObjects(context.Context, int) ([]ObjectDeleteClaim, error)
	FinalizeObjectDeletion(context.Context, ObjectDeleteClaim) error
	CleanupTerminalMetadata(context.Context, int) (int, error)
}

type BeginEffectRequest struct {
	Identity         Identity
	Fence            FenceClaim
	ExecutionAttempt int
	Effect           Effect
}

func (request BeginEffectRequest) Clone() BeginEffectRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request BeginEffectRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt != 0 && request.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: effect execution attempt does not match fence")
	}
	if request.Effect.State != EffectBegun {
		return fmt.Errorf("checkpoint: effect journal must begin in begun state")
	}
	return request.Effect.Validate()
}

type CommitEffectRequest struct {
	Identity         Identity
	Fence            FenceClaim
	ExecutionAttempt int
	ToolCallID       string
}

func (request CommitEffectRequest) Clone() CommitEffectRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request CommitEffectRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt != 0 && request.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: effect execution attempt does not match fence")
	}
	if strings.TrimSpace(request.ToolCallID) == "" {
		return fmt.Errorf("checkpoint: effect commit identity is invalid")
	}
	return nil
}

type EffectJournal interface {
	BeginEffect(context.Context, BeginEffectRequest) (Effect, error)
	CommitEffect(context.Context, CommitEffectRequest) (Effect, error)
	ListEffects(context.Context, Identity, int) ([]Effect, error)
}

type EventType string

const (
	EventSessionStarted       EventType = "session_started"
	EventCheckpointRequested  EventType = "checkpoint_requested"
	EventCheckpointStarted    EventType = "checkpoint_started"
	EventCheckpointCommitted  EventType = "checkpoint_committed"
	EventCheckpointFailed     EventType = "checkpoint_failed"
	EventEffectBegun          EventType = "effect_begun"
	EventEffectCommitted      EventType = "effect_committed"
	EventInterrupted          EventType = "interrupted"
	EventResumeAttempted      EventType = "resume_attempted"
	EventResumeNative         EventType = "resume_native"
	EventResumeWorkspaceOnly  EventType = "resume_workspace_only"
	EventResumeCheckpointZero EventType = "resume_checkpoint_zero"
	EventManualReviewRequired EventType = "manual_review_required"
	EventSessionCompleted     EventType = "session_completed"
)

const MaxEventDetailsBytes = 16 * 1024

type RunEvent struct {
	Identity             Identity
	ExecutionAttempt     int
	Type                 EventType
	Reason               string
	CheckpointGeneration int
	Details              json.RawMessage
	CreatedAt            time.Time
}

func (event RunEvent) Validate() error {
	if err := event.Identity.Validate(); err != nil {
		return err
	}
	if event.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: event execution attempt must be positive")
	}
	if !validEventType(event.Type) {
		return fmt.Errorf("checkpoint: invalid event type %q", event.Type)
	}
	if len(event.Reason) > 512 {
		return fmt.Errorf("checkpoint: event reason exceeds 512 bytes")
	}
	if event.CheckpointGeneration < 0 {
		return fmt.Errorf("checkpoint: event checkpoint generation cannot be negative")
	}
	if len(event.Details) == 0 {
		return nil
	}
	if len(event.Details) > MaxEventDetailsBytes {
		return fmt.Errorf("checkpoint: event details exceed %d bytes", MaxEventDetailsBytes)
	}
	if !json.Valid(event.Details) || event.Details[0] != '{' {
		return fmt.Errorf("checkpoint: event details must be a JSON object")
	}
	return nil
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventSessionStarted, EventCheckpointRequested, EventCheckpointStarted,
		EventCheckpointCommitted, EventCheckpointFailed, EventEffectBegun,
		EventEffectCommitted, EventInterrupted, EventResumeAttempted,
		EventResumeNative, EventResumeWorkspaceOnly, EventResumeCheckpointZero,
		EventManualReviewRequired, EventSessionCompleted:
		return true
	default:
		return false
	}
}

type EventRecorder interface {
	Record(context.Context, RunEvent) error
}
