package occurrence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// codeReviewSources builds Sources over the real code-review-v3 seed, planned
// through the real planner, so the derivation is exercised against a plan the
// rest of the system actually produces.
func codeReviewSources(t *testing.T) Sources {
	t.Helper()
	return sourcesForSeed(t, compileSeed(t, "code-review-v3"))
}

func TestDeriveAgentNodeFromAttemptMetrics(t *testing.T) {
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	completed := started.Add(4 * time.Minute)

	sources := codeReviewSources(t)
	reviewPlanID := planIDOf(t, sources.Run.ActualPlan, "review")
	sources.AttemptMetrics = []AttemptMetric{{
		PlanID:           reviewPlanID,
		ExecutionAttempt: 1,
		Status:           "ok",
		CostUSD:          1.25,
		CreatedAt:        started,
		UpdatedAt:        completed,
	}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}

	got, found := findOccurrence(occurrences, "review")
	if !found {
		t.Fatalf("expected an occurrence for the agent node, got %+v", occurrences)
	}
	if got.NodeKind != KindAgent {
		t.Fatalf("unexpected node kind: %+v", got)
	}
	if got.Status != StatusSucceeded {
		t.Fatalf("expected succeeded, got %q", got.Status)
	}
	if got.Attempt != 1 || got.RetryAttempt != 1 || got.CostUSD != 1.25 {
		t.Fatalf("unexpected attempt or cost: %+v", got)
	}
	if got.WorkflowRunID != 42 || got.TeamID != 1 || got.WorkflowName != "code-review" ||
		got.WorkflowDefinitionID != 41 || got.WorkflowVersion != 3 {
		t.Fatalf("run identity was not carried onto the occurrence: %+v", got)
	}
	if got.PlanID != reviewPlanID {
		t.Fatalf("expected plan ID %q, got %q", reviewPlanID, got.PlanID)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("expected started at %v, got %v", started, got.StartedAt)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Fatalf("expected completed at %v, got %v", completed, got.CompletedAt)
	}
	if got.DurationSeconds != 240 {
		t.Fatalf("expected 240 seconds, got %d", got.DurationSeconds)
	}
}

// 'incomplete' maps to succeeded because migration 1773106126 documents it as
// a missing flight RECORDING on an otherwise successful step, not a failed
// step, and DeriveOutcome already fuses it to amber on a succeeded build.
// Rendering it red on the canvas would be a false alarm.
func TestDeriveMapsAgentFailureAndError(t *testing.T) {
	for status, want := range map[string]Status{
		"ok":         StatusSucceeded,
		"failed":     StatusFailed,
		"error":      StatusErrored,
		"incomplete": StatusSucceeded,
		"parked":     StatusRunning,
	} {
		sources := codeReviewSources(t)
		sources.AttemptMetrics = []AttemptMetric{{
			PlanID:           planIDOf(t, sources.Run.ActualPlan, "review"),
			ExecutionAttempt: 1,
			Status:           status,
		}}
		occurrences, err := Derive(sources)
		if err != nil {
			t.Fatalf("Derive returned an error: %v", err)
		}
		got, found := findOccurrence(occurrences, "review")
		if !found {
			t.Fatalf("metric status %q: no occurrence for the agent node", status)
		}
		if got.Status != want {
			t.Fatalf("metric status %q: expected %q, got %q", status, want, got.Status)
		}
	}
}

// A non-terminal status must not be given a completion time, or the projection
// would claim the step finished when it has not.
func TestDeriveLeavesNonTerminalOccurrencesOpen(t *testing.T) {
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	sources := codeReviewSources(t)
	sources.AttemptMetrics = []AttemptMetric{{
		PlanID:           planIDOf(t, sources.Run.ActualPlan, "review"),
		ExecutionAttempt: 1,
		Status:           "parked",
		CreatedAt:        started,
		UpdatedAt:        started.Add(time.Hour),
	}}
	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "review")
	if got.Status != StatusRunning {
		t.Fatalf("expected running, got %q", got.Status)
	}
	if got.CompletedAt != nil || got.DurationSeconds != 0 {
		t.Fatalf("a running occurrence must not be completed: %+v", got)
	}
}

func TestDeriveEmitsPendingForUnreachedNode(t *testing.T) {
	occurrences, err := Derive(codeReviewSources(t))
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, found := findOccurrence(occurrences, "review")
	if !found {
		t.Fatalf("expected the unreached node to still be projected, got %+v", occurrences)
	}
	if got.Status != StatusPending {
		t.Fatalf("expected pending, got %q", got.Status)
	}
	if got.StartedAt != nil || got.CompletedAt != nil {
		t.Fatalf("a pending occurrence has no times: %+v", got)
	}
}

// Deterministic task steps have no durable metrics row, so the freeze reads
// their terminal state from build step state while it still exists.
func TestDeriveUsesBuildStepStatusWhenThereAreNoMetrics(t *testing.T) {
	sources := sourcesForSeed(t, compileSeed(t, "measure-review-v3"))
	taskPlanID := planIDOf(t, sources.Run.ActualPlan, "measure-review")
	sources.BuildStepStatus = map[string]Status{taskPlanID: StatusSucceeded}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, found := findOccurrence(occurrences, "measure-review")
	if !found {
		t.Fatalf("expected an occurrence for the task node, got %+v", occurrences)
	}
	if got.NodeKind != KindTask || got.Status != StatusSucceeded {
		t.Fatalf("expected a succeeded task occurrence, got %+v", got)
	}
}

// Metrics win over build step state: a metrics row is the more precise record
// of the same step, and reading both would let one contradict the other.
func TestDeriveMetricsOutrankBuildStepStatus(t *testing.T) {
	sources := codeReviewSources(t)
	reviewPlanID := planIDOf(t, sources.Run.ActualPlan, "review")
	sources.AttemptMetrics = []AttemptMetric{{PlanID: reviewPlanID, ExecutionAttempt: 1, Status: "failed"}}
	sources.BuildStepStatus = map[string]Status{reviewPlanID: StatusSucceeded}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "review")
	if got.Status != StatusFailed {
		t.Fatalf("expected the metrics row to win, got %q", got.Status)
	}
}

func TestDeriveEmitsOneOccurrencePerRecoveryAttempt(t *testing.T) {
	sources := codeReviewSources(t)
	reviewPlanID := planIDOf(t, sources.Run.ActualPlan, "review")
	sources.AttemptMetrics = []AttemptMetric{
		{PlanID: reviewPlanID, ExecutionAttempt: 1, Status: "error", CostUSD: 0.5},
		{PlanID: reviewPlanID, ExecutionAttempt: 2, Status: "ok", CostUSD: 1.5},
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	var review []NodeOccurrence
	for _, occurrence := range occurrences {
		if occurrence.NodeID == "review" {
			review = append(review, occurrence)
		}
	}
	if len(review) != 2 {
		t.Fatalf("expected one occurrence per recovery attempt, got %+v", review)
	}
	if review[0].Attempt != 1 || review[0].Status != StatusErrored {
		t.Fatalf("unexpected first attempt: %+v", review[0])
	}
	if review[1].Attempt != 2 || review[1].Status != StatusSucceeded {
		t.Fatalf("unexpected second attempt: %+v", review[1])
	}
}

func TestDeriveRequiresAFrozenActualPlan(t *testing.T) {
	_, err := Derive(Sources{Run: db.AgentWorkflowRun{ID: 42}})
	if err == nil {
		t.Fatal("expected an error when the run has no frozen actual plan")
	}
	// The message must name the run and the missing plan, not surface a raw
	// JSON decode failure, because this is the case a caller has to act on.
	if !strings.Contains(err.Error(), "no frozen actual plan") || !strings.Contains(err.Error(), "42") {
		t.Fatalf("unhelpful error for a missing plan: %v", err)
	}
}

// Times that cannot describe a real interval must not produce a negative or
// nonsense duration: a clock skew or a partially written row would otherwise
// show up on the canvas as a step that took negative time.
func TestDeriveGuardsAgainstUnusableTimestamps(t *testing.T) {
	started := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)

	for name, metric := range map[string]AttemptMetric{
		"completed before started": {ExecutionAttempt: 1, Status: "ok", CreatedAt: started, UpdatedAt: started.Add(-time.Hour)},
		"no start time":            {ExecutionAttempt: 1, Status: "ok", UpdatedAt: started},
		"no completion time":       {ExecutionAttempt: 1, Status: "ok", CreatedAt: started},
	} {
		t.Run(name, func(t *testing.T) {
			sources := codeReviewSources(t)
			metric.PlanID = planIDOf(t, sources.Run.ActualPlan, "review")
			sources.AttemptMetrics = []AttemptMetric{metric}

			occurrences, err := Derive(sources)
			if err != nil {
				t.Fatalf("Derive returned an error: %v", err)
			}
			got, _ := findOccurrence(occurrences, "review")
			if got.DurationSeconds != 0 {
				t.Fatalf("expected no duration, got %d", got.DurationSeconds)
			}
		})
	}
}

// A zero timestamp means the record carries no time, so the occurrence must
// carry no time either rather than pointing at the zero instant.
func TestDeriveOmitsZeroTimestamps(t *testing.T) {
	sources := codeReviewSources(t)
	sources.AttemptMetrics = []AttemptMetric{{
		PlanID:           planIDOf(t, sources.Run.ActualPlan, "review"),
		ExecutionAttempt: 1,
		Status:           "ok",
	}}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	got, _ := findOccurrence(occurrences, "review")
	if got.StartedAt != nil {
		t.Fatalf("expected no start time, got %v", got.StartedAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("expected no completion time, got %v", got.CompletedAt)
	}
}

func TestDeriveRejectsAMalformedActualPlan(t *testing.T) {
	if _, err := Derive(Sources{Run: db.AgentWorkflowRun{ID: 42, ActualPlan: []byte("not json")}}); err == nil {
		t.Fatal("expected an error for a malformed actual plan")
	}
}

// The projection stores only nodes the graph contains.
//
// RenderFunction prepends a synthetic load_snapshot per input port, named with
// the BARE port name, so code-review-v3's plan contains loads called "before"
// and "after" while graph.Build calls the same concepts "input:before" and
// "input:after". Those are endpoint nodes, which never carry occurrences, so
// the synthetic loads must not reach the projection — otherwise Phase C's
// join from a graph node to its occurrence row would have to guess across
// three prefixes.
func TestDeriveDropsSyntheticInputPortLoads(t *testing.T) {
	sources := codeReviewSources(t)

	// The plan really does contain them, so this is a filter and not an
	// accident of the fixture.
	nodes, err := PlanNodes(sources.Run.ActualPlan)
	if err != nil {
		t.Fatalf("PlanNodes: %v", err)
	}
	for _, name := range []string{"before", "after"} {
		var inPlan bool
		for _, node := range nodes {
			if node.NodeID == name && node.Kind == KindLoad {
				inPlan = true
			}
		}
		if !inPlan {
			t.Fatalf("expected the plan to contain a synthetic load %q, got %+v", name, nodes)
		}
		if _, inGraph := sources.ExecutionNodes[name]; inGraph {
			t.Fatalf("expected %q to be an endpoint rather than an execution node", name)
		}
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, name := range []string{"before", "after"} {
		if got, found := findOccurrence(occurrences, name); found {
			t.Fatalf("synthetic input-port load %q must not be projected, got %+v", name, got)
		}
	}
	if _, found := findOccurrence(occurrences, "review"); !found {
		t.Fatalf("the filter must keep real execution nodes, got %+v", occurrences)
	}
}

// The other half of the same rule: an authored load_snapshot IS a graph
// execution node with a bare ID, so it is kept — and it is kept without a
// special case, purely because the graph contains it.
//
// The fixture is assembled directly because RenderFunction currently rejects
// authored load_snapshot steps outright (render.go: "workflow inputs are
// loaded by the renderer"), so no seed can produce one. graph.Build models
// them regardless, and the filter must agree with the graph rather than with
// today's renderer policy.
func TestDeriveKeepsAnAuthoredLoadSnapshotWhileDroppingASyntheticOne(t *testing.T) {
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			// What RenderFunction prepends for the input port "before".
			{ID: "1/1", LoadSnapshot: &atc.LoadSnapshotPlan{Name: "before", Type: "repository/v1"}},
			// What an author wrote.
			{ID: "1/2", LoadSnapshot: &atc.LoadSnapshotPlan{Name: "baseline", Type: "repository/v1"}},
			{ID: "1/3", Agent: &atc.AgentPlan{Name: "review", FunctionID: "review"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	occurrences, err := Derive(Sources{
		Run: db.AgentWorkflowRun{ID: 42, TeamID: 1, WorkflowName: "authored-load", ActualPlan: raw},
		// graph.Build names the input port "input:before" and the authored
		// load "baseline".
		ExecutionNodes: ExecutionNodesOf(graph.Graph{Nodes: []graph.Node{
			{ID: "input:before", Kind: graph.KindInput},
			{ID: "baseline", Kind: graph.KindLoad},
			{ID: "review", Kind: graph.KindAgent},
		}}),
	})
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}

	if got, found := findOccurrence(occurrences, "before"); found {
		t.Fatalf("the synthetic input-port load must be dropped, got %+v", got)
	}
	got, found := findOccurrence(occurrences, "baseline")
	if !found {
		t.Fatalf("the authored load_snapshot must be projected, got %+v", occurrences)
	}
	if got.NodeKind != KindLoad {
		t.Fatalf("expected the load kind, got %q", got.NodeKind)
	}
}

// A plan node whose identity the graph knows under a different kind is not the
// same node. Keeping it would put two different concepts on one projection key
// — an input port named "review" and an agent function named "review" both
// land on (run, "review", 1).
func TestDeriveDropsAPlanNodeWhoseGraphKindDiffers(t *testing.T) {
	raw, err := json.Marshal(atc.Plan{
		ID: "1",
		Do: &atc.DoPlan{
			{ID: "1/1", LoadSnapshot: &atc.LoadSnapshotPlan{Name: "review", Type: "repository/v1"}},
			{ID: "1/2", Agent: &atc.AgentPlan{Name: "review", FunctionID: "review"}},
		},
	})
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}

	occurrences, err := Derive(Sources{
		Run:            db.AgentWorkflowRun{ID: 42, TeamID: 1, ActualPlan: raw},
		ExecutionNodes: map[string]string{"review": KindAgent},
	})
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	if len(occurrences) != 1 {
		t.Fatalf("expected only the agent node, got %+v", occurrences)
	}
	if occurrences[0].NodeKind != KindAgent || occurrences[0].PlanID != "1/2" {
		t.Fatalf("expected the agent copy to survive, got %+v", occurrences[0])
	}
}

// An absent node set would project nothing at all, so at the freeze call site
// it would silently discard a run's whole history. It must be an error the
// caller has to see.
func TestDeriveRequiresTheGraphExecutionNodeSet(t *testing.T) {
	sources := codeReviewSources(t)
	sources.ExecutionNodes = nil

	_, err := Derive(sources)
	if err == nil {
		t.Fatal("expected an error when the graph execution-node set is missing")
	}
	if !strings.Contains(err.Error(), "no graph execution nodes") || !strings.Contains(err.Error(), "42") {
		t.Fatalf("unhelpful error for a missing node set: %v", err)
	}
}

// ExecutionNodesOf answers "which kinds may carry occurrences", so an endpoint
// kind sneaking in would reopen the ambiguity the filter exists to close.
func TestExecutionNodesOfKeepsOnlyOccurrenceBearingKinds(t *testing.T) {
	nodes := ExecutionNodesOf(graph.Graph{Nodes: []graph.Node{
		{ID: "input:before", Kind: graph.KindInput},
		{ID: "source:repo", Kind: graph.KindResourceSource},
		{ID: "output:draft", Kind: graph.KindOutput},
		{ID: "baseline", Kind: graph.KindLoad},
		{ID: "review", Kind: graph.KindAgent},
		{ID: "measure", Kind: graph.KindTask},
		{ID: "approval", Kind: graph.KindAwait},
		{ID: "ship", Kind: graph.KindPublish},
	}})

	want := map[string]string{
		"baseline": KindLoad, "review": KindAgent, "measure": KindTask,
		"approval": KindAwait, "ship": KindPublish,
	}
	if len(nodes) != len(want) {
		t.Fatalf("expected %d execution nodes, got %+v", len(want), nodes)
	}
	for id, kind := range want {
		if nodes[id] != kind {
			t.Fatalf("node %q: expected kind %q, got %q", id, kind, nodes[id])
		}
	}
}

// A retry materializes several plan copies of one node. Copies that never ran
// are not separate facts: projecting each as pending would leave a finished
// workflow showing attention-worthy pending nodes that never existed.
func TestDeriveOnlyProjectsRetryCopiesThatHaveEvidence(t *testing.T) {
	sources := sourcesForSeed(t, retryCompiled(t))

	nodes, err := PlanNodes(sources.Run.ActualPlan)
	if err != nil {
		t.Fatalf("PlanNodes: %v", err)
	}
	var copies []PlanNode
	for _, node := range nodes {
		if node.NodeID == "implement" {
			copies = append(copies, node)
		}
	}
	if len(copies) != 3 {
		t.Fatalf("expected the planner to materialize three retry copies, got %+v", copies)
	}

	sources.AttemptMetrics = []AttemptMetric{
		{PlanID: copies[0].PlanID, ExecutionAttempt: 1, Status: "failed"},
		{PlanID: copies[1].PlanID, ExecutionAttempt: 1, Status: "ok"},
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	var implement []NodeOccurrence
	for _, occurrence := range occurrences {
		if occurrence.NodeID == "implement" {
			implement = append(implement, occurrence)
		}
	}
	if len(implement) != 2 {
		t.Fatalf("expected only the two retry copies with evidence, got %+v", implement)
	}
	if implement[0].RetryAttempt != 1 || implement[0].Status != StatusFailed {
		t.Fatalf("unexpected first retry copy: %+v", implement[0])
	}
	if implement[1].RetryAttempt != 2 || implement[1].Status != StatusSucceeded {
		t.Fatalf("unexpected second retry copy: %+v", implement[1])
	}
}

// When no retry copy ran at all, the node is still in the plan, so it is
// projected once as pending rather than once per unexecuted copy.
func TestDeriveProjectsAnUnreachedRetryNodeExactlyOnce(t *testing.T) {
	occurrences, err := Derive(sourcesForSeed(t, retryCompiled(t)))
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	var implement []NodeOccurrence
	for _, occurrence := range occurrences {
		if occurrence.NodeID == "implement" {
			implement = append(implement, occurrence)
		}
	}
	if len(implement) != 1 {
		t.Fatalf("expected exactly one pending occurrence, got %+v", implement)
	}
	if implement[0].Status != StatusPending || implement[0].RetryAttempt != 1 {
		t.Fatalf("unexpected occurrence: %+v", implement[0])
	}
}

// Every seed must derive cleanly, and every occurrence-bearing graph node must
// appear in the projection with a valid status.
func TestDeriveCoversEverySeedWorkflow(t *testing.T) {
	for _, name := range seedNames(t) {
		t.Run(name, func(t *testing.T) {
			sources := sourcesForSeed(t, compileSeed(t, name))
			occurrences, err := Derive(sources)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if len(occurrences) == 0 {
				t.Fatal("expected occurrences")
			}
			seen := map[string]bool{}
			for _, occurrence := range occurrences {
				if err := occurrence.Status.Validate(); err != nil {
					t.Fatalf("%+v: %v", occurrence, err)
				}
				if occurrence.Status != StatusPending {
					t.Fatalf("a run with no evidence must be entirely pending, got %+v", occurrence)
				}
				if occurrence.NodeID == "" || occurrence.NodeKind == "" {
					t.Fatalf("occurrence has no identity: %+v", occurrence)
				}
				// Every projected identity must be a graph node of the same
				// kind, which is what makes Phase C's join exact.
				if sources.ExecutionNodes[occurrence.NodeID] != occurrence.NodeKind {
					t.Fatalf("occurrence %q (%s) is not a graph execution node; graph has %v",
						occurrence.NodeID, occurrence.NodeKind, sources.ExecutionNodes)
				}
				seen[occurrence.NodeID] = true
			}
			// And every graph execution node must be projected, or durable
			// history would silently omit it.
			for nodeID := range sources.ExecutionNodes {
				if !seen[nodeID] {
					t.Fatalf("graph execution node %q has no occurrence", nodeID)
				}
			}
		})
	}
}

func TestStatusValidateRejectsUnknownStatuses(t *testing.T) {
	if err := Status("nonsense").Validate(); err == nil {
		t.Fatal("expected an unknown status to be rejected")
	}
	for _, status := range []Status{
		StatusPending, StatusRunning, StatusWaiting, StatusSucceeded,
		StatusFailed, StatusErrored, StatusAborted, StatusSkipped,
	} {
		if err := status.Validate(); err != nil {
			t.Fatalf("%q should be valid: %v", status, err)
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	for status, want := range map[Status]bool{
		StatusPending:   false,
		StatusRunning:   false,
		StatusWaiting:   false,
		StatusSucceeded: true,
		StatusFailed:    true,
		StatusErrored:   true,
		StatusAborted:   true,
		StatusSkipped:   true,
	} {
		if status.Terminal() != want {
			t.Fatalf("%q: expected terminal %v", status, want)
		}
	}
}

func findOccurrence(occurrences []NodeOccurrence, nodeID string) (NodeOccurrence, bool) {
	for _, occurrence := range occurrences {
		if occurrence.NodeID == nodeID {
			return occurrence, true
		}
	}
	return NodeOccurrence{}, false
}

// retryCompiled compiles a workflow whose only agent step is wrapped in a
// retry, so the real planner materializes several copies of one node.
func retryCompiled(t *testing.T) *workflow.CompiledDefinition {
	t.Helper()
	return compileManifest(t, map[string]string{
		"workflow.yaml": `
schema_version: 3
name: retrying
signature_version: 1
disposition_output: draft
description: A workflow whose first step is retried.
inputs:
  - name: repository
    type: repository/v1
    description: Repository state to change.
outputs:
  - name: draft
    type: repository-change/v1
    from: draft
    description: The proposed change.
plan:
  - agent: implement
    attempts: 3
    budget_slice_usd: 5
    function_id: implement
    prompt_file: prompts/implement.md
    inputs: [repository]
    outputs: [attempted]
    input_types:
      repository: {type: repository/v1}
    output_types:
      attempted: repository-change/v1
  - agent: finalize
    budget_slice_usd: 5
    function_id: finalize
    prompt_file: prompts/finalize.md
    inputs: [attempted]
    outputs: [draft]
    input_types:
      attempted: {type: repository-change/v1}
    output_types:
      draft: repository-change/v1
`,
		"prompts/implement.md": "Implement the change.",
		"prompts/finalize.md":  "Finalize the change.",
	})
}
