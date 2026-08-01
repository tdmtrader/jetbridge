package occurrence

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

// codeReviewSources builds Sources over the real code-review-v3 seed, planned
// through the real planner, so the derivation is exercised against a plan the
// rest of the system actually produces.
func codeReviewSources(t *testing.T) Sources {
	t.Helper()
	compiled := compileSeed(t, "code-review-v3")
	return Sources{
		Run: db.AgentWorkflowRun{
			ID:                   42,
			TeamID:               1,
			WorkflowName:         compiled.Name,
			WorkflowDefinitionID: 41,
			WorkflowVersion:      3,
			ActualPlan:           planSeed(t, compiled),
		},
	}
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
	compiled := compileSeed(t, "measure-review-v3")
	sources := Sources{
		Run: db.AgentWorkflowRun{
			ID: 42, TeamID: 1, WorkflowName: compiled.Name,
			WorkflowDefinitionID: 41, WorkflowVersion: 3,
			ActualPlan: planSeed(t, compiled),
		},
	}
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

// The synthetic loads RenderFunction prepends are real plan steps that can
// fail, so they are projected like any other execution node.
func TestDeriveProjectsSyntheticLoadNodes(t *testing.T) {
	occurrences, err := Derive(codeReviewSources(t))
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	for _, name := range []string{"before", "after"} {
		got, found := findOccurrence(occurrences, name)
		if !found {
			t.Fatalf("expected an occurrence for load %q, got %+v", name, occurrences)
		}
		if got.NodeKind != KindLoad {
			t.Fatalf("expected load kind for %q, got %q", name, got.NodeKind)
		}
	}
}

// A retry materializes several plan copies of one node. Copies that never ran
// are not separate facts: projecting each as pending would leave a finished
// workflow showing attention-worthy pending nodes that never existed.
func TestDeriveOnlyProjectsRetryCopiesThatHaveEvidence(t *testing.T) {
	compiled := retryCompiled(t)
	sources := Sources{
		Run: db.AgentWorkflowRun{
			ID: 42, TeamID: 1, WorkflowName: compiled.Name,
			WorkflowDefinitionID: 41, WorkflowVersion: 3,
			ActualPlan: planSeed(t, compiled),
		},
	}

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
	compiled := retryCompiled(t)
	occurrences, err := Derive(Sources{
		Run: db.AgentWorkflowRun{
			ID: 42, TeamID: 1, WorkflowName: compiled.Name,
			WorkflowDefinitionID: 41, WorkflowVersion: 3,
			ActualPlan: planSeed(t, compiled),
		},
	})
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
			compiled := compileSeed(t, name)
			occurrences, err := Derive(Sources{
				Run: db.AgentWorkflowRun{
					ID: 42, TeamID: 1, WorkflowName: compiled.Name,
					WorkflowDefinitionID: 41, WorkflowVersion: 3,
					ActualPlan: planSeed(t, compiled),
				},
			})
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if len(occurrences) == 0 {
				t.Fatal("expected occurrences")
			}
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
