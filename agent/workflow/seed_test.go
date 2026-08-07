package workflow_test

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/atc"
)

func TestOnlySupportedEngineeringSeedsRemain(t *testing.T) {
	entries, err := os.ReadDir("seeds")
	if err != nil {
		t.Fatalf("read seeds: %v", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		if !entry.IsDir() {
			t.Errorf("seed root entry %q is not a directory", entry.Name())
		}
	}
	sort.Strings(names)

	want := []string{
		"anonymization-audit-v3",
		"code-review-node-v1",
		"code-review-v3",
		"log-diagnosis-node-v1",
		"log-diagnosis-v3",
		"measure-review-v3",
		"merge-delivery-v3",
		"small-fix-v3",
		"version-upgrade-v3",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("seed root entries = %q, want %q", names, want)
	}
}

func TestLogDiagnosisReusableNodeSeedFreezesItsAtomicImplementation(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("seeds/log-diagnosis-node-v1")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflow.CompileNodeDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileNodeDefinition: %v", err)
	}
	if definition.SchemaVersion != 1 || definition.Name != "log-diagnosis" {
		t.Fatalf("node identity = schema %d name %q", definition.SchemaVersion, definition.Name)
	}
	if len(definition.Parameters) != 0 {
		t.Fatalf("node parameter contract = %#v, want none", definition.Parameters)
	}
	if len(definition.Function.Inputs) != 2 ||
		definition.Function.Inputs[0].Name != "logs" ||
		definition.Function.Inputs[0].Type != snapshot.TypeRef("log-bundle/v1") ||
		definition.Function.Inputs[0].Optional ||
		definition.Function.Inputs[1].Name != "deployment" ||
		definition.Function.Inputs[1].Type != snapshot.TypeRef("deployment-snapshot/v1") ||
		!definition.Function.Inputs[1].Optional ||
		len(definition.Function.Outputs) != 1 ||
		definition.Function.Outputs[0].Name != "diagnosis" ||
		definition.Function.Outputs[0].Type != snapshot.TypeRef("diagnosis/v1") ||
		definition.Function.Outputs[0].From != "diagnosis" {
		t.Fatalf("node port contract = inputs %#v outputs %#v", definition.Function.Inputs, definition.Function.Outputs)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, port := range definition.Function.Inputs {
		if _, err := registry.Lookup(port.Type); err != nil {
			t.Fatalf("node port %q has no built-in validator: %v", port.Name, err)
		}
	}
	for _, output := range definition.Function.Outputs {
		if _, err := registry.Lookup(output.Type); err != nil {
			t.Fatalf("node port %q has no built-in validator: %v", output.Name, err)
		}
	}
	if len(definition.Function.Plan) != 1 {
		t.Fatalf("node plan has %d steps, want one visible leaf", len(definition.Function.Plan))
	}
	agent, ok := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("node leaf = %T, want *atc.AgentStep", definition.Function.Plan[0].Config)
	}
	if agent.Model != "" || agent.BudgetSliceUSD != 5 || !reflect.DeepEqual(agent.Skills, []string{"diagnosis"}) {
		t.Fatalf("frozen agent implementation = model %q budget %v skills %q", agent.Model, agent.BudgetSliceUSD, agent.Skills)
	}
	if definition.Function.SkillFiles["skills/diagnosis/SKILL.md"] == "" {
		t.Fatalf("compiled skill tree = %#v", definition.Function.SkillFiles)
	}
	if len(agent.Sidecars) != 0 || agent.Capabilities != nil || definition.Function.Capabilities != nil {
		t.Fatalf("portable log-diagnosis node froze mutable capabilities: agent=%#v catalog=%#v sidecars=%#v", agent.Capabilities, definition.Function.Capabilities, agent.Sidecars)
	}
}

func TestCodeReviewReusableNodeSeedFreezesItsAtomicImplementation(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("seeds/code-review-node-v1")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflow.CompileNodeDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileNodeDefinition: %v", err)
	}
	if definition.SchemaVersion != 1 || definition.Name != "code-review" {
		t.Fatalf("node identity = schema %d name %q", definition.SchemaVersion, definition.Name)
	}
	if len(definition.Parameters) != 1 ||
		definition.Parameters[0].Name != "MINIMUM_SEVERITY" ||
		definition.Parameters[0].Default == nil ||
		*definition.Parameters[0].Default != "medium" {
		t.Fatalf("node parameter contract = %#v", definition.Parameters)
	}
	if len(definition.Function.Inputs) != 2 ||
		definition.Function.Inputs[0].Name != "before" ||
		definition.Function.Inputs[0].Type != snapshot.TypeRef("repository/v1") ||
		definition.Function.Inputs[1].Name != "after" ||
		definition.Function.Inputs[1].Type != snapshot.TypeRef("repository/v1") ||
		len(definition.Function.Outputs) != 1 ||
		definition.Function.Outputs[0].Name != "review" ||
		definition.Function.Outputs[0].Type != snapshot.TypeRef("review/v1") ||
		definition.Function.Outputs[0].From != "review" {
		t.Fatalf("node port contract = inputs %#v outputs %#v", definition.Function.Inputs, definition.Function.Outputs)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, input := range definition.Function.Inputs {
		if _, err := registry.Lookup(input.Type); err != nil {
			t.Fatalf("node input %q has no built-in validator: %v", input.Name, err)
		}
	}
	for _, output := range definition.Function.Outputs {
		if _, err := registry.Lookup(output.Type); err != nil {
			t.Fatalf("node output %q has no built-in validator: %v", output.Name, err)
		}
	}
	if len(definition.Function.Plan) != 1 {
		t.Fatalf("node plan has %d steps, want one visible leaf", len(definition.Function.Plan))
	}
	agent, ok := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("node leaf = %T, want *atc.AgentStep", definition.Function.Plan[0].Config)
	}
	if agent.Model != "" ||
		agent.BudgetSliceUSD != 5 ||
		!strings.Contains(agent.Prompt, "Compare the immutable repositories mounted at `before` and `after`.") ||
		!strings.Contains(agent.Prompt, "The `subjects` array must be sorted lexicographically by id") ||
		!reflect.DeepEqual(agent.Skills, []string{"review"}) {
		t.Fatalf("frozen agent implementation = model %q budget %v prompt %q skills %q", agent.Model, agent.BudgetSliceUSD, agent.Prompt, agent.Skills)
	}
	if definition.Function.SkillFiles["skills/review/SKILL.md"] == "" {
		t.Fatalf("compiled skill tree = %#v", definition.Function.SkillFiles)
	}
	defaultInstance, err := definition.Instantiate(nil)
	if err != nil {
		t.Fatalf("instantiate node with declared defaults: %v", err)
	}
	if got := defaultInstance.Plan[0].Config.(*atc.AgentStep).Env["MINIMUM_SEVERITY"]; got != "medium" {
		t.Fatalf("instantiated default MINIMUM_SEVERITY = %q, want medium", got)
	}
	if len(agent.Sidecars) != 0 {
		t.Fatalf("portable code-review node has sidecars = %#v", agent.Sidecars)
	}
	if agent.Capabilities != nil || definition.Function.Capabilities != nil {
		t.Fatalf("mutable capability names escaped compilation: agent=%#v catalog=%#v", agent.Capabilities, definition.Function.Capabilities)
	}
}

func TestVersionThreeEngineeringSeedsCompileAndRender(t *testing.T) {
	tests := []struct {
		directory         string
		name              string
		inputs            []workflow.SignaturePort
		outputs           []workflow.SignaturePort
		dispositionOutput string
		humanWait         bool
		publisher         bool
	}{
		{
			directory: "seeds/code-review-v3",
			name:      "code-review",
			inputs: []workflow.SignaturePort{
				{Name: "before", Type: snapshot.TypeRef("repository/v1")},
				{Name: "after", Type: snapshot.TypeRef("repository/v1")},
			},
			outputs:           []workflow.SignaturePort{{Name: "review", Type: snapshot.TypeRef("review/v1")}},
			dispositionOutput: "review",
		},
		{
			directory: "seeds/small-fix-v3",
			name:      "small-fix",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "work-item", Type: snapshot.TypeRef("work-item/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("opaque/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			directory: "seeds/version-upgrade-v3",
			name:      "version-upgrade",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "request", Type: snapshot.TypeRef("upgrade-request/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("upgrade-report/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			// The shipped experiment evaluator. It has no agent and no publisher
			// on purpose: those are exactly the two shapes experiment admission
			// charges for (a budget slice) and refuses (an outbound effect).
			directory: "seeds/measure-review-v3",
			name:      "measure-review",
			inputs: []workflow.SignaturePort{
				{Name: "candidate", Type: snapshot.TypeRef("review/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "measurements", Type: snapshot.TypeRef("measurements/v1")},
			},
		},
		{
			directory: "seeds/merge-delivery-v3",
			name:      "merge-delivery",
			inputs: []workflow.SignaturePort{
				{Name: "base", Type: snapshot.TypeRef("repository/v1")},
				{Name: "candidate", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "target", Type: snapshot.TypeRef("repository/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "merged-change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "merge-report", Type: snapshot.TypeRef("validation/v1")},
			},
			dispositionOutput: "merged-change",
			humanWait:         true,
			publisher:         true,
		},
		{
			directory: "seeds/anonymization-audit-v3",
			name:      "anonymization-audit",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "database", Type: snapshot.TypeRef("database-snapshot/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "findings", Type: snapshot.TypeRef("audit-findings/v1")},
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1"), Optional: true},
			},
		},
		{
			directory: "seeds/log-diagnosis-v3",
			name:      "log-diagnosis",
			inputs: []workflow.SignaturePort{
				{Name: "logs", Type: snapshot.TypeRef("log-bundle/v1")},
				{Name: "deployment", Type: snapshot.TypeRef("deployment-snapshot/v1"), Optional: true},
			},
			outputs: []workflow.SignaturePort{
				{Name: "diagnosis", Type: snapshot.TypeRef("diagnosis/v1")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(test.directory)
			if err != nil {
				t.Fatalf("ManifestFromDir: %v", err)
			}
			definition, err := workflowtest.NewMemoryStore().ImportManifest(test.name, manifest, "seed-test")
			if err != nil {
				t.Fatalf("compile/import seed: %v", err)
			}
			if definition.SchemaVersion != 3 || definition.SignatureVersion != 1 {
				t.Fatalf("version identity = schema %d signature %d", definition.SchemaVersion, definition.SignatureVersion)
			}
			signature, err := definition.Compiled.PublicSignature()
			if err != nil {
				t.Fatalf("PublicSignature: %v", err)
			}
			if !reflect.DeepEqual(signature.Inputs, test.inputs) || !reflect.DeepEqual(signature.Outputs, test.outputs) {
				t.Fatalf("signature = %+v, want inputs=%+v outputs=%+v", signature, test.inputs, test.outputs)
			}
			if definition.Compiled.Function.DispositionOutput != test.dispositionOutput {
				t.Fatalf("disposition_output = %q, want %q", definition.Compiled.Function.DispositionOutput, test.dispositionOutput)
			}

			target, err := workflow.FullFunctionTarget(*definition)
			if err != nil {
				t.Fatalf("FullFunctionTarget: %v", err)
			}
			rendered, err := workflow.RenderFunction(target)
			if err != nil {
				t.Fatalf("RenderFunction: %v", err)
			}
			if len(rendered.Config.Jobs) != 1 || len(rendered.Config.Jobs[0].PlanSequence) <= len(test.inputs) {
				t.Fatalf("rendered plan omitted the authored DAG: %+v", rendered.Config.Jobs)
			}
			var waits, publishers int
			unsupported := func(kind string) error {
				return fmt.Errorf("version-3 seed rendered unsupported visible %s step", kind)
			}
			recursor := atc.StepRecursor{
				OnTask:            func(*atc.TaskStep) error { return nil },
				OnAgent:           func(*atc.AgentStep) error { return nil },
				OnLoadSnapshot:    func(*atc.LoadSnapshotStep) error { return nil },
				OnAwaitSnapshot:   func(*atc.AwaitSnapshotStep) error { waits++; return nil },
				OnPublishSnapshot: func(*atc.PublishSnapshotStep) error { publishers++; return nil },
				OnGet:             func(*atc.GetStep) error { return unsupported("get") },
				OnPut:             func(*atc.PutStep) error { return unsupported("put") },
				OnRun:             func(*atc.RunStep) error { return unsupported("run") },
				OnSetPipeline:     func(*atc.SetPipelineStep) error { return unsupported("set_pipeline") },
				OnLoadVar:         func(*atc.LoadVarStep) error { return unsupported("load_var") },
			}
			for _, step := range rendered.Config.Jobs[0].PlanSequence {
				if err := step.Config.Visit(recursor); err != nil {
					t.Fatalf("inspect rendered plan: %v", err)
				}
			}
			if (waits > 0) != test.humanWait || (publishers > 0) != test.publisher {
				t.Fatalf("visible boundaries: waits=%d publishers=%d, want wait=%t publisher=%t", waits, publishers, test.humanWait, test.publisher)
			}
			if strings.Contains(string(manifest[workflow.WorkflowFileName]), "ticket_id") || strings.Contains(string(manifest[workflow.WorkflowFileName]), "workspace") {
				t.Fatal("version-3 seed is coupled to the legacy ticket/workspace model")
			}
		})
	}
}

// TestMeasureReviewSeedStaysAdmissibleAsAnExperimentEvaluator pins the three
// properties that make this seed usable where the others are not.
//
// An experiment binds an evaluator run per cell, so the evaluator's shape
// decides what the whole matrix costs and whether it can be admitted at all:
// agent/experiment/execution_budget.go rejects a publish_snapshot outright and
// demands a positive dollar envelope the moment any agent leaf exists while the
// deployment daily cap is on. An evaluator that acquired either would silently
// turn every repetition into spend or into an external write. The third
// property — that the task really invokes the deterministic function — is what
// keeps the first two from being true only because the seed does nothing.
func TestMeasureReviewSeedStaysAdmissibleAsAnExperimentEvaluator(t *testing.T) {
	manifest, err := workflow.ManifestFromDir("seeds/measure-review-v3")
	if err != nil {
		t.Fatalf("ManifestFromDir: %v", err)
	}
	definition, err := workflowtest.NewMemoryStore().ImportManifest("measure-review", manifest, "seed-test")
	if err != nil {
		t.Fatalf("compile/import seed: %v", err)
	}
	target, err := workflow.FullFunctionTarget(*definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}

	var agents, effects int
	var tasks []*atc.TaskStep
	// An unset hook is a no-op, so every step kind that could carry an effect
	// gets an explicit handler: a silently ignored `put` would let this test
	// certify an evaluator that writes to the outside world on every repetition.
	unsupported := func(kind string) error {
		return fmt.Errorf("evaluator seed rendered unsupported visible %s step", kind)
	}
	recursor := atc.StepRecursor{
		OnTask:            func(step *atc.TaskStep) error { tasks = append(tasks, step); return nil },
		OnAgent:           func(*atc.AgentStep) error { agents++; return nil },
		OnLoadSnapshot:    func(*atc.LoadSnapshotStep) error { return nil },
		OnAwaitSnapshot:   func(*atc.AwaitSnapshotStep) error { effects++; return nil },
		OnPublishSnapshot: func(*atc.PublishSnapshotStep) error { effects++; return nil },
		OnGet:             func(*atc.GetStep) error { return unsupported("get") },
		OnPut:             func(*atc.PutStep) error { return unsupported("put") },
		OnRun:             func(*atc.RunStep) error { return unsupported("run") },
		OnSetPipeline:     func(*atc.SetPipelineStep) error { return unsupported("set_pipeline") },
		OnLoadVar:         func(*atc.LoadVarStep) error { return unsupported("load_var") },
	}
	for _, step := range rendered.Config.Jobs[0].PlanSequence {
		if err := step.Config.Visit(recursor); err != nil {
			t.Fatalf("inspect rendered plan: %v", err)
		}
	}
	if agents != 0 {
		t.Errorf("evaluator seed renders %d agent step(s); an evaluator with an agent is neither free nor reproducible", agents)
	}
	if effects != 0 {
		t.Errorf("evaluator seed renders %d human-wait/publisher step(s); experiment admission requires an effect-free evaluator", effects)
	}
	if len(tasks) != 1 {
		t.Fatalf("evaluator seed renders %d task steps, want exactly the measuring function", len(tasks))
	}

	run := tasks[0].Config.Run
	if run.Path != "function-runner" {
		t.Fatalf("evaluator task runs %q, want the deterministic function-runner", run.Path)
	}
	want := []string{"judge", "--candidate=candidate", "--output=measurements"}
	if !reflect.DeepEqual(run.Args, want) {
		t.Fatalf("evaluator task args = %q, want %q", run.Args, want)
	}
	if tasks[0].SnapshotOutputs["measurements"].Type != snapshot.TypeRef("measurements/v1") {
		t.Fatalf("evaluator task output declarations = %+v, want a typed measurements/v1 port", tasks[0].SnapshotOutputs)
	}
}

func TestGovernedSeedsRenderPrivateExactValidationRequirements(t *testing.T) {
	for directory, name := range map[string]string{"seeds/small-fix-v3": "small-fix", "seeds/version-upgrade-v3": "version-upgrade", "seeds/merge-delivery-v3": "merge-delivery"} {
		t.Run(directory, func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflowtest.NewMemoryStore().ImportManifest(name, manifest, "seed-test")
			if err != nil {
				t.Fatal(err)
			}
			if len(definition.Compiled.Function.DevValidationProfiles) == 0 || len(definition.Compiled.Function.DevValidationProfiles[0].Profile) == 0 || !strings.Contains(string(definition.Compiled.Function.DevValidationProfiles[0].Profile), "- id: tests") {
				t.Fatalf("governed seed must compile a nonempty executable validation profile: %#v", definition.Compiled.Function.DevValidationProfiles)
			}
			if directory != "seeds/merge-delivery-v3" {
				var changeFrom string
				for _, output := range definition.Compiled.Function.Outputs {
					if output.Port.Name == "change" {
						changeFrom = output.From
					}
				}
				if changeFrom != "candidate" {
					t.Fatalf("public change must be the exact validated candidate, got %q", changeFrom)
				}
				validated := false
				for index, step := range definition.Compiled.Function.Plan {
					if task, ok := step.Config.(*atc.TaskStep); ok && task.DevValidationAuthority != nil && task.DevValidationAuthority.CandidateInput == "candidate" {
						validated = true
					}
					if !validated {
						continue
					}
					if err := step.Config.Visit(atc.StepRecursor{OnAgent: func(agent *atc.AgentStep) error {
						for _, declaration := range agent.SnapshotOutputs {
							if declaration.Type == snapshot.TypeRef("repository-change/v1") {
								return fmt.Errorf("plan step %d has a repository-change producer after validation", index)
							}
						}
						return nil
					}}); err != nil {
						t.Fatal(err)
					}
				}
				if !validated {
					t.Fatal("candidate validation task was not found")
				}
			}
			target, err := workflow.FullFunctionTarget(*definition)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := workflow.RenderFunction(target)
			if err != nil {
				t.Fatal(err)
			}
			var review, await, publish int
			for _, step := range rendered.Config.Jobs[0].PlanSequence {
				if err := step.Config.Visit(atc.StepRecursor{OnAgent: func(s *atc.AgentStep) error {
					if s.ReviewValidation != nil {
						review++
					}
					return nil
				}, OnAwaitSnapshot: func(s *atc.AwaitSnapshotStep) error {
					if s.MergeApprovalValidation != nil {
						await++
					}
					return nil
				}, OnPublishSnapshot: func(s *atc.PublishSnapshotStep) error {
					if s.PublishValidation != nil {
						publish++
					}
					return nil
				}}); err != nil {
					t.Fatal(err)
				}
			}
			if directory == "seeds/merge-delivery-v3" {
				if await != 1 || publish != 1 {
					t.Fatalf("merge requirements = await %d publish %d", await, publish)
				}
			} else if review != 1 {
				t.Fatalf("review requirements = %d", review)
			}
		})
	}
}
