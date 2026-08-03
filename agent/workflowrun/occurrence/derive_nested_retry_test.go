package occurrence

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

// nestedRetryCompiled authors a retry closure INSIDE another retry closure,
// which the type checker permits (it tracks retryDepth but never bounds it) and
// which atc/builds/planner.go materializes multiplicatively: outer * inner full
// copies of the leaf, each with its own plan ID.
func nestedRetryCompiled(t *testing.T) *workflow.CompiledDefinition {
	t.Helper()
	return compileManifest(t, map[string]string{
		"workflow.yaml": `
schema_version: 3
name: nested-retrying
signature_version: 1
disposition_output: draft
description: A workflow whose retried block retries its own step.
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
  - do:
      - agent: implement
        attempts: 2
        budget_slice_usd: 5
        function_id: implement
        prompt_file: prompts/implement.md
        inputs: [repository]
        outputs: [attempted]
        input_types:
          repository: {type: repository/v1}
        output_types:
          attempted: repository-change/v1
    attempts: 3
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

// The projection is keyed (workflow_run_id, node_id, retry_attempt, attempt).
// walkPlan numbers a copy by its INNERMOST enclosing retry index, so under
// nested retry the six materialized copies of one node carried only two
// distinct numbers — and Freeze's ON CONFLICT DO NOTHING silently discarded
// four rows of real, unrecoverable history. Every copy must key uniquely.
func TestDeriveKeepsEveryNestedRetryCopyDistinct(t *testing.T) {
	sources := sourcesForSeed(t, nestedRetryCompiled(t))

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
	if len(copies) != 6 {
		t.Fatalf("expected 3 outer x 2 inner materialized copies, got %d: %+v", len(copies), copies)
	}

	// Every copy ran, which is what an exhausted inner closure inside a firing
	// outer closure looks like.
	for index, copied := range copies {
		status := "failed"
		if index == len(copies)-1 {
			status = "ok"
		}
		sources.AttemptMetrics = append(sources.AttemptMetrics, AttemptMetric{
			PlanID: copied.PlanID, ExecutionAttempt: 1, Status: status,
		})
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}

	type key struct {
		node    string
		retry   int
		attempt int
	}
	seen := map[key]string{}
	implement := 0
	for _, occurrence := range occurrences {
		if occurrence.NodeID != "implement" {
			continue
		}
		implement++
		identity := key{occurrence.NodeID, occurrence.RetryAttempt, occurrence.Attempt}
		if previous, duplicate := seen[identity]; duplicate {
			t.Fatalf("plan copies %q and %q collide on the projection key %+v",
				previous, occurrence.PlanID, identity)
		}
		seen[identity] = occurrence.PlanID
	}
	if implement != 6 {
		t.Fatalf("expected one occurrence per materialized copy, got %d", implement)
	}
	// The copy that finally succeeded is the LAST one, and it must survive: the
	// old key kept the first insert per collision, which was always an early
	// failure, so a run that succeeded froze with its node showing failed.
	last := seen[key{"implement", 6, 1}]
	if last != copies[5].PlanID {
		t.Fatalf("the last copy is keyed %q, want plan %q", last, copies[5].PlanID)
	}
}

// A single-level retry closure is by far the common case and its numbering must
// not move: the ordinal within the node group is exactly the plan's own retry
// index there.
func TestDeriveNumbersSingleLevelRetryCopiesUnchanged(t *testing.T) {
	sources := sourcesForSeed(t, retryCompiled(t))
	nodes, err := PlanNodes(sources.Run.ActualPlan)
	if err != nil {
		t.Fatalf("PlanNodes: %v", err)
	}
	for _, node := range nodes {
		if node.NodeID != "implement" {
			continue
		}
		sources.AttemptMetrics = append(sources.AttemptMetrics, AttemptMetric{
			PlanID: node.PlanID, ExecutionAttempt: 1, Status: "failed",
		})
	}

	occurrences, err := Derive(sources)
	if err != nil {
		t.Fatalf("Derive returned an error: %v", err)
	}
	want := 1
	for _, occurrence := range occurrences {
		if occurrence.NodeID != "implement" {
			continue
		}
		if occurrence.RetryAttempt != want {
			t.Fatalf("copy %d: retry attempt = %d", want, occurrence.RetryAttempt)
		}
		want++
	}
	if want != 4 {
		t.Fatalf("expected three retry copies, saw %d", want-1)
	}
}
