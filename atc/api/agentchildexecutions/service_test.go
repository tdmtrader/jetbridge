package agentchildexecutions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/db"
)

func TestServiceVerifiesFrozenResolutionAndOwnsTerminalBinding(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{authorityProfile()})
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, broker.Selector{
		Tier: broker.TierBalanced, Effort: broker.EffortHigh,
	})
	store := &fakeStore{}
	sealer := &fakeSealer{}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{
		Scope: agentchildexecutions.Scope{
			TeamID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute,
		},
		Catalog: catalog, Store: store, Sealer: sealer,
	})
	if err != nil {
		t.Fatal(err)
	}
	admission := broker.AdmissionRequest{
		IdempotencyKey: "call", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	}
	executionID, err := service.Admit(context.Background(), admission)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	if executionID == "" || store.identity.ProfileDigest != resolved.Digest ||
		store.advances[0].State != broker.ExecutionAdmitted {
		t.Fatalf("admission = %#v advances=%#v", store.identity, store.advances)
	}

	store.execution.State = broker.ExecutionSealing
	store.execution.Sequence = 3
	sealed, err := service.Seal(context.Background(), broker.SealRequest{
		ExecutionID: executionID, ResultType: "consultation/v1", Profile: resolved,
	})
	if err != nil {
		t.Fatalf("Seal(): %v", err)
	}
	last := store.advances[len(store.advances)-1]
	if sealed.ID != 99 || last.State != broker.ExecutionSucceeded ||
		last.ResultSnapshotID != 99 {
		t.Fatalf("seal = %#v advance=%#v", sealed, last)
	}

	admission.ProfileDigest = "sha256:" + strings.Repeat("d", 64)
	if _, err := service.Admit(context.Background(), admission); err == nil ||
		!strings.Contains(err.Error(), "profile") {
		t.Fatalf("profile mismatch error = %v", err)
	}
}

func TestServiceRejectsUnsafeEventsAndPersistsExactTerminalFailure(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{authorityProfile()})
	store := &fakeStore{}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{
		Scope: agentchildexecutions.Scope{
			TeamID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	executionID, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Phase(context.Background(), executionID, "running"); err != nil {
		t.Fatal(err)
	}
	if err := service.Update(context.Background(), executionID, broker.RunUpdate{
		Events: []broker.Event{{Kind: "native TOKEN=super-secret"}},
	}); err == nil || !strings.Contains(err.Error(), "event") {
		t.Fatalf("Update() error = %v, want unsafe event rejection", err)
	}
	if err := service.Terminal(context.Background(), executionID, broker.Terminal{
		State: broker.ExecutionTimedOut, Code: "deadline_exceeded", Retryable: true,
		Summary: "child execution exceeded its deadline",
	}); err != nil {
		t.Fatalf("Terminal(): %v", err)
	}
	last := store.advances[len(store.advances)-1]
	if last.State != broker.ExecutionTimedOut || last.ErrorCode != "deadline_exceeded" ||
		last.ErrorSummary != "child execution exceeded its deadline" || last.ErrorRetryable == nil || !*last.ErrorRetryable {
		t.Fatalf("terminal advance = %#v", last)
	}
}

func TestServiceOwnsClosedTerminalContractAndSafeSummary(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{authorityProfile()})
	store := &fakeStore{}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{
		Scope: agentchildexecutions.Scope{
			TeamID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	executionID, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "closed-terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Terminal(context.Background(), executionID, broker.Terminal{
		State: broker.ExecutionErrored, Code: "provider body", Retryable: false, Summary: "provider body",
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Terminal() error = %v, want closed-contract rejection", err)
	}
	if err := service.Terminal(context.Background(), executionID, broker.Terminal{
		State: broker.ExecutionTimedOut, Code: "deadline_exceeded", Retryable: false, Summary: "provider body",
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Terminal() error = %v, want retryability rejection", err)
	}
	if err := service.Terminal(context.Background(), executionID, broker.Terminal{
		State: broker.ExecutionTimedOut, Code: "deadline_exceeded", Retryable: true, Summary: "provider body",
	}); err != nil {
		t.Fatalf("Terminal(): %v", err)
	}
	last := store.advances[len(store.advances)-1]
	if last.ErrorSummary != "child execution exceeded its deadline" || strings.Contains(last.ErrorSummary, "provider body") {
		t.Fatalf("terminal summary = %q", last.ErrorSummary)
	}
}

func TestServiceRejectsTerminalTransitionsThroughGenericPhase(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{authorityProfile()})
	store := &fakeStore{}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{
		Scope: agentchildexecutions.Scope{
			TeamID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	executionID, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "phase-terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Phase(context.Background(), executionID, "errored"); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Phase() error = %v, want terminal phase rejection", err)
	}
}

type fakeStore struct {
	identity  broker.ExecutionIdentity
	execution db.AgentChildExecution
	advances  []db.AdvanceAgentChildExecution
}

func (store *fakeStore) Create(_ context.Context, id string, identity broker.ExecutionIdentity) (db.AgentChildExecution, error) {
	store.identity = identity
	store.execution = db.AgentChildExecution{
		ID: id, ExecutionIdentity: identity, State: broker.ExecutionPending,
	}
	return store.execution, nil
}
func (store *fakeStore) Advance(_ context.Context, request db.AdvanceAgentChildExecution) (db.AgentChildExecution, error) {
	store.advances = append(store.advances, request)
	store.execution.State = request.State
	store.execution.Sequence++
	return store.execution, nil
}
func (store *fakeStore) Find(context.Context, int, string) (db.AgentChildExecution, bool, error) {
	return store.execution, true, nil
}

type fakeSealer struct{}

func (*fakeSealer) Seal(context.Context, agentchildexecutions.Scope, broker.SealRequest) (snapshot.SnapshotRef, error) {
	return snapshot.SnapshotRef{
		ID: 99, Type: "consultation/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("e", 64)),
	}, nil
}

func authorityProfile() broker.Profile {
	return broker.Profile{
		ID: "profile", Revision: 1,
		Selector:     broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:        []broker.Tool{broker.ToolConsultAgent},
		WorkerImage:  "registry/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:      broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:     broker.ProviderSpec{Name: "provider", Model: "model"},
		NativeEffort: "high", InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot: "shared",
		Limits:         broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeOutputSchema: true, IgnoresUserConfig: true,
		},
	}
}
