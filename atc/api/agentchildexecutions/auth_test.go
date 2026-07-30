package agentchildexecutions_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
)

func TestExecutionCapabilityRequiresExactScopeActionAndTimeWindow(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	signer, err := agentchildexecutions.NewCapabilitySigner("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	scope := completeScope()
	base := snapshot.SnapshotRef{ID: 8, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("8", 64))}
	scope.WorkspaceBase = &base
	catalog, err := broker.NewCatalog([]broker.Profile{authorityProfile()})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.MintExecution(scope, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", profile, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]agentchildexecutions.CapabilityCheck{
		"wrong action":    {Action: agentchildexecutions.ActionAdmit, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: scope, Now: now},
		"wrong execution": {Action: agentchildexecutions.ActionPhase, Resource: "c34a6e95-2e3a-45b0-b3f0-30c4e09acb7d", Scope: scope, Now: now},
		"cross team":      {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope { foreign := completeScope(); foreign.TeamID = 9; return foreign }(), Now: now},
		"cross team name": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope {
			foreign := completeScope()
			foreign.TeamName = "other"
			return foreign
		}(), Now: now},
		"cross build": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope { foreign := completeScope(); foreign.BuildID++; return foreign }(), Now: now},
		"cross definition": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope {
			foreign := completeScope()
			foreign.WorkflowDefinitionID++
			return foreign
		}(), Now: now},
		"cross input": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope {
			foreign := completeScope()
			input := foreign.Inputs["design"]
			input.Digest = snapshot.Digest("sha256:" + strings.Repeat("f", 64))
			foreign.Inputs["design"] = input
			return foreign
		}(), Now: now},
		"cross workspace base": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: func() agentchildexecutions.Scope {
			foreign := scope
			changed := *foreign.WorkspaceBase
			changed.ID++
			foreign.WorkspaceBase = &changed
			return foreign
		}(), Now: now},
		"expired": {Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: scope, Now: now.Add(2 * time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(token, check); err == nil {
				t.Fatal("Verify() succeeded")
			}
		})
	}
	if _, err := verifier.Verify(token+"x", agentchildexecutions.CapabilityCheck{Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: scope, Now: now}); err == nil {
		t.Fatal("Verify() accepted malformed capability")
	}
	notYet, err := signer.MintExecution(scope, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", profile, now.Add(time.Minute), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(notYet, agentchildexecutions.CapabilityCheck{Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: scope, Now: now}); err == nil {
		t.Fatal("Verify() accepted not-yet-valid capability")
	}
	if _, err := verifier.Verify(token, agentchildexecutions.CapabilityCheck{Action: agentchildexecutions.ActionPhase, Resource: "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", Scope: scope, Now: now}); err != nil {
		t.Fatalf("Verify(): %v", err)
	}
}

// This catches a capability scope that authorizes an occurrence by run alone,
// which would let the same signed scope be re-used across definitions.
func TestCapabilityScopeRequiresWorkflowDefinitionIdentity(t *testing.T) {
	scope := completeScope()
	scope.WorkflowDefinitionID = 0
	if err := scope.Validate(); err == nil {
		t.Fatal("Scope.Validate() accepted scope without workflow definition identity")
	}
}

func TestReviewCapabilitiesStagePhaseCaptureAndLifecycleAuthority(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	signer, _ := agentchildexecutions.NewCapabilitySigner("key-1", key)
	verifier, _ := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	scope := completeScope()
	delete(scope.Inputs, "workspace")
	base := snapshot.SnapshotRef{ID: 8, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("8", 64))}
	scope.WorkspaceBase = &base
	profile := resolvedReviewProfile(t)
	executionID := "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98"

	phaseToken, err := signer.MintReviewPhase(scope, executionID, profile, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifier.WorkspaceExecution(phaseToken, agentchildexecutions.ActionCaptureWorkspace, executionID, now); err == nil {
		t.Fatal("phase capability authorized workspace capture")
	}
	if _, _, capturePending, err := verifier.PhaseExecution(phaseToken, executionID, now); err != nil || !capturePending {
		t.Fatalf("PhaseExecution() pending=%v err=%v", capturePending, err)
	}

	captureToken, err := signer.MintWorkspaceCapture(scope, executionID, profile, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifier.Execution(captureToken, agentchildexecutions.ActionTerminal, executionID, now); err == nil {
		t.Fatal("capture capability authorized terminal")
	}
	if _, _, err := verifier.WorkspaceExecution(captureToken, agentchildexecutions.ActionCaptureWorkspace, executionID, now); err != nil {
		t.Fatalf("WorkspaceExecution(): %v", err)
	}

	captured := scope
	captured.Inputs = map[string]snapshot.SnapshotRef{}
	for name, ref := range scope.Inputs {
		captured.Inputs[name] = ref
	}
	captured.Inputs["workspace"] = snapshot.SnapshotRef{ID: 9, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("9", 64))}
	lifecycle, err := signer.MintExecution(captured, executionID, profile, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifier.WorkspaceExecution(lifecycle, agentchildexecutions.ActionCaptureWorkspace, executionID, now); err == nil {
		t.Fatal("lifecycle capability authorized a second capture")
	}
	if _, _, err := verifier.Execution(lifecycle, agentchildexecutions.ActionSeal, executionID, now); err != nil {
		t.Fatalf("Execution(): %v", err)
	}
}
