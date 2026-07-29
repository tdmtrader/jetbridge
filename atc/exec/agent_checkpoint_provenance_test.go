package exec

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestDeriveAgentCheckpointProvenanceUsesOnlyPinnedServerInputs(t *testing.T) {
	request := validAgentCheckpointProvenanceRequest()

	first, err := deriveAgentCheckpointProvenance(request)
	if err != nil {
		t.Fatal(err)
	}

	// Map and declaration order are not execution identity. Reordering them
	// must not manufacture a different checkpoint lineage.
	reordered := request
	reordered.Plan.Inputs = []string{"skills", "notes", "repo"}
	reordered.Plan.Skills = []string{"testing", "review"}
	reordered.Plan.Env = map[string]string{"Z": "last", "A": "first"}
	reordered.Inputs = map[string]snapshot.SnapshotRef{
		"skills": request.Inputs["skills"],
		"repo":   request.Inputs["repo"],
	}
	reordered.Sidecars = []atc.SidecarConfig{request.Sidecars[1], request.Sidecars[0]}
	second, err := deriveAgentCheckpointProvenance(reordered)
	if err != nil {
		t.Fatal(err)
	}

	if first.Identity != second.Identity ||
		first.ConfigDigest != second.ConfigDigest ||
		first.InputDigest != second.InputDigest ||
		first.MCPDigest != second.MCPDigest ||
		first.SkillDigest != second.SkillDigest {
		t.Fatalf("canonical provenance changed after reorder:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.ContainerHandle != "container-handle" ||
		first.Provider != "anthropic" ||
		first.RuntimeImage != request.RuntimeImage ||
		first.Model != request.Plan.Model {
		t.Fatalf("pinned provenance = %#v", first)
	}
	if first.SessionID != "" || first.TranscriptCursor != 0 ||
		len(first.CompletedToolCallIDs) != 0 || len(first.Effects) != 0 {
		t.Fatalf("workspace-only capture invented provider evidence: %#v", first)
	}
}

func TestDeriveAgentCheckpointProvenanceSeparatesConfigInputsMCPAndSkills(t *testing.T) {
	baseRequest := validAgentCheckpointProvenanceRequest()
	base, err := deriveAgentCheckpointProvenance(baseRequest)
	if err != nil {
		t.Fatal(err)
	}

	configChanged := baseRequest
	configChanged.Plan.Prompt = "different prompt"
	config, err := deriveAgentCheckpointProvenance(configChanged)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConfigDigest == base.ConfigDigest ||
		config.InputDigest != base.InputDigest ||
		config.MCPDigest != base.MCPDigest ||
		config.SkillDigest != base.SkillDigest {
		t.Fatalf("config change crossed digest domains: base=%#v changed=%#v", base, config)
	}

	inputChanged := baseRequest
	inputChanged.Inputs = cloneCheckpointSnapshotRefs(baseRequest.Inputs)
	inputChanged.Inputs["repo"] = checkpointSnapshotRef(11, "repository/v1", "c")
	input, err := deriveAgentCheckpointProvenance(inputChanged)
	if err != nil {
		t.Fatal(err)
	}
	if input.InputDigest == base.InputDigest ||
		input.ConfigDigest != base.ConfigDigest ||
		input.MCPDigest != base.MCPDigest ||
		input.SkillDigest != base.SkillDigest {
		t.Fatalf("input change crossed digest domains: base=%#v changed=%#v", base, input)
	}

	mcpChanged := baseRequest
	mcpChanged.Sidecars = append([]atc.SidecarConfig(nil), baseRequest.Sidecars...)
	mcpChanged.Sidecars[0].Args = []string{"--different"}
	mcp, err := deriveAgentCheckpointProvenance(mcpChanged)
	if err != nil {
		t.Fatal(err)
	}
	if mcp.MCPDigest == base.MCPDigest ||
		mcp.ConfigDigest != base.ConfigDigest ||
		mcp.InputDigest != base.InputDigest ||
		mcp.SkillDigest != base.SkillDigest {
		t.Fatalf("MCP change crossed digest domains: base=%#v changed=%#v", base, mcp)
	}

	skillChanged := baseRequest
	skillChanged.Plan.Skills = []string{"review"}
	skill, err := deriveAgentCheckpointProvenance(skillChanged)
	if err != nil {
		t.Fatal(err)
	}
	if skill.SkillDigest == base.SkillDigest ||
		skill.ConfigDigest != base.ConfigDigest ||
		skill.InputDigest != base.InputDigest ||
		skill.MCPDigest != base.MCPDigest {
		t.Fatalf("skill change crossed digest domains: base=%#v changed=%#v", base, skill)
	}
}

func TestDeriveAgentCheckpointProvenanceFailsClosedWhenExecutionIsNotPinned(t *testing.T) {
	tests := map[string]func(*agentCheckpointProvenanceRequest){
		"non-hermetic plan": func(request *agentCheckpointProvenanceRequest) {
			request.Plan.Hermetic = false
		},
		"missing workflow run": func(request *agentCheckpointProvenanceRequest) {
			request.Metadata.WorkflowRunID = nil
		},
		"missing function": func(request *agentCheckpointProvenanceRequest) {
			request.Plan.FunctionID = ""
		},
		"tagged runtime": func(request *agentCheckpointProvenanceRequest) {
			request.RuntimeImage = "registry.example/agent:latest"
		},
		"missing model": func(request *agentCheckpointProvenanceRequest) {
			request.Plan.Model = ""
		},
		"invalid adapter": func(request *agentCheckpointProvenanceRequest) {
			request.Adapter = provider.Identity{}
		},
		"tagged sidecar": func(request *agentCheckpointProvenanceRequest) {
			request.Sidecars[0].Image = "registry.example/mcp:latest"
		},
		"missing typed input ref": func(request *agentCheckpointProvenanceRequest) {
			delete(request.Inputs, "repo")
		},
		"selected skills without source": func(request *agentCheckpointProvenanceRequest) {
			request.Plan.Inputs = []string{"repo", "notes"}
			delete(request.Inputs, "skills")
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validAgentCheckpointProvenanceRequest()
			mutate(&request)
			if _, err := deriveAgentCheckpointProvenance(request); err == nil {
				t.Fatal("unpinned provenance succeeded")
			}
		})
	}
}

func validAgentCheckpointProvenanceRequest() agentCheckpointProvenanceRequest {
	runID := snapshot.WorkflowRunID(91)
	return agentCheckpointProvenanceRequest{
		PlanID: "plan-1",
		Plan: atc.AgentPlan{
			Name:         "review",
			FunctionID:   "review-code",
			Hermetic:     true,
			RuntimeImage: pinnedCheckpointImage("agent", "a"),
			Prompt:       "review it",
			Model:        "claude-opus",
			MaxTurns:     7,
			SystemPrompt: "be precise",
			Context:      "trunk based",
			Inputs:       []string{"repo", "notes", "skills"},
			Outputs:      []string{"review"},
			SnapshotInputs: map[string]atc.SnapshotInputConfig{
				"repo":   {Type: "repository/v1"},
				"skills": {Type: "skill-bundle/v1"},
			},
			Skills: []string{"review", "testing"},
			Env:    map[string]string{"A": "first", "Z": "last"},
		},
		Metadata: StepMetadata{
			BuildID:       42,
			WorkflowRunID: &runID,
		},
		RuntimeImage:     pinnedCheckpointImage("agent", "a"),
		Provider:         "anthropic",
		Adapter:          provider.Identity{Name: "claude-cli", Version: "legacy-stream-json"},
		ExecutionAttempt: 1,
		ContainerHandle:  "container-handle",
		Inputs: map[string]snapshot.SnapshotRef{
			"repo":   checkpointSnapshotRef(10, "repository/v1", "b"),
			"skills": checkpointSnapshotRef(12, "skill-bundle/v1", "d"),
		},
		Sidecars: []atc.SidecarConfig{
			{Name: "tests", Image: pinnedCheckpointImage("tests", "e"), Command: []string{"/mcp"}, Args: []string{"--stdio"}},
			{Name: "platform", Image: pinnedCheckpointImage("platform", "f"), Ports: []atc.SidecarPort{{ContainerPort: 7781}}},
		},
	}
}

func pinnedCheckpointImage(name, hex string) string {
	return "registry.example/" + name + "@sha256:" + strings.Repeat(hex, 64)
}

func checkpointSnapshotRef(id snapshot.SnapshotID, typ snapshot.TypeRef, hex string) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID:     id,
		Type:   typ,
		Digest: snapshot.Digest("sha256:" + strings.Repeat(hex, 64)),
	}
}

func cloneCheckpointSnapshotRefs(values map[string]snapshot.SnapshotRef) map[string]snapshot.SnapshotRef {
	cloned := make(map[string]snapshot.SnapshotRef, len(values))
	for name, ref := range values {
		cloned[name] = ref
	}
	return cloned
}
