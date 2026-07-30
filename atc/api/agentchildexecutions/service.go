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
	TeamID         int
	WorkflowRunID  int64
	NodePlanID     string
	ParentAttempt  int
	BrokerInstance string
	LeaseDuration  time.Duration
}

type ExecutionStore interface {
	Create(context.Context, string, broker.ExecutionIdentity) (db.AgentChildExecution, error)
	Advance(context.Context, db.AdvanceAgentChildExecution) (db.AgentChildExecution, error)
	Find(context.Context, int, string) (db.AgentChildExecution, bool, error)
}

type ResultSealer interface {
	Seal(context.Context, Scope, broker.SealRequest) (snapshot.SnapshotRef, error)
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
	if config.Scope.TeamID <= 0 || config.Scope.WorkflowRunID <= 0 ||
		config.Scope.ParentAttempt <= 0 || config.Scope.NodePlanID == "" {
		return nil, fmt.Errorf("agent child authority: complete execution scope is required")
	}
	if config.Scope.BrokerInstance == "" || config.Scope.LeaseDuration <= 0 {
		return nil, fmt.Errorf("agent child authority: broker instance and lease duration are required")
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
	execution, found, err := service.config.Store.Find(ctx, service.config.Scope.TeamID, executionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent child authority: execution not found")
	}
	update := db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: state, Phase: phase,
		BrokerInstance: service.config.Scope.BrokerInstance,
	}
	if state != broker.ExecutionErrored && state != broker.ExecutionCancelled && state != broker.ExecutionTimedOut {
		update.LeaseExpiresAt = time.Now().Add(service.config.Scope.LeaseDuration)
	}
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
	execution, found, err := service.config.Store.Find(ctx, service.config.Scope.TeamID, executionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent child authority: execution not found")
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
	if terminal.State != broker.ExecutionErrored && terminal.State != broker.ExecutionCancelled &&
		terminal.State != broker.ExecutionTimedOut {
		return fmt.Errorf("agent child authority: terminal state %q is invalid", terminal.State)
	}
	if terminal.Code == "" || terminal.Summary == "" {
		return fmt.Errorf("agent child authority: terminal code and summary are required")
	}
	execution, found, err := service.config.Store.Find(ctx, service.config.Scope.TeamID, executionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent child authority: execution not found")
	}
	retryable := terminal.Retryable
	_, err = service.config.Store.Advance(ctx, db.AdvanceAgentChildExecution{
		ID: execution.ID, TeamID: service.config.Scope.TeamID,
		ExpectedSequence: execution.Sequence, State: terminal.State, Phase: string(terminal.State),
		BrokerInstance: service.config.Scope.BrokerInstance,
		ErrorCode:      terminal.Code, ErrorRetryable: &retryable, ErrorSummary: terminal.Summary,
	})
	return err
}

func (service *Service) Seal(
	ctx context.Context,
	request broker.SealRequest,
) (snapshot.SnapshotRef, error) {
	execution, found, err := service.config.Store.Find(
		ctx, service.config.Scope.TeamID, request.ExecutionID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if !found {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: execution not found")
	}
	if execution.State != broker.ExecutionSealing {
		return snapshot.SnapshotRef{}, fmt.Errorf(
			"agent child authority: execution state is %q, expected sealing", execution.State)
	}
	if execution.ProfileID != request.Profile.ID ||
		execution.ProfileDigest != request.Profile.Digest {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child authority: result profile mismatch")
	}
	sealed, err := service.config.Sealer.Seal(ctx, service.config.Scope, request)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if sealed.ID <= 0 || sealed.Type != request.ResultType {
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
	case "errored":
		return broker.ExecutionErrored, true
	case "cancelled":
		return broker.ExecutionCancelled, true
	case "timed_out":
		return broker.ExecutionTimedOut, true
	default:
		return "", false
	}
}

var _ broker.Authority = (*Service)(nil)
