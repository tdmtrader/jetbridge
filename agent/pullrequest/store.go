package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	MaxLaunchReservationTTL = 15 * time.Minute
	maxAttentionReasonBytes = 2048
	maxAuditPageSize        = 200
)

var (
	ErrBindingConflict      = errors.New("pullrequest: binding conflict")
	ErrBindingNotFound      = errors.New("pullrequest: binding not found")
	ErrStaleBindingRevision = errors.New("pullrequest: stale binding revision")
	ErrBindingImmutable     = errors.New("pullrequest: binding is immutable")
	ErrBindingBusy          = errors.New("pullrequest: binding has an active action")
	ErrReservationMismatch  = errors.New("pullrequest: launch reservation mismatch")
)

// BindingState is provider evidence plus the one recoverable attention state.
// Pause and operator termination are separate operator intents and can never
// manufacture completed or abandoned provider evidence.
type BindingState string

const (
	BindingActive            BindingState = "active"
	BindingAttentionRequired BindingState = "attention_required"
	BindingCompleted         BindingState = "completed"
	BindingAbandoned         BindingState = "abandoned"
)

func (state BindingState) Terminal() bool {
	return state == BindingCompleted || state == BindingAbandoned
}

func (state BindingState) Validate() error {
	switch state {
	case BindingActive, BindingAttentionRequired, BindingCompleted, BindingAbandoned:
		return nil
	default:
		return fmt.Errorf("pullrequest: invalid binding state %q", state)
	}
}

// Binding is the mutable coordination projection for one provider-native PR.
// Immutable observations and publication evidence remain snapshots and
// publication occurrences; this row only serializes their processing.
type Binding struct {
	ID                               int64
	TeamID                           int
	Locator                          Locator
	URL                              string
	SourceRef                        string
	TargetRef                        string
	OriginatingWorkflowRunID         *snapshot.WorkflowRunID
	OriginatingPublicationOccurrence *int64
	MonitorWorkflowDefinitionID      int
	MonitorWorkflowVersion           int
	PipelineID                       *int
	AcknowledgedCursor               Cursor
	LastObservationSnapshotID        *snapshot.SnapshotID
	LastAcknowledgedActionDigest     string
	LastAcknowledgedWorkflowRunID    *snapshot.WorkflowRunID
	LastReconciledSourceSHA          string
	LastReconciledTargetSHA          string
	LastReconciledAt                 time.Time
	State                            BindingState
	AttentionReason                  string
	Paused                           bool
	OperatorTerminated               bool
	ObservationRequestedAt           *time.Time
	Active                           *LaunchReservation
	TerminalObservationSnapshotID    *snapshot.SnapshotID
	TerminalAt                       *time.Time
	Revision                         int64
	CreatedAt                        time.Time
	UpdatedAt                        time.Time
}

type CreateBinding struct {
	TeamID                           int
	Locator                          Locator
	URL                              string
	SourceRef                        string
	TargetRef                        string
	OriginatingWorkflowRunID         snapshot.WorkflowRunID
	OriginatingPublicationOccurrence int64
	MonitorWorkflowDefinitionID      int
	MonitorWorkflowVersion           int
	AcknowledgedCursor               Cursor
	LastObservationSnapshotID        snapshot.SnapshotID
	LastReconciledSourceSHA          string
	LastReconciledTargetSHA          string
	LastReconciledAt                 time.Time
}

func (request CreateBinding) Validate() error {
	if request.TeamID <= 0 || request.MonitorWorkflowDefinitionID <= 0 ||
		request.MonitorWorkflowVersion <= 0 {
		return fmt.Errorf("pullrequest: binding requires positive team and monitor definition identities")
	}
	if err := request.Locator.validate(contracts.PullRequestActive); err != nil {
		return fmt.Errorf("pullrequest: binding locator: %w", err)
	}
	if err := validateURL(request.URL); err != nil {
		return fmt.Errorf("pullrequest: binding %w", err)
	}
	if err := validateRef("source ref", request.SourceRef); err != nil {
		return err
	}
	if err := validateRef("target ref", request.TargetRef); err != nil {
		return err
	}
	if err := request.AcknowledgedCursor.Validate(); err != nil || request.AcknowledgedCursor == "" {
		return fmt.Errorf("pullrequest: binding requires a non-empty valid acknowledged cursor")
	}
	if err := validateObjectID("last reconciled source sha", request.LastReconciledSourceSHA); err != nil {
		return err
	}
	if err := validateObjectID("last reconciled target sha", request.LastReconciledTargetSHA); err != nil {
		return err
	}
	if request.LastReconciledAt.IsZero() {
		return fmt.Errorf("pullrequest: binding requires a reconciliation time")
	}
	if request.OriginatingWorkflowRunID <= 0 ||
		request.OriginatingPublicationOccurrence <= 0 ||
		request.LastObservationSnapshotID <= 0 {
		return fmt.Errorf("pullrequest: binding requires exact originating run, publication, and observation evidence")
	}
	return nil
}

// ReserveLaunch is the exact immutable action selected from one observation.
// ExpectedRevision is the binding revision projected into the selected source
// version; ExpiresIn bounds only an unattached reservation.
type ReserveLaunch struct {
	TeamID                int
	BindingID             int64
	ExpectedRevision      int64
	ActionDigest          string
	ObservationSnapshotID snapshot.SnapshotID
	Cursor                Cursor
	SourceSHA             string
	TargetSHA             string
	ExpiresIn             time.Duration
}

func (request ReserveLaunch) Validate() error {
	if request.TeamID <= 0 || request.BindingID <= 0 || request.ExpectedRevision <= 0 ||
		request.ObservationSnapshotID <= 0 {
		return fmt.Errorf("pullrequest: reservation requires positive binding identities and revision")
	}
	if !isDigest(request.ActionDigest) {
		return fmt.Errorf("pullrequest: reservation action must be a sha256 digest")
	}
	if err := request.Cursor.Validate(); err != nil || request.Cursor == "" {
		return fmt.Errorf("pullrequest: reservation requires a non-empty valid cursor")
	}
	if err := validateObjectID("reservation source sha", request.SourceSHA); err != nil {
		return err
	}
	if err := validateObjectID("reservation target sha", request.TargetSHA); err != nil {
		return err
	}
	if request.ExpiresIn <= 0 || request.ExpiresIn > MaxLaunchReservationTTL {
		return fmt.Errorf("pullrequest: reservation expiry must be positive and at most %s", MaxLaunchReservationTTL)
	}
	return nil
}

type LaunchReservation struct {
	BindingID             int64
	BindingRevision       int64
	BaseRevision          int64
	ActionDigest          string
	ObservationSnapshotID snapshot.SnapshotID
	Cursor                Cursor
	SourceSHA             string
	TargetSHA             string
	Token                 string
	ExpiresAt             time.Time
	WorkflowRunID         *snapshot.WorkflowRunID
}

type AttachRun struct {
	TeamID           int
	BindingID        int64
	ExpectedRevision int64
	ActionDigest     string
	ReservationToken string
	WorkflowRunID    snapshot.WorkflowRunID
}

func (request AttachRun) Validate() error {
	if request.TeamID <= 0 || request.BindingID <= 0 || request.ExpectedRevision <= 0 ||
		request.WorkflowRunID <= 0 || !isDigest(request.ActionDigest) ||
		strings.TrimSpace(request.ReservationToken) == "" || len(request.ReservationToken) > 128 {
		return fmt.Errorf("pullrequest: invalid run attachment")
	}
	return nil
}

// ReleaseLaunch clears one exact reservation after launch failed before
// attachment, or after its attached run ended unsuccessfully. A nil
// WorkflowRunID selects only an unattached reservation.
type ReleaseLaunch struct {
	TeamID           int
	BindingID        int64
	ExpectedRevision int64
	ActionDigest     string
	ReservationToken string
	WorkflowRunID    *snapshot.WorkflowRunID
}

func (request ReleaseLaunch) Validate() error {
	if request.TeamID <= 0 || request.BindingID <= 0 || request.ExpectedRevision <= 0 ||
		!isDigest(request.ActionDigest) ||
		strings.TrimSpace(request.ReservationToken) != request.ReservationToken ||
		request.ReservationToken == "" || len(request.ReservationToken) > 128 ||
		(request.WorkflowRunID != nil && *request.WorkflowRunID <= 0) {
		return fmt.Errorf("pullrequest: invalid launch release")
	}
	return nil
}

type AcknowledgeAction struct {
	TeamID                int
	BindingID             int64
	ExpectedRevision      int64
	ActionDigest          string
	ReservationToken      string
	WorkflowRunID         snapshot.WorkflowRunID
	ObservationSnapshotID snapshot.SnapshotID
	Cursor                Cursor
	SourceSHA             string
	TargetSHA             string
}

func (request AcknowledgeAction) Validate() error {
	if request.TeamID <= 0 || request.BindingID <= 0 || request.ExpectedRevision <= 0 ||
		request.WorkflowRunID <= 0 || request.ObservationSnapshotID <= 0 ||
		!isDigest(request.ActionDigest) || strings.TrimSpace(request.ReservationToken) == "" ||
		len(request.ReservationToken) > 128 {
		return fmt.Errorf("pullrequest: invalid action acknowledgement")
	}
	if err := request.Cursor.Validate(); err != nil || request.Cursor == "" {
		return fmt.Errorf("pullrequest: acknowledgement requires a non-empty valid cursor")
	}
	if err := validateObjectID("acknowledged source sha", request.SourceSHA); err != nil {
		return err
	}
	return validateObjectID("acknowledged target sha", request.TargetSHA)
}

type TerminalBinding struct {
	TeamID                int
	BindingID             int64
	ExpectedRevision      int64
	State                 BindingState
	ActionDigest          string
	ReservationToken      string
	WorkflowRunID         snapshot.WorkflowRunID
	ObservationSnapshotID snapshot.SnapshotID
	Cursor                Cursor
	SourceSHA             string
	TargetSHA             string
}

func (request TerminalBinding) Validate() error {
	if !request.State.Terminal() {
		return fmt.Errorf("pullrequest: terminal binding requires completed or abandoned provider state")
	}
	ack := AcknowledgeAction{
		TeamID: request.TeamID, BindingID: request.BindingID,
		ExpectedRevision: request.ExpectedRevision, ActionDigest: request.ActionDigest,
		ReservationToken: request.ReservationToken, WorkflowRunID: request.WorkflowRunID,
		ObservationSnapshotID: request.ObservationSnapshotID, Cursor: request.Cursor,
		SourceSHA: request.SourceSHA, TargetSHA: request.TargetSHA,
	}
	return ack.Validate()
}

type OperatorRequest struct {
	TeamID           int
	BindingID        int64
	ExpectedRevision int64
}

func (request OperatorRequest) Validate() error {
	if request.TeamID <= 0 || request.BindingID <= 0 || request.ExpectedRevision <= 0 {
		return fmt.Errorf("pullrequest: invalid operator request")
	}
	return nil
}

type AuditFilter struct {
	BeforeWorkflowRunID snapshot.WorkflowRunID
	Limit               int
}

func (filter AuditFilter) Normalized() (AuditFilter, error) {
	if filter.BeforeWorkflowRunID < 0 || filter.Limit < 0 || filter.Limit > maxAuditPageSize {
		return AuditFilter{}, fmt.Errorf("pullrequest: invalid audit filter")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	return filter, nil
}

// AuditEntry is a safe run-level projection. Snapshot, wait, outcome, and
// publication details are joined by the API from their existing immutable
// stores rather than copied into a PR-specific event table.
type AuditEntry struct {
	WorkflowRunID   snapshot.WorkflowRunID
	OriginKind      string
	OriginReference string
	Status          string
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

type BindingStore interface {
	Create(context.Context, CreateBinding) (Binding, bool, error)
	Get(context.Context, int, int64) (Binding, bool, error)
	GetByExternal(context.Context, int, Locator) (Binding, bool, error)
	ReserveLaunch(context.Context, ReserveLaunch) (LaunchReservation, bool, error)
	AttachRun(context.Context, AttachRun) (Binding, error)
	ReleaseLaunch(context.Context, ReleaseLaunch) (Binding, error)
	AcknowledgeAction(context.Context, AcknowledgeAction) (Binding, error)
	MarkAttention(context.Context, int, int64, string) (Binding, error)
	MarkTerminal(context.Context, TerminalBinding) (Binding, error)
	RequestObservation(context.Context, OperatorRequest) (Binding, error)
	Pause(context.Context, OperatorRequest) (Binding, error)
	Resume(context.Context, OperatorRequest) (Binding, error)
	Terminate(context.Context, OperatorRequest) (Binding, error)
	ListAudit(context.Context, int, int64, AuditFilter) ([]AuditEntry, error)
	ListActive(context.Context, int) ([]Binding, error)
}

func monitorOriginReference(bindingID int64) string {
	return strconv.FormatInt(bindingID, 10)
}
