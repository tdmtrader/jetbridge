// Package agentchildexecutions is ATC's narrow authority for broker-worker
// admission, lifecycle persistence, and terminal snapshot binding.
package agentchildexecutions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/google/uuid"
)

type Scope struct {
	TeamID               int                             `json:"team_id"`
	TeamName             string                          `json:"team_name"`
	BuildID              int                             `json:"build_id"`
	SnapshotCreatedBy    string                          `json:"snapshot_created_by"`
	WorkflowDefinitionID int                             `json:"workflow_definition_id"`
	WorkflowRunID        int64                           `json:"workflow_run_id"`
	NodePlanID           string                          `json:"node_plan_id"`
	ParentAttempt        int                             `json:"parent_attempt"`
	BrokerInstance       string                          `json:"broker_instance"`
	LeaseDuration        time.Duration                   `json:"lease_duration"`
	Inputs               map[string]snapshot.SnapshotRef `json:"inputs"`
	WorkspaceBase        *snapshot.SnapshotRef           `json:"workspace_base,omitempty"`
}

type ExecutionStore interface {
	Create(context.Context, string, broker.ExecutionIdentity) (db.AgentChildExecution, error)
	Advance(context.Context, db.AdvanceAgentChildExecution) (db.AgentChildExecution, error)
	Find(context.Context, int, string) (db.AgentChildExecution, bool, error)
}

type ResultSealer interface {
	Seal(context.Context, Scope, string, broker.ExecutionIdentity, CandidateResult) (SealedResult, error)
}
type WorkspaceResultSealer interface {
	SealWorkspace(context.Context, Scope, string, broker.ExecutionIdentity, broker.WorkspaceCapture) (snapshot.SnapshotRef, error)
}
type WorkspaceExecutionStore interface {
	BindWorkspace(context.Context, db.BindAgentChildWorkspace) (db.AgentChildExecution, error)
}
type SealedResult struct {
	Snapshot snapshot.SnapshotRef
	Body     json.RawMessage
}

type Config struct {
	Scope   Scope
	Catalog *broker.Catalog
	Store   ExecutionStore
	Sealer  ResultSealer
}

type Service struct {
	config Config
}

func NewService(config Config) (*Service, error) {
	if err := config.Scope.Validate(); err != nil {
		return nil, fmt.Errorf("agent child authority: %w", err)
	}
	if config.Catalog == nil || config.Store == nil || config.Sealer == nil {
		return nil, fmt.Errorf("agent child authority: catalog, store, and sealer are required")
	}
	return &Service{config: config}, nil
}

func (service *Service) Admit(
	ctx context.Context,
	request broker.AdmissionRequest,
) (broker.Admission, error) {
	if err := broker.ValidateAttachments(request.Tool, request.Attachments); err != nil {
		return broker.Admission{}, fmt.Errorf("agent child authority: invalid attachments: %w", err)
	}
	for _, attachment := range request.Attachments {
		if request.Tool == broker.ToolRequestReview && attachment == "workspace" &&
			service.config.Scope.WorkspaceBase != nil &&
			service.config.Scope.WorkspaceBase.Type == "repository/v1" &&
			service.config.Scope.WorkspaceBase.Validate() == nil {
			continue
		}
		ref, found := service.config.Scope.Inputs[attachment]
		if !found || ref.Validate() != nil {
			return broker.Admission{}, fmt.Errorf("agent child authority: immutable input authority %q is unavailable", attachment)
		}
	}
	resolved, err := service.config.Catalog.Resolve(request.Tool, request.Selector)
	if err != nil {
		return broker.Admission{}, err
	}
	if resolved.ID != request.ProfileID || resolved.Digest != request.ProfileDigest {
		return broker.Admission{}, fmt.Errorf("agent child authority: exact profile resolution mismatch")
	}
	identity := broker.ExecutionIdentity{
		TeamID: service.config.Scope.TeamID, WorkflowRunID: service.config.Scope.WorkflowRunID,
		NodePlanID: service.config.Scope.NodePlanID, ParentAttempt: service.config.Scope.ParentAttempt,
		IdempotencyKey: request.IdempotencyKey, Tool: request.Tool, Selector: request.Selector,
		ProfileID: request.ProfileID, ProfileDigest: request.ProfileDigest,
		InputDigest: request.InputDigest, Attachments: append([]string(nil), request.Attachments...),
	}
	execution, err := service.config.Store.Create(ctx, uuid.NewString(), identity)
	if err != nil {
		return broker.Admission{}, err
	}
	if replay, found, err := admissionReplay(execution); err != nil {
		return broker.Admission{}, err
	} else if found {
		return broker.Admission{ExecutionID: execution.ID, Succeeded: replay.succeeded, Terminal: replay.terminal}, nil
	}
	if execution.State != broker.ExecutionPending {
		return broker.Admission{}, fmt.Errorf("agent child authority: execution is already in progress")
	}
	if execution.State == broker.ExecutionPending {
		execution, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
			ID: execution.ID, TeamID: service.config.Scope.TeamID,
			ExpectedSequence: execution.Sequence, State: broker.ExecutionAdmitted,
			Phase: "admitted", BrokerInstance: service.config.Scope.BrokerInstance,
			LeaseExpiresAt: time.Now().Add(service.config.Scope.LeaseDuration),
		})
		if err != nil {
			return broker.Admission{}, err
		}
	}
	return broker.Admission{ExecutionID: execution.ID}, nil
}

type replayAdmission struct {
	succeeded *broker.SucceededReplay
	terminal  *broker.Terminal
}

func admissionReplay(execution db.AgentChildExecution) (replayAdmission, bool, error) {
	switch execution.State {
	case broker.ExecutionSucceeded:
		if execution.ResultSnapshot == nil || len(execution.ResultBody) == 0 {
			return replayAdmission{}, false, fmt.Errorf("agent child authority: succeeded execution lacks durable result")
		}
		return replayAdmission{succeeded: &broker.SucceededReplay{Snapshot: *execution.ResultSnapshot, Body: append([]byte(nil), execution.ResultBody...), Duration: durationFromMS(execution.DurationMS), InputTokens: usageInt(execution.ObservedUsage, "input_tokens"), OutputTokens: usageInt(execution.ObservedUsage, "output_tokens"), CostUSD: usageFloat(execution.ObservedUsage, "cost_usd")}}, true, nil
	case broker.ExecutionErrored, broker.ExecutionCancelled, broker.ExecutionTimedOut:
		if execution.ErrorRetryable == nil {
			return replayAdmission{}, false, fmt.Errorf("agent child authority: terminal execution lacks durable error")
		}
		return replayAdmission{terminal: &broker.Terminal{State: execution.State, Code: execution.ErrorCode, Retryable: *execution.ErrorRetryable, Summary: execution.ErrorSummary}}, true, nil
	default:
		return replayAdmission{}, false, nil
	}
}
func durationFromMS(value *int64) time.Duration {
	if value == nil {
		return 0
	}
	return time.Duration(*value) * time.Millisecond
}
func usageInt(raw json.RawMessage, key string) *int64 {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	var output int64
	if value[key] == nil || json.Unmarshal(value[key], &output) != nil {
		return nil
	}
	return &output
}
func usageFloat(raw json.RawMessage, key string) *float64 {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	var output float64
	if value[key] == nil || json.Unmarshal(value[key], &output) != nil {
		return nil
	}
	return &output
}

func (service *Service) Phase(ctx context.Context, executionID, phase string) error {
	state, found := phaseState(phase)
	if !found {
		return fmt.Errorf("agent child authority: unsupported phase %q", phase)
	}
	execution, err := service.find(ctx, executionID)
	if err != nil {
		return err
	}
	update := db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: state, Phase: phase,
		BrokerInstance: service.config.Scope.BrokerInstance,
	}
	update.LeaseExpiresAt = time.Now().Add(service.config.Scope.LeaseDuration)
	_, err = service.config.Store.Advance(ctx, update)
	return err
}

func (service *Service) CaptureWorkspace(ctx context.Context, executionID string, capture broker.WorkspaceCapture) (snapshot.SnapshotRef, error) {
	captureDigest, err := capture.Fingerprint()
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if service.config.Scope.WorkspaceBase == nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace base authority is unavailable")
	}
	execution, err := service.find(ctx, executionID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if execution.Tool != broker.ToolRequestReview {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace capture is unavailable for execution state or tool")
	}
	if execution.WorkspaceSnapshot != nil {
		if execution.WorkspaceCaptureDigest != captureDigest {
			return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace capture replay conflicts with durable capture")
		}
		return *execution.WorkspaceSnapshot, nil
	}
	if execution.State != broker.ExecutionCapturing {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace capture is unavailable for execution state or tool")
	}
	sealer, ok := service.config.Sealer.(WorkspaceResultSealer)
	if !ok {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace sealer is unavailable")
	}
	sealed, err := sealer.SealWorkspace(ctx, service.config.Scope, executionID, execution.ExecutionIdentity, capture)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if sealed.Validate() != nil || sealed.Type != "repository-change/v1" {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace sealer returned invalid result")
	}
	store, ok := service.config.Store.(WorkspaceExecutionStore)
	if !ok {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: workspace binding store is unavailable")
	}
	bound, err := store.BindWorkspace(ctx, db.BindAgentChildWorkspace{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, Snapshot: sealed, CaptureDigest: captureDigest,
	})
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if bound.WorkspaceSnapshot == nil || *bound.WorkspaceSnapshot != sealed {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: durable workspace binding does not match")
	}
	return sealed, nil
}

func (service *Service) FailWorkspaceCapture(ctx context.Context, executionID string) error {
	execution, err := service.find(ctx, executionID)
	if err != nil {
		return err
	}
	if execution.Tool != broker.ToolRequestReview ||
		execution.State != broker.ExecutionCapturing ||
		execution.WorkspaceSnapshot != nil {
		return fmt.Errorf("agent child authority: workspace capture failure is unavailable for execution state")
	}
	contract := terminalContracts["workspace_capture_failed"]
	retryable := contract.retryable
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: contract.state,
		Phase: "workspace_capture_failed", BrokerInstance: service.config.Scope.BrokerInstance,
		ErrorCode: "workspace_capture_failed", ErrorRetryable: &retryable,
		ErrorSummary: contract.summary,
	})
	return err
}

// Update persists only the bounded broker event DTO and observed accounting.
// Native harness payloads remain in the sidecar's protected local transcript.
func (service *Service) Update(ctx context.Context, executionID string, update broker.RunUpdate) error {
	eventsToPersist, err := broker.NormalizeEvents(update.Events)
	if err != nil {
		return fmt.Errorf("agent child authority: validate normalized events: %w", err)
	}
	execution, err := service.find(ctx, executionID)
	if err != nil {
		return err
	}
	if execution.State != broker.ExecutionRunning {
		return fmt.Errorf("agent child authority: execution state is %q, expected running", execution.State)
	}
	events, err := json.Marshal(eventsToPersist)
	if err != nil {
		return fmt.Errorf("agent child authority: encode normalized events: %w", err)
	}
	usage, err := json.Marshal(struct {
		InputTokens  *int64   `json:"input_tokens,omitempty"`
		OutputTokens *int64   `json:"output_tokens,omitempty"`
		CostUSD      *float64 `json:"cost_usd,omitempty"`
	}{update.InputTokens, update.OutputTokens, update.CostUSD})
	if err != nil {
		return fmt.Errorf("agent child authority: encode observed usage: %w", err)
	}
	var duration *int64
	if update.Duration > 0 {
		milliseconds := update.Duration.Milliseconds()
		duration = &milliseconds
	}
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: execution.State, Phase: "observed",
		BrokerInstance: service.config.Scope.BrokerInstance,
		LeaseExpiresAt: time.Now().Add(service.config.Scope.LeaseDuration),
		ObservedUsage:  usage, DurationMS: duration, Detail: events,
	})
	return err
}

func (service *Service) Terminal(ctx context.Context, executionID string, terminal broker.Terminal) error {
	contract, found := terminalContracts[terminal.Code]
	if !found || terminal.State != contract.state || terminal.Retryable != contract.retryable {
		return fmt.Errorf("agent child authority: terminal state, code, or retryability is invalid")
	}
	execution, err := service.find(ctx, executionID)
	if err != nil {
		return err
	}
	if execution.State == terminal.State && execution.ErrorCode == terminal.Code && execution.ErrorRetryable != nil && *execution.ErrorRetryable == terminal.Retryable {
		return nil
	}
	if execution.State == broker.ExecutionErrored || execution.State == broker.ExecutionCancelled || execution.State == broker.ExecutionTimedOut {
		return fmt.Errorf("agent child authority: terminal replay conflicts with durable execution")
	}
	retryable := contract.retryable
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: terminal.State, Phase: string(terminal.State),
		BrokerInstance: service.config.Scope.BrokerInstance,
		ErrorCode:      terminal.Code, ErrorRetryable: &retryable, ErrorSummary: contract.summary,
	})
	return err
}

type terminalContract struct {
	state     broker.ExecutionState
	retryable bool
	summary   string
}

// terminalContracts is intentionally authority-owned. The broker can request
// a stable classification, but never choose the durable state semantics or
// store caller/provider text as an execution summary.
var terminalContracts = map[string]terminalContract{
	"attachment_unknown":       {broker.ExecutionErrored, false, "declared attachments could not be resolved"},
	"credential_unavailable":   {broker.ExecutionErrored, true, "broker credential is unavailable"},
	"provider_rejected":        {broker.ExecutionErrored, true, "provider rejected the child execution"},
	"deadline_exceeded":        {broker.ExecutionTimedOut, true, "child execution exceeded its deadline"},
	"cancelled":                {broker.ExecutionCancelled, true, "child execution was cancelled"},
	"output_invalid":           {broker.ExecutionErrored, false, "child execution returned invalid typed output"},
	"sealing_failed":           {broker.ExecutionErrored, true, "child result could not be sealed"},
	"broker_lost":              {broker.ExecutionErrored, true, "broker lease expired before a terminal result was recorded"},
	"workspace_capture_failed": {broker.ExecutionErrored, false, "workspace could not be captured authoritatively"},
}

func (service *Service) Seal(
	ctx context.Context,
	request broker.SealRequest,
) (snapshot.SnapshotRef, error) {
	execution, err := service.find(ctx, request.ExecutionID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if execution.State == broker.ExecutionSucceeded {
		replay, found, err := admissionReplay(execution)
		if err != nil || !found || replay.succeeded == nil || !bytes.Equal(replay.succeeded.Body, request.Body) {
			return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: sealed result replay does not match")
		}
		return replay.succeeded.Snapshot, nil
	}
	if execution.State != broker.ExecutionSealing {
		return snapshot.SnapshotRef{}, fmt.Errorf(
			"agent child authority: execution state is %q, expected sealing", execution.State)
	}
	sealed, err := service.config.Sealer.Seal(ctx, service.config.Scope, request.ExecutionID, execution.ExecutionIdentity, CandidateResult{Body: append([]byte(nil), request.Body...)})
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	resultType, err := resultTypeForTool(execution.Tool)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if sealed.Snapshot.ID <= 0 || sealed.Snapshot.Type != resultType || len(sealed.Body) == 0 {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: sealer returned invalid result identity")
	}
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: broker.ExecutionSucceeded,
		Phase: "succeeded", ResultSnapshotID: int64(sealed.Snapshot.ID), ResultSnapshot: &sealed.Snapshot, ResultBody: append([]byte(nil), sealed.Body...),
	})
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	return sealed.Snapshot, nil
}

func phaseState(phase string) (broker.ExecutionState, bool) {
	switch phase {
	case "capturing":
		return broker.ExecutionCapturing, true
	case "running":
		return broker.ExecutionRunning, true
	case "validating":
		return broker.ExecutionValidating, true
	case "sealing":
		return broker.ExecutionSealing, true
	default:
		return "", false
	}
}

func (service *Service) find(ctx context.Context, executionID string) (db.AgentChildExecution, error) {
	execution, found, err := service.config.Store.Find(ctx, service.config.Scope.TeamID, executionID)
	if err != nil {
		return db.AgentChildExecution{}, err
	}
	if !found || execution.TeamID != service.config.Scope.TeamID || execution.WorkflowRunID != service.config.Scope.WorkflowRunID || execution.NodePlanID != service.config.Scope.NodePlanID || execution.ParentAttempt != service.config.Scope.ParentAttempt || execution.BrokerInstance != service.config.Scope.BrokerInstance {
		return db.AgentChildExecution{}, fmt.Errorf("agent child authority: execution not found")
	}
	return execution, nil
}

var _ broker.Authority = (*Service)(nil)
