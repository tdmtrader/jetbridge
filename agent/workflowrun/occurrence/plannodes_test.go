package occurrence

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestPlanNodesWalksEverySemanticKind(t *testing.T) {
	plan := atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			{ID: "1/1", Agent: &atc.AgentPlan{Name: "implement step", FunctionID: "implement"}},
			{ID: "1/2", Retry: &atc.RetryPlan{
				{ID: "1/2/1", Task: &atc.TaskPlan{Name: "validate", FunctionID: "validate"}},
			}},
			{ID: "1/3", AwaitSnapshot: &atc.AwaitSnapshotPlan{Name: "approval", Type: "interaction/v1"}},
			{ID: "1/4", PublishSnapshot: &atc.PublishSnapshotPlan{Name: "ship", Publisher: "git-branch/v1", InputType: "repository/v1"}},
			{ID: "1/5", LoadSnapshot: &atc.LoadSnapshotPlan{Name: "repository", Type: "repository/v1"}},
		},
	}

	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}

	want := []PlanNode{
		{PlanID: "1/1", NodeID: "implement", Kind: "agent", DisplayName: "implement step", RetryAttempt: 1},
		{PlanID: "1/2/1", NodeID: "validate", Kind: "task", DisplayName: "validate", RetryAttempt: 1},
		{PlanID: "1/3", NodeID: "approval", Kind: "await", DisplayName: "approval", RetryAttempt: 1},
		{PlanID: "1/4", NodeID: "ship", Kind: "publish", DisplayName: "ship", RetryAttempt: 1},
		{PlanID: "1/5", NodeID: "repository", Kind: "load", DisplayName: "repository", RetryAttempt: 1},
	}
	if len(nodes) != len(want) {
		t.Fatalf("expected %d nodes, got %+v", len(want), nodes)
	}
	for index := range want {
		if nodes[index] != want[index] {
			t.Fatalf("node %d: expected %+v, got %+v", index, want[index], nodes[index])
		}
	}
}

// Each copy the planner materializes inside a retry is a distinct attempt of
// the same node. Without a per-copy attempt number every copy would collapse
// onto one identity, which both misreports the canvas and collides on the
// projection's (workflow_run_id, node_id, ...) key.
func TestPlanNodesNumbersRetryCopiesLikeTheEngine(t *testing.T) {
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Retry: &atc.RetryPlan{
			{ID: "1/1", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
			{ID: "1/2", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
			{ID: "1/3", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected one node per retry copy, got %+v", nodes)
	}
	for index, node := range nodes {
		if node.NodeID != "implement" {
			t.Fatalf("copy %d: expected the same node identity, got %q", index, node.NodeID)
		}
		if node.RetryAttempt != index+1 {
			t.Fatalf("copy %d: expected retry attempt %d, got %d", index, index+1, node.RetryAttempt)
		}
	}
}

// A retry wrapping a composite propagates its attempt number to every node in
// the copy, exactly as atc/engine/builder.go propagates Plan.Attempts.
func TestPlanNodesPropagatesRetryAttemptThroughComposites(t *testing.T) {
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Retry: &atc.RetryPlan{
			{ID: "1/1", Do: &atc.DoPlan{
				{ID: "1/1/1", Agent: &atc.AgentPlan{Name: "a", FunctionID: "a"}},
				{ID: "1/1/2", Task: &atc.TaskPlan{Name: "b", FunctionID: "b"}},
			}},
			{ID: "1/2", Do: &atc.DoPlan{
				{ID: "1/2/1", Agent: &atc.AgentPlan{Name: "a", FunctionID: "a"}},
				{ID: "1/2/2", Task: &atc.TaskPlan{Name: "b", FunctionID: "b"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}
	want := map[string]int{"1/1/1": 1, "1/1/2": 1, "1/2/1": 2, "1/2/2": 2}
	if len(nodes) != len(want) {
		t.Fatalf("expected %d nodes, got %+v", len(want), nodes)
	}
	for _, node := range nodes {
		if node.RetryAttempt != want[node.PlanID] {
			t.Fatalf("plan %q: expected retry attempt %d, got %d", node.PlanID, want[node.PlanID], node.RetryAttempt)
		}
	}
}

// Control wrappers are traversed but never emitted: they decorate nodes rather
// than becoming them.
func TestPlanNodesTraversesEveryControlWrapperWithoutEmittingIt(t *testing.T) {
	leaf := func(id, functionID string) atc.Plan {
		return atc.Plan{ID: atc.PlanID(id), Agent: &atc.AgentPlan{Name: functionID, FunctionID: functionID}}
	}

	// Both arms of every hook are expected: an on_failure escalation agent or
	// an ensure cleanup task is real work the overview must show.
	cases := map[string]struct {
		plan atc.Plan
		want []string
	}{
		"do":          {atc.Plan{ID: "w", Do: &atc.DoPlan{leaf("w/1", "one"), leaf("w/2", "two")}}, []string{"one", "two"}},
		"in_parallel": {atc.Plan{ID: "w", InParallel: &atc.InParallelPlan{Steps: []atc.Plan{leaf("w/1", "one"), leaf("w/2", "two")}}}, []string{"one", "two"}},
		"retry":       {atc.Plan{ID: "w", Retry: &atc.RetryPlan{leaf("w/1", "one"), leaf("w/2", "one")}}, []string{"one", "one"}},
		"try":         {atc.Plan{ID: "w", Try: &atc.TryPlan{Step: leaf("w/1", "one")}}, []string{"one"}},
		"timeout":     {atc.Plan{ID: "w", Timeout: &atc.TimeoutPlan{Step: leaf("w/1", "one")}}, []string{"one"}},
		"on_success":  {atc.Plan{ID: "w", OnSuccess: &atc.OnSuccessPlan{Step: leaf("w/1", "one"), Next: leaf("w/2", "two")}}, []string{"one", "two"}},
		"on_failure":  {atc.Plan{ID: "w", OnFailure: &atc.OnFailurePlan{Step: leaf("w/1", "one"), Next: leaf("w/2", "two")}}, []string{"one", "two"}},
		"on_error":    {atc.Plan{ID: "w", OnError: &atc.OnErrorPlan{Step: leaf("w/1", "one"), Next: leaf("w/2", "two")}}, []string{"one", "two"}},
		"on_abort":    {atc.Plan{ID: "w", OnAbort: &atc.OnAbortPlan{Step: leaf("w/1", "one"), Next: leaf("w/2", "two")}}, []string{"one", "two"}},
		"ensure":      {atc.Plan{ID: "w", Ensure: &atc.EnsurePlan{Step: leaf("w/1", "one"), Next: leaf("w/2", "two")}}, []string{"one", "two"}},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(testCase.plan)
			if err != nil {
				t.Fatalf("marshalling plan: %v", err)
			}
			nodes, err := PlanNodes(raw)
			if err != nil {
				t.Fatalf("PlanNodes returned an error: %v", err)
			}
			if len(nodes) != len(testCase.want) {
				t.Fatalf("%s: expected %d nodes, got %+v", name, len(testCase.want), nodes)
			}
			for index, want := range testCase.want {
				if nodes[index].NodeID != want {
					t.Fatalf("%s: node %d: expected %q, got %q", name, index, want, nodes[index].NodeID)
				}
			}
			for _, node := range nodes {
				if node.PlanID == "w" {
					t.Fatalf("%s emitted the wrapper itself as a node: %+v", name, node)
				}
			}
		})
	}
}

// Hook branches carry real work — an on_failure escalation agent is a node the
// overview must show — so both arms are walked.
func TestPlanNodesEmitsHookBranchNodes(t *testing.T) {
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		OnFailure: &atc.OnFailurePlan{
			Step: atc.Plan{ID: "1/1", Agent: &atc.AgentPlan{Name: "implement", FunctionID: "implement"}},
			Next: atc.Plan{ID: "1/2", Agent: &atc.AgentPlan{Name: "escalate", FunctionID: "escalate"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}
	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}
	if len(nodes) != 2 || nodes[0].NodeID != "implement" || nodes[1].NodeID != "escalate" {
		t.Fatalf("expected both hook arms, got %+v", nodes)
	}
}

// An across step's substeps are materialized at runtime with rewritten plan
// IDs, so the frozen template's IDs join to no evidence. Emitting them would
// manufacture permanently-pending nodes for work that ran, so the walker
// deliberately does not descend. This test pins that as an intentional
// decision rather than an accidental gap.
func TestPlanNodesDoesNotDescendIntoAcross(t *testing.T) {
	substep, err := json.Marshal(atc.Plan{
		ID: "1/1", Agent: &atc.AgentPlan{Name: "fan-out", FunctionID: "fan-out"},
	})
	if err != nil {
		t.Fatalf("marshalling substep: %v", err)
	}
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Across: &atc.AcrossPlan{
			Vars:            []atc.AcrossVar{{Var: "target"}},
			SubStepTemplate: string(substep),
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}
	nodes, err := PlanNodes(raw)
	if err != nil {
		t.Fatalf("PlanNodes returned an error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected across substeps to be skipped, got %+v", nodes)
	}
}

func TestPlanNodesRejectsMalformedPlan(t *testing.T) {
	if _, err := PlanNodes([]byte("not json")); err == nil {
		t.Fatal("expected a decode error for a malformed plan")
	}
}

// TestPlanNodesAgreeWithSeedGraphIdentities is the load-bearing contract of
// this phase. Phase C joins occurrence rows to graph nodes by node ID, so
// every identity PlanNodes reads out of a real planned workflow must resolve
// to a node graph.Build produced from the same workflow.
//
// The two derivations run on different artifacts on purpose: graph.Build reads
// the compiled definition, while PlanNodes reads the plan produced after
// RenderFunction prepends a synthetic load_snapshot per input port and per
// bound resource source. Those synthetic loads carry the BARE port name and
// are not execution nodes, so Derive filters them out against this same node
// set. What must hold here is the two-way agreement that filter relies on:
// every graph execution node is reachable from the plan, and every plan node
// the graph knows has the same kind in both.
func TestPlanNodesAgreeWithSeedGraphIdentities(t *testing.T) {
	seeds := seedNames(t)
	if len(seeds) == 0 {
		t.Fatal("expected to find seed workflows")
	}

	for _, name := range seeds {
		t.Run(name, func(t *testing.T) {
			compiled := compileSeed(t, name)
			executionNodes := executionNodesOf(t, compiled)
			if len(executionNodes) == 0 {
				t.Fatal("expected the workflow's graph to contain execution nodes")
			}

			nodes, err := PlanNodes(planSeed(t, compiled))
			if err != nil {
				t.Fatalf("PlanNodes: %v", err)
			}
			if len(nodes) == 0 {
				t.Fatal("expected the planned workflow to contain semantic nodes")
			}

			planned := map[string]string{}
			for _, node := range nodes {
				if node.NodeID == "" {
					t.Fatalf("node %+v has no identity", node)
				}
				planned[node.NodeID] = node.Kind
			}

			for nodeID, kind := range executionNodes {
				plannedKind, found := planned[nodeID]
				if !found {
					t.Fatalf("graph execution node %q (%s) has no plan node; plan has %v", nodeID, kind, planned)
				}
				if plannedKind != kind {
					t.Fatalf("node %q is kind %q in the plan but %q in the graph", nodeID, plannedKind, kind)
				}
			}

			// The reverse direction, with exactly one exception: a load may be
			// one of the synthetic per-input-port or per-resource-source steps
			// the renderer prepends, which the graph models as an endpoint.
			// Any other plan node the graph does not know would be silently
			// dropped from the projection.
			for _, node := range nodes {
				if node.Kind == KindLoad {
					continue
				}
				if executionNodes[node.NodeID] != node.Kind {
					t.Fatalf("plan node %q (%s) is not a graph execution node; graph has %v",
						node.NodeID, node.Kind, executionNodes)
				}
			}
		})
	}
}
