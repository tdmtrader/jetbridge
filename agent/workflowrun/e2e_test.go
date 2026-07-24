package workflowrun_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun/provenance"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
)

func TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution(t *testing.T) {
	for _, schemaVersion := range []int{1, 2} {
		schemaVersion := schemaVersion
		t.Run(fmt.Sprintf("schema-%d source", schemaVersion), func(t *testing.T) {
			manifest := workflow.Manifest{"workflow.yml": fmt.Sprintf(`
schema_version: %d
name: legacy-%d
steps: []
`, schemaVersion, schemaVersion)}
			compiled, err := workflow.CompileDefinition(manifest)
			if compiled != nil {
				t.Fatalf("CompileDefinition returned %+v, want nil", compiled)
			}
			var unsupported workflow.UnsupportedSchemaVersionError
			if !errors.As(err, &unsupported) {
				t.Fatalf("CompileDefinition error = %T %v, want UnsupportedSchemaVersionError", err, err)
			}
			if unsupported.Got != schemaVersion {
				t.Fatalf("unsupported schema = %d, want %d", unsupported.Got, schemaVersion)
			}
			want := fmt.Sprintf(
				"workflow: unsupported schema_version %d; only schema_version 3 is supported",
				schemaVersion,
			)
			if err.Error() != want {
				t.Fatalf("CompileDefinition error = %q, want %q", err, want)
			}
		})
	}

	persistedLegacy := workflow.Definition{
		ID: 71, Name: "persisted-legacy", Version: 4, SchemaVersion: 2,
		SignatureVersion: 0, ContentHash: "legacy-history",
	}
	target, err := workflow.FullFunctionTarget(persistedLegacy)
	if err == nil {
		t.Fatalf("FullFunctionTarget = %+v, want persisted non-v3 rejection", target)
	}
	if got, want := err.Error(), "workflow: function targets require schema_version 3, got 2"; got != want {
		t.Fatalf("persisted non-v3 error = %q, want %q", got, want)
	}
	if target.WorkflowDefinitionID != 0 || target.WorkflowName != "" || target.Function.Plan != nil {
		t.Fatalf("rejected persisted definition produced executable target: %+v", target)
	}
}

func TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance(t *testing.T) {
	manifest := workflow.Manifest{
		"workflow.yml": `
schema_version: 3
name: review-change
signature_version: 1
inputs:
  - name: before
    type: repository/v1
  - name: after
    type: repository/v1
outputs:
  - name: review
    type: review/v1
    from: review
plan:
  - agent: review
    function_id: review
    prompt_file: prompts/review.md
    inputs: [before, after]
    outputs: [review]
    input_types:
      before: {type: repository/v1}
      after: {type: repository/v1}
    output_types:
      review: review/v1
`,
		"prompts/review.md": "Review the immutable repository states.",
	}

	compiled, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	definition := workflow.Definition{
		ID:               41,
		Name:             compiled.Name,
		Version:          3,
		SchemaVersion:    compiled.SchemaVersion,
		SignatureVersion: compiled.Function.SignatureVersion,
		ContentHash:      "definition-hash",
		Compiled:         *compiled,
	}
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}

	const workflowRunID snapshot.WorkflowRunID = 9007199254740993
	const beforeSnapshotID snapshot.SnapshotID = 9007199254740995
	const afterSnapshotID snapshot.SnapshotID = 9007199254740997
	params := map[string]any{
		"workflow_run_id": stringID(workflowRunID),
		"snapshot_before": stringID(beforeSnapshotID),
		"snapshot_after":  stringID(afterSnapshotID),
	}
	validated, err := atc.ValidateRunParams(rendered.Config.Params, params)
	if err != nil {
		t.Fatalf("ValidateRunParams: %v", err)
	}
	concrete, err := atc.MaterializeRunConfig(rendered.Config, 1, 7, validated)
	if err != nil {
		t.Fatalf("MaterializeRunConfig: %v", err)
	}

	job := concrete.Jobs[0]
	plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
		job.StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
	)
	if err != nil {
		t.Fatalf("plan concrete job: %v", err)
	}
	if plan.Do == nil || len(*plan.Do) != 3 {
		t.Fatalf("planned sequence = %#v, want two loads followed by the authored agent", plan)
	}
	steps := *plan.Do
	if steps[0].LoadSnapshot == nil || steps[0].LoadSnapshot.Name != "before" || steps[0].LoadSnapshot.ID != stringID(beforeSnapshotID) {
		t.Fatalf("first plan step = %#v, want concrete before snapshot load", steps[0])
	}
	if steps[1].LoadSnapshot == nil || steps[1].LoadSnapshot.Name != "after" || steps[1].LoadSnapshot.ID != stringID(afterSnapshotID) {
		t.Fatalf("second plan step = %#v, want concrete after snapshot load", steps[1])
	}
	if steps[2].Agent == nil || steps[2].Agent.Name != "review" {
		t.Fatalf("third plan step = %#v, want authored review agent", steps[2])
	}
	declaration := steps[2].Agent.SnapshotOutputs["review"]
	if declaration.Retention != snapshot.RetentionClassWorkflow || declaration.WorkflowPort != "review" ||
		declaration.WorkflowDefinitionID != definition.ID || declaration.WorkflowRunID != stringID(workflowRunID) {
		t.Fatalf("materialized public output = %#v, want durable workflow identity", declaration)
	}

	concreteJSON, err := atc.CanonicalJSON(concrete)
	if err != nil {
		t.Fatalf("canonical concrete config: %v", err)
	}
	captureInput := provenance.Input{
		WorkflowRunID: workflowRunID, WorkflowDefinitionID: definition.ID,
		ConcreteConfig: concreteJSON,
	}
	captured, err := provenance.FromPlan(captureInput, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	if len(captured.Outputs) != 1 || len(captured.Outputs[0].Producers) != 1 {
		t.Fatalf("captured outputs = %#v, want one exact producer", captured.Outputs)
	}
	if got, want := captured.Outputs[0].Producers[0].PlanID, steps[2].ID.String(); got != want {
		t.Fatalf("captured producer plan ID = %q, want %q", got, want)
	}
	if !json.Valid(captured.CanonicalPlan) || !json.Valid(captured.ResolvedDependencies) {
		t.Fatalf("captured provenance is not canonical JSON: plan=%q dependencies=%q", captured.CanonicalPlan, captured.ResolvedDependencies)
	}

	frozen, err := provenance.VerifyFrozen(
		captureInput, captured.CanonicalPlan, captured.PlanHash, captured.ResolvedDependencies,
	)
	if err != nil {
		t.Fatalf("VerifyFrozen: %v", err)
	}
	if string(frozen.CanonicalPlan) != string(captured.CanonicalPlan) || frozen.PlanHash != captured.PlanHash ||
		string(frozen.ResolvedDependencies) != string(captured.ResolvedDependencies) {
		t.Fatalf("frozen provenance changed across canonical round trip")
	}
}

func stringID[T ~int64](id T) string {
	return strconv.FormatInt(int64(id), 10)
}
