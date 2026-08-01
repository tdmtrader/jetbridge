package graph

import (
	"encoding/json"
	"path/filepath"
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
	if _, found := g.Node("input:repository"); !found {
		t.Fatal("expected a kind-qualified repository input node")
	}
	if _, found := g.Node("output:change"); !found {
		t.Fatal("expected a kind-qualified change output node")
	}

	want := []Edge{
		{From: "input:repository", To: "implement", PortName: "repository", TypeRef: "repository/v1"},
		{From: "implement", To: "review", PortName: "draft", TypeRef: "repository-change/v1"},
		{From: "review", To: "output:change", PortName: "candidate", TypeRef: "repository-change/v1"},
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

// TestBuildCodeReviewSeedProducesTerminalOutputEdge is the regression guard
// for the collision this task fixed: the shipped code-review-v3 seed
// declares public output port "review" (from: review) right alongside an
// agent whose function_id is also "review" — an idiomatic pattern, since a
// port's name is naturally taken from the binding that feeds it. Before
// endpoint IDs were kind-qualified, addNode's de-duplication silently merged
// the output node into the agent node and link's self-loop guard silently
// dropped the edge, so the workflow's only public output vanished from the
// graph with no error. Build it from the real seed directory, not a
// synthetic fixture, so a future regression here is caught against shipped
// data.
func TestBuildCodeReviewSeedProducesTerminalOutputEdge(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("../seeds/code-review-v3")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}

	g, err := Build(definition.Function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	if _, found := g.Node("review"); !found {
		t.Fatal("expected the review agent node")
	}
	if _, found := g.Node("output:review"); !found {
		t.Fatalf("expected a kind-qualified output:review node in %+v", g.Nodes)
	}

	want := Edge{From: "review", To: "output:review", PortName: "review", TypeRef: "review/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("expected terminal edge %+v in %+v", want, g.Edges)
	}
}

func TestBuildAwaitPublishAndLoadNodes(t *testing.T) {
	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			agentStep("prepare", "prepare",
				[]string{"repository"}, []string{"approval-question"},
				map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
				map[string]atc.SnapshotOutputConfig{"approval-question": {Type: "question/v1"}}),
			{Config: &atc.AwaitSnapshotStep{
				Name:      "approval",
				Question:  "approval-question",
				Type:      "human-answer/v1",
				OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
			}},
			{Config: &atc.PublishSnapshotStep{
				Name:      "ship",
				Input:     "repository",
				InputType: "repository/v1",
				Approval:  "approval",
			}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	await, found := g.Node("approval")
	if !found || await.Kind != KindAwait {
		t.Fatalf("expected an await node, got found=%v node=%+v", found, await)
	}
	if await.TypeRef != "human-answer/v1" {
		t.Fatalf("expected the await node to carry its answer type, got %q", await.TypeRef)
	}

	publish, found := g.Node("ship")
	if !found || publish.Kind != KindPublish {
		t.Fatalf("expected a publish node, got found=%v node=%+v", found, publish)
	}

	want := []Edge{
		{From: "prepare", To: "approval", PortName: "approval-question", TypeRef: "question/v1"},
		{From: "input:repository", To: "ship", PortName: "repository", TypeRef: "repository/v1"},
		{From: "approval", To: "ship", PortName: "approval", TypeRef: "human-answer/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
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

func TestBuildTreatsWrappersAsDecorations(t *testing.T) {
	inner := agentStep("implement", "implement",
		[]string{"repository"}, []string{"draft"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})

	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			// TimeoutStep.Step and RetryStep.Step are StepConfig, not Step.
			{Config: &atc.TimeoutStep{
				Step:     &atc.RetryStep{Step: inner.Config, Attempts: 3},
				Duration: "1h",
			}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	if len(g.Nodes) != 2 {
		t.Fatalf("expected exactly the input and agent nodes, got %+v", g.Nodes)
	}

	node, found := g.Node("implement")
	if !found {
		t.Fatal("expected the wrapped agent to remain a node")
	}
	if !hasDecoration(node, DecorationTimeout) || !hasDecoration(node, DecorationRetry) {
		t.Fatalf("expected timeout and retry decorations, got %+v", node.Decorations)
	}
}

func TestBuildWalksDoAndInParallel(t *testing.T) {
	left := agentStep("left", "left", []string{"repository"}, []string{"a"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"a": {Type: "opaque/v1"}})
	right := agentStep("right", "right", []string{"repository"}, []string{"b"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"b": {Type: "opaque/v1"}})

	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			{Config: &atc.DoStep{Steps: []atc.Step{
				{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{left, right}}}},
			}}},
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	for _, id := range []string{"left", "right"} {
		if _, found := g.Node(id); !found {
			t.Fatalf("expected node %q in %+v", id, g.Nodes)
		}
	}
}

func TestBuildDoesNotLeakDecorationsBetweenSiblings(t *testing.T) {
	plain := agentStep("plain", "plain", []string{"repository"}, []string{"a"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"a": {Type: "opaque/v1"}})
	retried := agentStep("retried", "retried", []string{"repository"}, []string{"b"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"b": {Type: "opaque/v1"}})

	function := &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			{Config: &atc.RetryStep{Step: retried.Config, Attempts: 2}},
			plain,
		},
	}

	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	retriedNode, _ := g.Node("retried")
	if !hasDecoration(retriedNode, DecorationRetry) {
		t.Fatalf("expected the retried node to carry retry, got %+v", retriedNode.Decorations)
	}
	plainNode, _ := g.Node("plain")
	if len(plainNode.Decorations) != 0 {
		t.Fatalf("an unwrapped sibling must carry no decorations, got %+v", plainNode.Decorations)
	}
}

func hasDecoration(node Node, want Decoration) bool {
	for _, decoration := range node.Decorations {
		if decoration == want {
			return true
		}
	}
	return false
}

// TestBuildCompilesEveryShippedSeed is the real proof for Task A5: every
// shipped workflow seed (a directory with a workflow.yaml) ships an
// await_snapshot wrapped in a mandatory timeout (the type checker requires
// it), and before this task walkStepConfig rejected *atc.TimeoutStep
// outright, so no seed with an await could build. Build them all from the
// real seed directories, not synthetic fixtures.
//
// Scoped to */workflow.yaml, not every entry under seeds: code-review-node-v1
// ships a node.yaml (a reusable node fragment meant to be instantiated via
// CompiledNodeDefinition.Instantiate and referenced from a workflow, not a
// standalone workflow function) and correctly has no workflow.yaml of its
// own; ManifestFromDir/CompileDefinition are the workflow-function pipeline,
// not the reusable-node one, so it is out of scope here by construction.
func TestBuildCompilesEveryShippedSeed(t *testing.T) {
	matches, err := filepath.Glob("../seeds/*/workflow.yaml")
	if err != nil {
		t.Fatalf("globbing seed workflow.yaml files: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one seed with a workflow.yaml under ../seeds")
	}

	for _, match := range matches {
		dir := filepath.Dir(match)
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(dir)
			if err != nil {
				t.Fatalf("ManifestFromDir(%q): %v", name, err)
			}
			definition, err := workflow.CompileDefinition(manifest)
			if err != nil {
				t.Fatalf("CompileDefinition(%q): %v", name, err)
			}

			g, err := Build(definition.Function)
			if err != nil {
				t.Fatalf("Build(%q) returned an error: %v", name, err)
			}
			if len(g.Nodes) == 0 {
				t.Fatalf("Build(%q) produced no nodes", name)
			}
		})
	}
}

// TestBuildSmallFixSeedAwaitCarriesTimeoutDecoration pins the specific case
// that was unreachable before this task: small-fix-v3's approval await is
// wrapped in a mandatory timeout (72h), which the type checker requires for
// every await_snapshot. Before wrapper steps decorated rather than rejected,
// Build failed on *atc.TimeoutStep before ever reaching this node.
func TestBuildSmallFixSeedAwaitCarriesTimeoutDecoration(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("../seeds/small-fix-v3")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}

	g, err := Build(definition.Function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}

	node, found := g.Node("approval")
	if !found {
		t.Fatalf("expected the approval await node in %+v", g.Nodes)
	}
	if node.Kind != KindAwait {
		t.Fatalf("expected approval to be an await node, got %+v", node)
	}
	if !hasDecoration(node, DecorationTimeout) {
		t.Fatalf("expected the approval await to carry a timeout decoration, got %+v", node.Decorations)
	}
}
