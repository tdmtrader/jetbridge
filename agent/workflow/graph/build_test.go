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

// buildTypeChecked is the single door every synthetic fixture goes through.
//
// This package's whole claim is that it agrees with agent/workflow/typecheck.go
// about typed producer-to-consumer linkage. A fixture the type checker would
// reject can neither confirm nor deny that claim: it proves only what Build
// does with input the rest of the system will never hand it. Before this
// helper existed, every fixture in this file was in exactly that state (the
// awaits had no mandatory timeout wrapper, the publish approval bound no
// await), which is why a whole class of divergence — hook subtrees walked
// against the wrong producer environment — went unnoticed.
//
// Route new fixtures through here. If TypeCheckFunction rejects one, the
// fixture is wrong, not the assertion.
func buildTypeChecked(t *testing.T, function *workflow.FunctionConfig) Graph {
	t.Helper()
	if err := workflow.TypeCheckFunction(function); err != nil {
		t.Fatalf("fixture does not type-check, so it cannot prove agreement with the type checker: %v", err)
	}
	g, err := Build(function)
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	return g
}

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

func hasEdge(g Graph, want Edge) bool {
	for _, edge := range g.Edges {
		if edge == want {
			return true
		}
	}
	return false
}

// edgesInto returns every edge terminating at nodeID, so a test can assert
// what a consumer is NOT linked to as well as what it is.
func edgesInto(g Graph, nodeID string) []Edge {
	var found []Edge
	for _, edge := range g.Edges {
		if edge.To == nodeID {
			found = append(found, edge)
		}
	}
	return found
}

func hasDecoration(node Node, want Decoration) bool {
	for _, decoration := range node.Decorations {
		if decoration == want {
			return true
		}
	}
	return false
}

func TestBuildLinksProducerToConsumer(t *testing.T) {
	g := buildTypeChecked(t, &workflow.FunctionConfig{
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
	})

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
		// The public output edge is labelled "change" (the port), not
		// "candidate" (the binding it consumed). The durable run-level binding
		// is keyed by the port, so this is the only label the run page can
		// join on. The producer is still selected through the binding.
		{From: "review", To: "output:change", PortName: "change", TypeRef: "repository-change/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}
}

func TestBuildRedactsPrompts(t *testing.T) {
	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			func() atc.Step {
				step := agentStep("implement", "implement", nil, []string{"draft"}, nil,
					map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
				step.Config.(*atc.AgentStep).Prompt = "SECRET PROMPT"
				return step
			}(),
		},
	})

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
	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
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
	})

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
	g := buildSeed(t, "code-review-v3")

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
	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			// A load_snapshot is the one execution node that consumes nothing:
			// its id is a renderer-owned parameter, not a binding.
			{Config: &atc.LoadSnapshotStep{Name: "prior-review", ID: "1", Type: "review/v1"}},
			agentStep("prepare", "prepare",
				[]string{"repository", "prior-review"}, []string{"approval-question"},
				map[string]atc.SnapshotInputConfig{
					"repository":   {Type: "repository/v1"},
					"prior-review": {Type: "review/v1"},
				},
				map[string]atc.SnapshotOutputConfig{"approval-question": {Type: "question/v1"}}),
			// checkAwaitSnapshot rejects an await with no ordinary timeout
			// wrapper outright, so an await fixture without one proves nothing.
			{Config: &atc.TimeoutStep{Duration: "72h", Step: &atc.AwaitSnapshotStep{
				Name:      "approval",
				Question:  "approval-question",
				Type:      "human-answer/v1",
				OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
			}}},
			agentStep("record", "record",
				[]string{"approval"}, []string{"report"},
				map[string]atc.SnapshotInputConfig{"approval": {Type: "human-answer/v1"}},
				map[string]atc.SnapshotOutputConfig{"report": {Type: "opaque/v1"}}),
			{Config: &atc.PublishSnapshotStep{
				Name:        "ship",
				Publisher:   "opaque-publisher/v1",
				Input:       "report",
				InputType:   "opaque/v1",
				Destination: "example.test/reports",
			}},
		},
	})

	load, found := g.Node("prior-review")
	if !found || load.Kind != KindLoad {
		t.Fatalf("expected a load node, got found=%v node=%+v", found, load)
	}
	if load.TypeRef != "review/v1" {
		t.Fatalf("expected the load node to carry its snapshot type, got %q", load.TypeRef)
	}
	if load.Optional {
		t.Fatalf("a required load must not be marked optional, got %+v", load)
	}

	await, found := g.Node("approval")
	if !found || await.Kind != KindAwait {
		t.Fatalf("expected an await node, got found=%v node=%+v", found, await)
	}
	if await.TypeRef != "human-answer/v1" {
		t.Fatalf("expected the await node to carry its answer type, got %q", await.TypeRef)
	}
	if !hasDecoration(await, DecorationTimeout) {
		t.Fatalf("expected the mandatory timeout wrapper to decorate the await, got %+v", await.Decorations)
	}

	publish, found := g.Node("ship")
	if !found || publish.Kind != KindPublish {
		t.Fatalf("expected a publish node, got found=%v node=%+v", found, publish)
	}

	want := []Edge{
		{From: "prior-review", To: "prepare", PortName: "prior-review", TypeRef: "review/v1"},
		{From: "input:repository", To: "prepare", PortName: "repository", TypeRef: "repository/v1"},
		{From: "prepare", To: "approval", PortName: "approval-question", TypeRef: "question/v1"},
		{From: "approval", To: "record", PortName: "approval", TypeRef: "human-answer/v1"},
		{From: "record", To: "ship", PortName: "report", TypeRef: "opaque/v1"},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}

	// A publish is a terminal side effect, never a producer.
	for _, edge := range g.Edges {
		if edge.From == "ship" {
			t.Fatalf("publish must produce nothing, got outgoing edge %+v", edge)
		}
	}
}

// TestBuildOptionalLoadIsMarkedOptional pins I6: atc.LoadSnapshotStep.Optional
// makes typecheck.go's binding conditional (checkLoadSnapshot sets presence =
// snapshotConditional), and the graph is the only place a renderer can learn
// "this may not run". Dropping it left Phase D with no way to say so.
func TestBuildOptionalLoadIsMarkedOptional(t *testing.T) {
	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: &atc.LoadSnapshotStep{Name: "prior-review", ID: "0", Type: "review/v1", Optional: true}},
			agentStep("summarize", "summarize",
				[]string{"prior-review"}, []string{"summary"},
				map[string]atc.SnapshotInputConfig{"prior-review": {Type: "review/v1", Optional: true}},
				map[string]atc.SnapshotOutputConfig{"summary": {Type: "opaque/v1"}}),
		},
	})

	load, found := g.Node("prior-review")
	if !found {
		t.Fatalf("expected the load node in %+v", g.Nodes)
	}
	if !load.Optional {
		t.Fatalf("an optional load must be marked optional, got %+v", load)
	}

	want := Edge{From: "prior-review", To: "summarize", PortName: "prior-review", TypeRef: "review/v1", Optional: true}
	if !hasEdge(g, want) {
		t.Fatalf("expected the conditional edge %+v in %+v", want, g.Edges)
	}
}

// TestBuildResourceSourceNodes covers the resource_source endpoint kind, which
// had no test at all: a standing resource source is a guaranteed typed
// producer that analyzeFunctionFlow seeds into the environment alongside
// public inputs.
func TestBuildResourceSourceNodes(t *testing.T) {
	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Resources: atc.ResourceConfigs{
			{Name: "upstream", Type: "git", Source: atc.Source{"uri": "https://example.test/repo"}},
		},
		ResourceSources: []workflow.ResourceSource{
			{Name: "pinned-repository", Resource: "upstream", Type: "repository/v1"},
		},
		Plan: []atc.Step{
			agentStep("implement", "implement",
				[]string{"pinned-repository"}, []string{"draft"},
				map[string]atc.SnapshotInputConfig{"pinned-repository": {Type: "repository/v1"}},
				map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}}),
		},
	})

	source, found := g.Node("source:pinned-repository")
	if !found || source.Kind != KindResourceSource {
		t.Fatalf("expected a kind-qualified resource source node, got found=%v node=%+v", found, source)
	}
	if source.TypeRef != "repository/v1" {
		t.Fatalf("expected the resource source node to carry its type, got %q", source.TypeRef)
	}

	want := Edge{From: "source:pinned-repository", To: "implement", PortName: "pinned-repository", TypeRef: "repository/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("expected edge %+v in %+v", want, g.Edges)
	}
}

func TestBuildTreatsWrappersAsDecorations(t *testing.T) {
	inner := agentStep("implement", "implement",
		[]string{"repository"}, []string{"draft"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			// TimeoutStep.Step and RetryStep.Step are StepConfig, not Step.
			{Config: &atc.TimeoutStep{
				Step:     &atc.RetryStep{Step: inner.Config, Attempts: 3},
				Duration: "1h",
			}},
		},
	})

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

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			{Config: &atc.DoStep{Steps: []atc.Step{
				{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{left, right}}}},
			}}},
		},
	})

	for _, id := range []string{"left", "right"} {
		if _, found := g.Node(id); !found {
			t.Fatalf("expected node %q in %+v", id, g.Nodes)
		}
	}
	for _, edge := range []Edge{
		{From: "input:repository", To: "left", PortName: "repository", TypeRef: "repository/v1"},
		{From: "input:repository", To: "right", PortName: "repository", TypeRef: "repository/v1"},
	} {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}
}

// TestBuildParallelBranchesDoNotSeeEachOther pins checkParallel's scoping.
// Every branch is checked against its own clone of the entry environment, so
// a branch that overwrites a name cannot become the producer another branch
// reads. Threading one environment through the branches in plan order made
// "shadow" the producer of repository for the second branch, an edge the type
// checker never draws.
func TestBuildParallelBranchesDoNotSeeEachOther(t *testing.T) {
	shadowing := agentStep("shadowing", "shadowing", nil, []string{"repository"}, nil,
		map[string]atc.SnapshotOutputConfig{"repository": {Type: "repository/v1"}})
	reader := agentStep("reader", "reader", []string{"repository"}, []string{"summary"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"summary": {Type: "opaque/v1"}})

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan: []atc.Step{
			{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{shadowing, reader}}}},
		},
	})

	want := Edge{From: "input:repository", To: "reader", PortName: "repository", TypeRef: "repository/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("expected the reader to bind the workflow input, got %+v", edgesInto(g, "reader"))
	}
	for _, edge := range edgesInto(g, "reader") {
		if edge.From == "shadowing" {
			t.Fatalf("a parallel branch must not see a sibling branch's writes, got %+v", edge)
		}
	}
}

// TestBuildTryScopesProductionConditionally pins checkStep's TryStep arm and
// conditionalTryFlow. Nothing inside a try is guaranteed, so a consumer after
// the try still binds to the try's node — but the dataflow is conditional and
// must be rendered as such.
func TestBuildTryScopesProductionConditionally(t *testing.T) {
	inner := agentStep("inner", "inner", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	after := agentStep("after", "after", []string{"draft"}, []string{"summary"},
		map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1", Optional: true}},
		map[string]atc.SnapshotOutputConfig{"summary": {Type: "opaque/v1"}})

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: &atc.TryStep{Step: inner}},
			after,
		},
	})

	node, found := g.Node("inner")
	if !found {
		t.Fatalf("expected the tried node in %+v", g.Nodes)
	}
	if !hasDecoration(node, DecorationTry) {
		t.Fatalf("expected a try decoration, got %+v", node.Decorations)
	}

	want := Edge{From: "inner", To: "after", PortName: "draft", TypeRef: "repository-change/v1", Optional: true}
	if !hasEdge(g, want) {
		t.Fatalf("expected the conditional try edge %+v in %+v", want, g.Edges)
	}
}

// TestBuildNonSuccessHookDoesNotSeeMainStepWrites is regression probe 1 for
// the Critical finding.
//
// typecheck.go's checkNonSuccessHook checks the hook against
// cloneSnapshotEnvironment(entry) — the environment as it stood BEFORE the
// main step — because a hook runs precisely when the main step did not
// succeed. Here "first" produces x as repository-change/v1 and the wrapped
// main step re-produces x as review/v1; the type checker binds the hook's x to
// "first". Threading one mutable producer map through main and hook made the
// graph emit from="main" type="review/v1": the wrong source node AND the wrong
// type, asserting a typed dependency the type checker specifically disproved.
func TestBuildNonSuccessHookDoesNotSeeMainStepWrites(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		decoration Decoration
		wrap       func(main atc.StepConfig, hook atc.Step) atc.StepConfig
	}{
		{"on_failure", DecorationOnFailure, func(main atc.StepConfig, hook atc.Step) atc.StepConfig {
			return &atc.OnFailureStep{Step: main, Hook: hook}
		}},
		{"on_error", DecorationOnError, func(main atc.StepConfig, hook atc.Step) atc.StepConfig {
			return &atc.OnErrorStep{Step: main, Hook: hook}
		}},
		{"on_abort", DecorationOnAbort, func(main atc.StepConfig, hook atc.Step) atc.StepConfig {
			return &atc.OnAbortStep{Step: main, Hook: hook}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			first := agentStep("first", "first", nil, []string{"x"}, nil,
				map[string]atc.SnapshotOutputConfig{"x": {Type: "repository-change/v1"}})
			main := agentStep("main", "main", nil, []string{"x"}, nil,
				map[string]atc.SnapshotOutputConfig{"x": {Type: "review/v1"}})
			hook := agentStep("hook", "hook", []string{"x"}, nil,
				map[string]atc.SnapshotInputConfig{"x": {Type: "repository-change/v1"}}, nil)

			g := buildTypeChecked(t, &workflow.FunctionConfig{
				SignatureVersion: 1,
				Plan: []atc.Step{
					first,
					{Config: testCase.wrap(main.Config, hook)},
				},
			})

			want := Edge{From: "first", To: "hook", PortName: "x", TypeRef: "repository-change/v1"}
			if !hasEdge(g, want) {
				t.Fatalf("the hook must bind the pre-wrapper producer, got %+v", edgesInto(g, "hook"))
			}
			for _, edge := range edgesInto(g, "hook") {
				if edge.From == "main" {
					t.Fatalf("a non-success hook must not see the main step's writes, got %+v", edge)
				}
			}

			node, found := g.Node("hook")
			if !found {
				t.Fatalf("expected the hook node in %+v", g.Nodes)
			}
			if !hasDecoration(node, testCase.decoration) {
				t.Fatalf("expected a %s decoration, got %+v", testCase.decoration, node.Decorations)
			}
		})
	}
}

// TestBuildNonSuccessHookWritesDoNotReachSuccessPath is regression probe 2 for
// the Critical finding.
//
// checkNonSuccessHook returns main, discarding hook.env entirely: failure,
// error, and abort remain non-success results, so a node after the wrapper
// binds to whatever produced the name before it. Threading one mutable
// producer map let the hook's write win, drawing a dataflow edge out of a
// failure-only hook into the success path — on the run page, a dependency
// that could never have executed in a successful run.
func TestBuildNonSuccessHookWritesDoNotReachSuccessPath(t *testing.T) {
	first := agentStep("first", "first", nil, []string{"x"}, nil,
		map[string]atc.SnapshotOutputConfig{"x": {Type: "repository-change/v1"}})
	main := agentStep("main", "main", nil, []string{"y"}, nil,
		map[string]atc.SnapshotOutputConfig{"y": {Type: "opaque/v1"}})
	hook := agentStep("hook", "hook", nil, []string{"x"}, nil,
		map[string]atc.SnapshotOutputConfig{"x": {Type: "repository-change/v1"}})
	after := agentStep("after", "after", []string{"x"}, nil,
		map[string]atc.SnapshotInputConfig{"x": {Type: "repository-change/v1"}}, nil)

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			first,
			{Config: &atc.OnFailureStep{Step: main.Config, Hook: hook}},
			after,
		},
	})

	want := Edge{From: "first", To: "after", PortName: "x", TypeRef: "repository-change/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("the success path must bind the pre-wrapper producer, got %+v", edgesInto(g, "after"))
	}
	for _, edge := range edgesInto(g, "after") {
		if edge.From == "hook" {
			t.Fatalf("a failure-only hook must not feed the success path, got %+v", edge)
		}
	}
}

// TestBuildOnSuccessHookSeesMainStepWrites is the counterpart: on_success is
// the one hook typecheck.go genuinely checks against main.env (checkStep's
// OnSuccessStep arm), because it runs only when the main step succeeded. The
// graph must keep that asymmetry rather than scoping every hook alike.
func TestBuildOnSuccessHookSeesMainStepWrites(t *testing.T) {
	main := agentStep("main", "main", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	hook := agentStep("hook", "hook", []string{"draft"}, []string{"summary"},
		map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}},
		map[string]atc.SnapshotOutputConfig{"summary": {Type: "opaque/v1"}})
	after := agentStep("after", "after", []string{"summary"}, nil,
		map[string]atc.SnapshotInputConfig{"summary": {Type: "opaque/v1"}}, nil)

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: &atc.OnSuccessStep{Step: main.Config, Hook: hook}},
			after,
		},
	})

	for _, edge := range []Edge{
		{From: "main", To: "hook", PortName: "draft", TypeRef: "repository-change/v1"},
		{From: "hook", To: "after", PortName: "summary", TypeRef: "opaque/v1"},
	} {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, g.Edges)
		}
	}

	node, found := g.Node("hook")
	if !found {
		t.Fatalf("expected the hook node in %+v", g.Nodes)
	}
	if !hasDecoration(node, DecorationOnSuccess) {
		t.Fatalf("expected an on_success decoration, got %+v", node.Decorations)
	}
}

// TestBuildEnsureHookSeesAConservativeEnvironment pins checkEnsure and
// conservativeEnsureEnvironment. An ensure hook runs on both outcomes, so the
// main step's writes reach it as alternatives rather than facts: the edge is
// real, but conditional. Walking the hook against main.env directly (the old
// single-map behaviour) would draw the same edge as guaranteed, telling a run
// page that a value which may never have been produced certainly was.
func TestBuildEnsureHookSeesAConservativeEnvironment(t *testing.T) {
	main := agentStep("main", "main", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	hook := agentStep("hook", "hook", []string{"draft"}, []string{"summary"},
		map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1", Optional: true}},
		map[string]atc.SnapshotOutputConfig{"summary": {Type: "opaque/v1"}})

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: &atc.EnsureStep{Step: main.Config, Hook: hook}},
		},
	})

	want := Edge{From: "main", To: "hook", PortName: "draft", TypeRef: "repository-change/v1", Optional: true}
	if !hasEdge(g, want) {
		t.Fatalf("expected the conditional ensure edge %+v in %+v", want, g.Edges)
	}

	node, found := g.Node("hook")
	if !found {
		t.Fatalf("expected the hook node in %+v", g.Nodes)
	}
	if !hasDecoration(node, DecorationEnsure) {
		t.Fatalf("expected an ensure decoration, got %+v", node.Decorations)
	}
}

// TestBuildDoesNotLeakDecorationsBetweenSiblings exercises deep decoration
// composition rather than the shallow two-sibling case it used to.
//
// Three nested wrappers matter: append(nil, x) always allocates, so a
// top-level fork can never create the shared-backing-array hazard at all.
// After three appends a naive append would be working with spare capacity,
// which is where independently-wrapped siblings could tread on each other.
func TestBuildDoesNotLeakDecorationsBetweenSiblings(t *testing.T) {
	tried := agentStep("tried", "tried", []string{"repository"}, []string{"a"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"a": {Type: "opaque/v1"}})
	retried := agentStep("retried", "retried", []string{"repository"}, []string{"b"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"b": {Type: "opaque/v1"}})
	plain := agentStep("plain", "plain", []string{"repository"}, []string{"c"},
		map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
		map[string]atc.SnapshotOutputConfig{"c": {Type: "opaque/v1"}})

	// timeout > retry > try, then a do whose siblings each add their own
	// fourth wrapper.
	nested := &atc.TimeoutStep{Duration: "1h", Step: &atc.RetryStep{Attempts: 2, Step: &atc.TryStep{
		Step: atc.Step{Config: &atc.DoStep{Steps: []atc.Step{
			{Config: &atc.TryStep{Step: tried}},
			{Config: &atc.RetryStep{Attempts: 3, Step: retried.Config}},
			plain,
		}}},
	}}}

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
		Plan:             []atc.Step{{Config: nested}},
	})

	shared := []Decoration{DecorationTimeout, DecorationRetry, DecorationTry}
	for _, expectation := range []struct {
		id   string
		want []Decoration
	}{
		{"tried", append(append([]Decoration(nil), shared...), DecorationTry)},
		{"retried", append(append([]Decoration(nil), shared...), DecorationRetry)},
		{"plain", shared},
	} {
		node, found := g.Node(expectation.id)
		if !found {
			t.Fatalf("expected node %q in %+v", expectation.id, g.Nodes)
		}
		if len(node.Decorations) != len(expectation.want) {
			t.Fatalf("node %q: expected decorations %+v, got %+v", expectation.id, expectation.want, node.Decorations)
		}
		for index := range expectation.want {
			if node.Decorations[index] != expectation.want[index] {
				t.Fatalf("node %q: expected decorations %+v, got %+v", expectation.id, expectation.want, node.Decorations)
			}
		}
	}
}

// TestBuildEmptyGraphMarshalsAsEmptyArrays pins I10. Nodes and Edges are
// decoded by the Elm frontend with Json.Decode.list, which fails outright on
// null, so nil slices here would break the page rather than render an empty
// workflow. This fixture deliberately skips buildTypeChecked: an empty plan
// is not a valid function, and the assertion is about the serialization
// contract of the degenerate graph, not about flow.
func TestBuildEmptyGraphMarshalsAsEmptyArrays(t *testing.T) {
	g, err := Build(&workflow.FunctionConfig{SignatureVersion: 1})
	if err != nil {
		t.Fatalf("Build returned an error: %v", err)
	}
	encoded, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshalling graph: %v", err)
	}
	if string(encoded) != `{"nodes":[],"edges":[]}` {
		t.Fatalf(`an empty graph must marshal as {"nodes":[],"edges":[]}, got %s`, encoded)
	}
}

// TestBuildRejectsNodeIDCollidingWithAnEndpoint pins I7. An agent whose
// function_id is literally "output:change" type-checks fine —
// registerNodeIdentity only guards plan-node identities against each other,
// and public ports never go through it. Silently de-duplicating the collision
// made a node vanish from the graph with no diagnostic, the same class of
// loss the kind-qualified prefixes were introduced to fix.
//
// The output-prefix case is the discriminating one. An agent colliding with
// an input node would also trip link's self-loop guard (it consumes the very
// binding the input node produces), so it cannot prove addNode itself
// rejects the collision; here the terminal edge comes from a third node and
// nothing else can catch it.
func TestBuildRejectsNodeIDCollidingWithAnEndpoint(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		functionID string
	}{
		{"output prefix", "output:change"},
		{"input prefix", "input:repository"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			function := &workflow.FunctionConfig{
				SignatureVersion: 1,
				Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
				Outputs: []workflow.FunctionOutput{
					{Port: snapshot.Port{Name: "change", Type: "repository-change/v1"}, From: "candidate"},
				},
				Plan: []atc.Step{
					agentStep("collide", testCase.functionID,
						[]string{"repository"}, []string{"draft"},
						map[string]atc.SnapshotInputConfig{"repository": {Type: "repository/v1"}},
						map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}}),
					agentStep("review", "review",
						[]string{"draft"}, []string{"candidate"},
						map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}},
						map[string]atc.SnapshotOutputConfig{"candidate": {Type: "repository-change/v1"}}),
				},
			}
			if err := workflow.TypeCheckFunction(function); err != nil {
				t.Fatalf("this collision is meant to type-check; the graph is the only layer that can catch it: %v", err)
			}
			if _, err := Build(function); err == nil {
				t.Fatal("expected Build to reject a node id colliding with an endpoint node")
			}
		})
	}
}

// TestBuildShowsEveryPossibleProducerOfAnAmbiguousBinding pins the one place
// this package deliberately diverges from typecheck.go.
//
// mergeBindingAlternatives drops snapshotBinding.producer to nil when two
// alternatives disagree, because the checker only needs to know whether one
// exact producer is provable (it is not, which is why a public output cannot
// select this binding). A reader of the graph needs the opposite: every node
// the value could have come from, each marked Optional because a given run
// took exactly one path. Dropping to a single edge would silently hide a real
// dataflow.
func TestBuildShowsEveryPossibleProducerOfAnAmbiguousBinding(t *testing.T) {
	first := agentStep("first", "first", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	inner := agentStep("inner", "inner", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	after := agentStep("after", "after", []string{"draft"}, nil,
		map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}}, nil)

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			first,
			{Config: &atc.TryStep{Step: inner}},
			after,
		},
	})

	want := []Edge{
		{From: "first", To: "after", PortName: "draft", TypeRef: "repository-change/v1", Optional: true},
		{From: "inner", To: "after", PortName: "draft", TypeRef: "repository-change/v1", Optional: true},
	}
	for _, edge := range want {
		if !hasEdge(g, edge) {
			t.Fatalf("expected edge %+v in %+v", edge, edgesInto(g, "after"))
		}
	}
	if len(edgesInto(g, "after")) != len(want) {
		t.Fatalf("expected exactly the two alternative producers, got %+v", edgesInto(g, "after"))
	}
}

// TestBuildRetryDiscardsFailedAttemptWrites pins checkStep's RetryStep arm,
// which resets allProduced to the child's successful writes only: a snapshot
// attempt scope discards every failed attempt, so a write made by a hook on a
// failed attempt can never become externally observable.
//
// The try wrapper is what makes it observable: conditionalTryFlow reads
// allProduced, so a retry that forgot to discard would turn the failure hook
// into a second possible producer of "draft" after the try.
func TestBuildRetryDiscardsFailedAttemptWrites(t *testing.T) {
	first := agentStep("first", "first", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	main := agentStep("main", "main", nil, []string{"note"}, nil,
		map[string]atc.SnapshotOutputConfig{"note": {Type: "opaque/v1"}})
	hook := agentStep("hook", "hook", nil, []string{"draft"}, nil,
		map[string]atc.SnapshotOutputConfig{"draft": {Type: "repository-change/v1"}})
	after := agentStep("after", "after", []string{"draft"}, nil,
		map[string]atc.SnapshotInputConfig{"draft": {Type: "repository-change/v1"}}, nil)

	g := buildTypeChecked(t, &workflow.FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			first,
			{Config: &atc.TryStep{Step: atc.Step{Config: &atc.RetryStep{Attempts: 2, Step: &atc.OnFailureStep{
				Step: main.Config,
				Hook: hook,
			}}}}},
			after,
		},
	})

	want := Edge{From: "first", To: "after", PortName: "draft", TypeRef: "repository-change/v1"}
	if !hasEdge(g, want) {
		t.Fatalf("expected the sole guaranteed producer edge %+v, got %+v", want, edgesInto(g, "after"))
	}
	if len(edgesInto(g, "after")) != 1 {
		t.Fatalf("a write discarded with a failed attempt must not become a producer, got %+v", edgesInto(g, "after"))
	}
}

func buildSeed(t *testing.T, name string) Graph {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(filepath.Join("..", "seeds", name))
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
	return g
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
		name := filepath.Base(filepath.Dir(match))
		t.Run(name, func(t *testing.T) {
			g := buildSeed(t, name)
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
	g := buildSeed(t, "small-fix-v3")

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

// A PR-reapproval step may legally name ONE binding for both the candidate's
// authoritative validation (AwaitSnapshotStep.Validation) and the accepted
// review's (PRApproval.AcceptedReview.Validation): the type checker constrains
// them independently — requireValidation proves the step's validation is
// authoritative dev validation dominating the candidate, requireExactAwaitArtifact
// only proves the accepted validation is a non-conditional validation/v1 — and
// nothing requires the two names to differ. Both used to be linked in their own
// loop with no de-duplication, so one binding satisfying both produced two
// byte-identical edges.
//
// The walk methods are exercised directly rather than through a mutated seed:
// the reachable configuration is a property of the two independent constraints
// above, not of any shipped workflow, and Build already documents that it
// assumes a type-checked function.
func TestWalkAwaitSnapshotDrawsOneEdgePerSharedValidationBinding(t *testing.T) {
	b := &builder{graph: Graph{Nodes: []Node{}, Edges: []Edge{}}, seen: map[string]bool{}}
	env := environment{
		"pull-request": typedBinding("observe", "pull-request/v1", false),
		"candidate":    typedBinding("merge-prepare", "repository-change/v1", false),
		"impact":       typedBinding("assess-impact", "publish-impact/v1", false),
		"response":     typedBinding("compose", "pull-request-response/v1", false),
		"review":       typedBinding("load-review", "review/v1", false),
		"validation":   typedBinding("dev-validation-pr-revision-gates", "validation/v1", false),
	}

	if _, err := b.walkAwaitSnapshot(&atc.AwaitSnapshotStep{
		Name: "reapproval",
		Type: "human-answer/v1",
		// Both validation slots name the same binding.
		Validation: "validation",
		PRApproval: &atc.PRApprovalIntent{
			Observation: "pull-request",
			Candidate:   "candidate",
			Impact:      "impact",
			Response:    "response",
			AcceptedReview: &atc.PRAcceptedReviewIntent{
				Review:     "review",
				Candidate:  "candidate",
				Validation: "validation",
			},
		},
	}, env, nil); err != nil {
		t.Fatalf("walkAwaitSnapshot returned an error: %v", err)
	}

	// Six distinct bindings: pull-request, candidate, impact, response, review,
	// validation. `candidate` is named twice (the intent's and the accepted
	// review's) and `validation` twice, and each draws exactly one edge.
	assertNoDuplicateEdges(t, b.graph)
	if got := len(edgesInto(b.graph, "reapproval")); got != 6 {
		t.Fatalf("expected one edge per distinct consumed binding, got %d: %+v",
			got, edgesInto(b.graph, "reapproval"))
	}
}

func TestWalkPublishSnapshotDrawsOneEdgePerSharedValidationBinding(t *testing.T) {
	b := &builder{graph: Graph{Nodes: []Node{}, Edges: []Edge{}}, seen: map[string]bool{}}
	env := environment{
		"candidate":  typedBinding("merge-prepare", "repository-change/v1", false),
		"reapproval": typedBinding("reapproval", "human-answer/v1", true),
		"review":     typedBinding("load-review", "review/v1", false),
		"validation": typedBinding("dev-validation-pr-revision-gates", "validation/v1", false),
	}

	if _, err := b.walkPublishSnapshot(&atc.PublishSnapshotStep{
		Name:       "publish-revision",
		Input:      "candidate",
		InputType:  "repository-change/v1",
		Approval:   "reapproval",
		Validation: "validation",
		PRApproval: &atc.PRApprovalPublicationIntent{
			AcceptedReview: &atc.PRAcceptedReviewIntent{
				Review:     "review",
				Candidate:  "candidate",
				Validation: "validation",
			},
		},
	}, env, nil); err != nil {
		t.Fatalf("walkPublishSnapshot returned an error: %v", err)
	}

	assertNoDuplicateEdges(t, b.graph)
	if got := len(edgesInto(b.graph, "publish-revision")); got != 4 {
		t.Fatalf("expected one edge per distinct consumed binding, got %d: %+v",
			got, edgesInto(b.graph, "publish-revision"))
	}
}
