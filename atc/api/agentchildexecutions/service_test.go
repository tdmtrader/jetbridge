package agentchildexecutions_test

import (
	"context"
	"encoding/json"
	"fmt"
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
			TeamID: 1, TeamName: "main", BuildID: 1, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute, Inputs: completeScope().Inputs,
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
	admitted, err := service.Admit(context.Background(), admission)
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	if admitted.ExecutionID == "" || store.identity.ProfileDigest != resolved.Digest ||
		store.advances[0].State != broker.ExecutionAdmitted {
		t.Fatalf("admission = %#v advances=%#v", store.identity, store.advances)
	}

	store.execution.State = broker.ExecutionSealing
	store.execution.Sequence = 3
	sealed, err := service.Seal(context.Background(), broker.SealRequest{
		ExecutionID: admitted.ExecutionID, Body: []byte(`{"answer":"answer","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`),
	})
	if err != nil {
		t.Fatalf("Seal(): %v", err)
	}
	last := store.advances[len(store.advances)-1]
	if sealed.ID != 99 || last.State != broker.ExecutionSucceeded ||
		last.ResultSnapshotID != 99 {
		t.Fatalf("seal = %#v advance=%#v", sealed, last)
	}
	replayed, err := service.Seal(context.Background(), broker.SealRequest{ExecutionID: admitted.ExecutionID, Body: []byte(`{"answer":"answer","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`)})
	if err != nil || replayed != sealed {
		t.Fatalf("idempotent seal replay = %#v, %v", replayed, err)
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
			TeamID: 1, TeamName: "main", BuildID: 1, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute, Inputs: completeScope().Inputs,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	admitted, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Phase(context.Background(), admitted.ExecutionID, "running"); err != nil {
		t.Fatal(err)
	}
	if err := service.Update(context.Background(), admitted.ExecutionID, broker.RunUpdate{
		Events: []broker.Event{{Kind: "native TOKEN=super-secret"}},
	}); err == nil || !strings.Contains(err.Error(), "event") {
		t.Fatalf("Update() error = %v, want unsafe event rejection", err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, broker.Terminal{
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
			TeamID: 1, TeamName: "main", BuildID: 1, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute, Inputs: completeScope().Inputs,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	admitted, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "closed-terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, broker.Terminal{
		State: broker.ExecutionErrored, Code: "provider body", Retryable: false, Summary: "provider body",
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Terminal() error = %v, want closed-contract rejection", err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, broker.Terminal{
		State: broker.ExecutionTimedOut, Code: "deadline_exceeded", Retryable: false, Summary: "provider body",
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Terminal() error = %v, want retryability rejection", err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, broker.Terminal{
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
			TeamID: 1, TeamName: "main", BuildID: 1, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1,
			BrokerInstance: "pod-1", LeaseDuration: time.Minute, Inputs: completeScope().Inputs,
		},
		Catalog: catalog, Store: store, Sealer: &fakeSealer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	admitted, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "phase-terminal", Tool: broker.ToolConsultAgent,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Phase(context.Background(), admitted.ExecutionID, "errored"); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Phase() error = %v, want terminal phase rejection", err)
	}
}

func TestServiceAcceptsIdenticalTerminalReplayAndRejectsConflict(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{authorityProfile()})
	store := &fakeStore{}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{Scope: completeScope(), Catalog: catalog, Store: store, Sealer: &fakeSealer{}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	admitted, err := service.Admit(context.Background(), broker.AdmissionRequest{IdempotencyKey: "terminal-replay", Tool: broker.ToolConsultAgent, Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest, InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"}})
	if err != nil {
		t.Fatal(err)
	}
	terminal := broker.Terminal{State: broker.ExecutionTimedOut, Code: "deadline_exceeded", Retryable: true}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, terminal); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if err := service.Terminal(context.Background(), admitted.ExecutionID, broker.Terminal{State: broker.ExecutionErrored, Code: "provider_rejected", Retryable: true}); err == nil {
		t.Fatal("accepted conflicting terminal replay")
	}
}

func TestServiceAdmitsReviewAgainstWorkspaceBaseAndBindsAuthoritativeCapture(t *testing.T) {
	profileInput := authorityProfile()
	profileInput.Tools = []broker.Tool{broker.ToolRequestReview}
	catalog, _ := broker.NewCatalog([]broker.Profile{profileInput})
	resolved, _ := catalog.Resolve(broker.ToolRequestReview, profileInput.Selector)
	scope := completeScope()
	delete(scope.Inputs, "workspace")
	base := snapshot.SnapshotRef{ID: 8, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("8", 64))}
	scope.WorkspaceBase = &base
	store := &fakeStore{}
	sealer := &fakeSealer{workspace: snapshot.SnapshotRef{
		ID: 9, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("9", 64)),
	}}
	service, err := agentchildexecutions.NewService(agentchildexecutions.Config{
		Scope: scope, Catalog: catalog, Store: store, Sealer: sealer,
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := service.Admit(context.Background(), broker.AdmissionRequest{
		IdempotencyKey: "review-capture", Tool: broker.ToolRequestReview,
		Selector: resolved.Selector, ProfileID: resolved.ID, ProfileDigest: resolved.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64),
		Attachments: []string{"workspace", "validation"},
	})
	if err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	if err := service.Phase(context.Background(), admitted.ExecutionID, "capturing"); err != nil {
		t.Fatal(err)
	}
	capture := broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree:  strings.Repeat("3", 40),
		PatchDigest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		EntryCount:  1, PolicyRevision: "git-workspace-capture/v2",
	}
	sealed, err := service.CaptureWorkspace(context.Background(), admitted.ExecutionID, capture)
	if err != nil {
		t.Fatalf("CaptureWorkspace(): %v", err)
	}
	if sealed != sealer.workspace || store.execution.WorkspaceSnapshot == nil ||
		*store.execution.WorkspaceSnapshot != sealer.workspace {
		t.Fatalf("sealed=%#v execution=%#v", sealed, store.execution)
	}
	replayed, err := service.CaptureWorkspace(context.Background(), admitted.ExecutionID, capture)
	if err != nil || replayed != sealed {
		t.Fatalf("capture replay = %#v, %v", replayed, err)
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
	if request.ID != store.execution.ID || request.TeamID != store.execution.TeamID || request.ExpectedSequence != store.execution.Sequence {
		return db.AgentChildExecution{}, fmt.Errorf("sequence conflict")
	}
	if err := broker.ValidateExecutionTransition(store.execution.State, request.State); err != nil {
		return db.AgentChildExecution{}, err
	}
	store.advances = append(store.advances, request)
	store.execution.State = request.State
	store.execution.ErrorCode = request.ErrorCode
	store.execution.ErrorRetryable = request.ErrorRetryable
	store.execution.ErrorSummary = request.ErrorSummary
	if request.BrokerInstance != "" {
		store.execution.BrokerInstance = request.BrokerInstance
	}
	store.execution.Sequence++
	if request.ResultSnapshot != nil {
		store.execution.ResultSnapshot = request.ResultSnapshot
		id := int64(request.ResultSnapshot.ID)
		store.execution.ResultSnapshotID = &id
		store.execution.ResultBody = append([]byte(nil), request.ResultBody...)
	}
	return store.execution, nil
}
func (store *fakeStore) Find(_ context.Context, teamID int, id string) (db.AgentChildExecution, bool, error) {
	return store.execution, teamID == store.execution.TeamID && id == store.execution.ID, nil
}

func (store *fakeStore) BindWorkspace(_ context.Context, request db.BindAgentChildWorkspace) (db.AgentChildExecution, error) {
	if store.execution.WorkspaceSnapshot != nil {
		if *store.execution.WorkspaceSnapshot != request.Snapshot {
			return db.AgentChildExecution{}, fmt.Errorf("workspace conflict")
		}
		return store.execution, nil
	}
	if request.ExpectedSequence != store.execution.Sequence {
		return db.AgentChildExecution{}, fmt.Errorf("sequence conflict")
	}
	ref := request.Snapshot
	store.execution.WorkspaceSnapshot = &ref
	store.execution.Sequence++
	return store.execution, nil
}

type fakeSealer struct {
	calls     int
	workspace snapshot.SnapshotRef
}

func (sealer *fakeSealer) Seal(context.Context, agentchildexecutions.Scope, broker.ExecutionIdentity, agentchildexecutions.CandidateResult) (agentchildexecutions.SealedResult, error) {
	sealer.calls++
	return agentchildexecutions.SealedResult{Snapshot: snapshot.SnapshotRef{ID: 99, Type: "consultation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("e", 64))}, Body: json.RawMessage(`{"answer":"answer","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`)}, nil
}

func (sealer *fakeSealer) SealWorkspace(context.Context, agentchildexecutions.Scope, broker.ExecutionIdentity, broker.WorkspaceCapture) (snapshot.SnapshotRef, error) {
	return sealer.workspace, nil
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

func resolvedReviewProfile(t *testing.T) broker.Profile {
	t.Helper()
	input := authorityProfile()
	input.Tools = []broker.Tool{broker.ToolRequestReview}
	catalog, err := broker.NewCatalog([]broker.Profile{input})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(broker.ToolRequestReview, input.Selector)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func completeScope() agentchildexecutions.Scope {
	return agentchildexecutions.Scope{TeamID: 1, TeamName: "main", BuildID: 1, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 1, WorkflowRunID: 2, NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker-1", LeaseDuration: time.Minute, Inputs: map[string]snapshot.SnapshotRef{"design": {ID: 1, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}, "api-contract": {ID: 2, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64))}, "workspace": {ID: 3, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}, "validation": {ID: 4, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("d", 64))}}}
}
