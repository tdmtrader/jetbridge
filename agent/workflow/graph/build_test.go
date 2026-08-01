package graph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func agentStep(name, functionID string, inputs, outputs []string, inTypes map[string]atc.SnapshotInputConfig, outTypes map[string]atc.SnapshotOutputConfig) atc.Step {
	return atc.Step{Config: &atc.AgentStep{
		Name:            name,
		FunctionID:      functionID,
		Inputs:          inputs,
		Outputs:         outputs,
		SnapshotInputs:  inTypes,
		SnapshotOutputs: outTypes,
	}}
}

func TestBuildLinksProducerToConsumer(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Outputs: []workflow.FunctionOutput{
			{Port: snapshot.Port{Name: "change", Type: "repository-change/v1"}, From: "candidate"},
		},
		Plan: []atc.Step{
			agentStep("implement", "implement",
				[]string{"repository"}, []string{"draft"},
				map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
				map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}}),
			agentStep("review", "review",
				[]string{"draft"}, []string{"candidate"},
				map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}},
				map[string]atc.SnapshotOutputConfig{"candidate": {Type: "repository-change/v1"}}),
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	if _, found := g.Node("implement"); !found {
		t.Fatal("expected an implement node")
	}
	if _, found := g.Node("repository"); !found {
		t.Fatal("expected a repository input node")
	}
	if _, found := g.Node("change"); !found {
		t.Fatal("expected a change output node")
	}

	want := []Edge{
		{From: "repository", To: "implement", PortName: "repository", TypeRef: "repository/v1"},
		{From: "implement", To: "review", PortName: "draft", TypeRef: "repository-change/v1"},
		{From: "review", To: "change", PortName: "candidate", TypeRef: "repository-change/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}
}

func TestBuildRedactsPrompts(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			func() atc.Step {
				step := agentStep("implement", "implement", nil, []string{"draft"}, nil,
					map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
				step.Config.(*atc.AgentStep).Prompt = "SECRET PROMPT"
				return step
			}(),
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	node, found := g.Node("implement")
	if !found || node.DisplayName != "implement" {
		t.Fatalf("expected the agent node, got found=%v node=%+v", found, node)
	}

	// Redaction is structural: Node has no prompt field, so the secret cannot
	// survive serialization. Assert against the wire form, which is what a
	// browser would actually receive.
	encoded, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshalling graph: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET PROMPT") {
		t.Fatalf("the graph must never carry prompt text: %s", encoded)
	}
}

// TestBuildHonoursAgentInputOutputMapping guards against a divergence from
// agent/workflow/typecheck.go's checkAgent, which resolves SnapshotInputs and
// SnapshotOutputs through InputMapping/OutputMapping before consulting the
// snapshot environment. A builder that links on the raw (pre-mapping) names
// would silently drop this edge instead of connecting producer to consumer.
func TestBuildHonoursAgentInputOutputMapping(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: &atc.AgentStep{
				Name:            "implement",
				FunctionID:      "implement",
				Inputs:          []string{"repo"},
				Outputs:         []string{"out"},
				InputMapping:    map[string]string{"repo": "repository"},
				OutputMapping:   map[string]string{"out": "draft"},
				SnapshotInputs:  map[string]atc.SnapshotInputConfig{"repo": {Type: "repository/v1"}},
				SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"out": {Type: "repository-change/v1"}},
			}},
			{Config: &atc.AgentStep{
				Name:            "review",
				FunctionID:      "review",
				Inputs:          []string{"draft"},
				Outputs:         []string{"candidate"},
				SnapshotInputs:  map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}},
				SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"candidate": {Type: "repository-change/v1"}},
			}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	want := Edge{From: "implement", To: "review", PortName: "draft", TypeRef: "repository-change/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("expected mapped edge %+v in %+v", want, g.Edges)
	}
}

func hasEdge(g Graph, want Edge) bool {
	for _, edge := range g.Edges {
		if edge == want {
			return true
		}
	}
	return false
}
