// Package agentchildexecutions is ATC's narrow authority for broker-worker
// admission, lifecycle persistence, and terminal snapshot binding.
package agentchildexecutions

import (
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
	TeamID            int                             `json:"team_id"`
	TeamName          string                          `json:"team_name"`
	BuildID           int                             `json:"build_id"`
	SnapshotCreatedBy string                          `json:"snapshot_created_by"`
	WorkflowRunID     int64                           `json:"workflow_run_id"`
	NodePlanID        string                          `json:"node_plan_id"`
	ParentAttempt     int                             `json:"parent_attempt"`
	BrokerInstance    string                          `json:"broker_instance"`
	LeaseDuration     time.Duration                   `json:"lease_duration"`
	Inputs            map[string]snapshot.SnapshotRef `json:"inputs"`
}

type ExecutionStore interface {
	Create(context.Context, string, broker.ExecutionIdentity) (db.AgentChildExecution, error)
	Advance(context.Context, db.AdvanceAgentChildExecution) (db.AgentChildExecution, error)
	Find(context.Context, int, string) (db.AgentChildExecution, bool, error)
}

type ResultSealer interface {
	Seal(context.Context, Scope, broker.ExecutionIdentity, CandidateResult) (snapshot.SnapshotRef, error)
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
) (string, error) {
	if err := broker.ValidateAttachments(request.Tool, request.Attachments); err != nil {
		return "", fmt.Errorf("agent child authority: invalid attachments: %w", err)
	}
	resolved, err := service.config.Catalog.Resolve(request.Tool, request.Selector)
	if err != nil {
		return "", err
	}
	if resolved.ID != request.ProfileID || resolved.Digest != request.ProfileDigest {
		return "", fmt.Errorf("agent child authority: exact profile resolution mismatch")
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
		return "", err
	}
	if execution.State == broker.ExecutionPending {
		execution, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
			ID: execution.ID, TeamID: service.config.Scope.TeamID,
			ExpectedSequence: execution.Sequence, State: broker.ExecutionAdmitted,
			Phase: "admitted", BrokerInstance: service.config.Scope.BrokerInstance,
			LeaseExpiresAt: time.Now().Add(service.config.Scope.LeaseDuration),
		})
		if err != nil {
			return "", err
		}
	}
	return execution.ID, nil
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
	"attachment_unknown":     {broker.ExecutionErrored, false, "declared attachments could not be resolved"},
	"credential_unavailable": {broker.ExecutionErrored, true, "broker credential is unavailable"},
	"provider_rejected":      {broker.ExecutionErrored, true, "provider rejected the child execution"},
	"deadline_exceeded":      {broker.ExecutionTimedOut, true, "child execution exceeded its deadline"},
	"cancelled":              {broker.ExecutionCancelled, true, "child execution was cancelled"},
	"output_invalid":         {broker.ExecutionErrored, false, "child execution returned invalid typed output"},
	"sealing_failed":         {broker.ExecutionErrored, true, "child result could not be sealed"},
	"broker_lost":            {broker.ExecutionErrored, true, "broker lease expired before a terminal result was recorded"},
}

func (service *Service) Seal(
	ctx context.Context,
	request broker.SealRequest,
) (snapshot.SnapshotRef, error) {
	execution, err := service.find(ctx, request.ExecutionID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if execution.State != broker.ExecutionSealing {
		return snapshot.SnapshotRef{}, fmt.Errorf(
			"agent child authority: execution state is %q, expected sealing", execution.State)
	}
	sealed, err := service.config.Sealer.Seal(ctx, service.config.Scope, execution.ExecutionIdentity, CandidateResult{Body: append([]byte(nil), request.Body...)})
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	resultType, err := resultTypeForTool(execution.Tool)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if sealed.ID <= 0 || sealed.Type != resultType {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: sealer returned invalid result identity")
	}
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: broker.ExecutionSucceeded,
		Phase: "succeeded", ResultSnapshotID: int64(sealed.ID),
	})
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	return sealed, nil
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
