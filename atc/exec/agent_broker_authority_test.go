package exec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestAgentBrokerAuthorityRequestRequiresExactWorkspaceForReview(t *testing.T) {
	plan, inputs := agentBrokerTestPlan(t, broker.ToolRequestReview)
	metadata := agentBrokerTestMetadata()
	container := db.ContainerMetadata{Attempt: "2"}
	request, enabled, err := agentBrokerAuthorityRequest("plan-review", plan, metadata, container, inputs, "/work")
	if err != nil || !enabled {
		t.Fatalf("request = %#v, enabled %v, err %v", request, enabled, err)
	}
	if request.ParentAttempt != 2 || request.WorkspaceInputPath != "/work/workspace" ||
		request.ScopeInputs["workspace"].Type != "repository/v1" ||
		request.WorkflowDefinitionID != 41 || request.WorkflowRunID != 73 ||
		request.BrokerInstance == "" || request.NodePlanID != "plan-review" {
		t.Fatalf("request lost trusted scope: %#v", request)
	}

	for name, mutate := range map[string]func(*atc.AgentPlan, *snapshotInputBindings){
		"missing declaration": func(p *atc.AgentPlan, _ *snapshotInputBindings) { delete(p.SnapshotInputs, "workspace") },
		"optional": func(p *atc.AgentPlan, _ *snapshotInputBindings) {
			d := p.SnapshotInputs["workspace"]
			d.Optional = true
			p.SnapshotInputs["workspace"] = d
		},
		"wrong type": func(p *atc.AgentPlan, _ *snapshotInputBindings) {
			d := p.SnapshotInputs["workspace"]
			d.Type = "validation/v1"
			p.SnapshotInputs["workspace"] = d
		},
		"missing ref":      func(_ *atc.AgentPlan, i *snapshotInputBindings) { delete(i.refs, "workspace") },
		"missing exposure": func(_ *atc.AgentPlan, i *snapshotInputBindings) { delete(i.exposures, "workspace") },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, bindings := agentBrokerTestPlan(t, broker.ToolRequestReview)
			mutate(&candidate, &bindings)
			if _, _, err := agentBrokerAuthorityRequest("plan-review", candidate, metadata, container, bindings, "/work"); err == nil {
				t.Fatal("invalid review workspace accepted")
			}
		})
	}
}

func TestAgentBrokerAuthorityRequestAllowsConsultWithoutWorkspaceAndFailsClosedIdentity(t *testing.T) {
	plan, inputs := agentBrokerTestPlan(t, broker.ToolConsultAgent)
	delete(plan.SnapshotInputs, "workspace")
	delete(inputs.refs, "workspace")
	delete(inputs.exposures, "workspace")
	inputs.order = nil
	request, enabled, err := agentBrokerAuthorityRequest("plan-consult", plan, agentBrokerTestMetadata(), db.ContainerMetadata{Attempt: "1"}, inputs, "/work")
	if err != nil || !enabled || request.WorkspaceInputPath != "" {
		t.Fatalf("consult request = %#v, enabled %v, err %v", request, enabled, err)
	}
	for _, metadata := range []StepMetadata{
		{},
		{TeamID: 1, BuildID: 2, TeamName: "main", SnapshotCreatedBy: "concourse"},
	} {
		if _, _, err := agentBrokerAuthorityRequest("plan-consult", plan, metadata, db.ContainerMetadata{Attempt: "1"}, inputs, "/work"); err == nil {
			t.Fatal("forged plan without workflow identity accepted")
		}
	}
	if _, _, err := agentBrokerAuthorityRequest("plan-consult", plan, agentBrokerTestMetadata(), db.ContainerMetadata{Attempt: "1.2"}, inputs, "/work"); err == nil {
		t.Fatal("ambiguous parent attempt accepted")
	}
}

func agentBrokerTestPlan(t *testing.T, tool broker.Tool) (atc.AgentPlan, snapshotInputBindings) {
	t.Helper()
	profile := broker.Profile{
		ID: "profile", Revision: 1, Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools: []broker.Tool{tool}, WorkerImage: "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:  broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider: broker.ProviderSpec{Name: "provider", Model: "model"}, NativeEffort: "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64), CredentialSlot: "shared",
		Limits:   broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true, NativeOutputSchema: true, IgnoresUserConfig: true},
	}
	catalog, err := broker.NewCatalog([]broker.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := catalog.Resolve(tool, profile.Selector)
	raw, _ := json.Marshal(resolved)
	ref := snapshot.SnapshotRef{ID: 9, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}
	return atc.AgentPlan{
			Name: "agent", FunctionID: "agent", Hermetic: true, Inputs: []string{"workspace"},
			SnapshotInputs: map[string]atc.SnapshotInputConfig{"workspace": {Type: "repository/v1"}},
			BrokerAuthority: []atc.AgentBrokerProfile{{
				FunctionID: "agent", Tool: string(tool), Tier: string(profile.Selector.Tier), Effort: string(profile.Selector.Effort),
				ProfileID: resolved.ID, ProfileRevision: resolved.Revision, ProfileDigest: resolved.Digest,
				WorkerImage: resolved.WorkerImage, Profile: raw,
			}},
		}, snapshotInputBindings{
			order: []string{"workspace"}, refs: map[string]snapshot.SnapshotRef{"workspace": ref},
			exposures: map[string]snapshot.InputExposure{"workspace": snapshot.FullTreeExposure("/work/workspace", ref.Digest)},
		}
}

func agentBrokerTestMetadata() StepMetadata {
	definition := 41
	run := snapshot.WorkflowRunID(73)
	return StepMetadata{
		TeamID: 11, TeamName: "main", BuildID: 22, SnapshotCreatedBy: "concourse",
		WorkflowDefinitionID: &definition, WorkflowRunID: &run,
	}
}
