package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestFullFunctionTargetAndRenderFunction(t *testing.T) {
	definition := renderTestDefinition()
	original := definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep)

	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	if target.Kind != TargetWorkflow || target.FunctionID != "" {
		t.Fatalf("target identity = (%q, %q), want workflow with no function ID", target.Kind, target.FunctionID)
	}
	if original.SnapshotOutputs["review"].Retention != "" {
		t.Fatal("target construction mutated the definition")
	}
	annotated := target.Function.Plan[0].Config.(*atc.AgentStep).SnapshotOutputs["review"]
	if annotated.Retention != snapshot.RetentionClassWorkflow || annotated.WorkflowPort != "review" || annotated.WorkflowDefinitionID != 41 || annotated.WorkflowRunID != "((workflow_run_id))" {
		t.Fatalf("target output annotation = %+v", annotated)
	}

	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
	wantParams := []atc.ParamSchema{
		{Name: "workflow_run_id", Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true},
		{Name: "snapshot_repo", Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true},
		{Name: "snapshot_notes", Type: "string", Format: atc.ParamFormatZeroOrPositiveDecimalInt64, Default: "0"},
	}
	if !reflect.DeepEqual(rendered.Config.Params, wantParams) {
		t.Fatalf("params = %#v, want %#v", rendered.Config.Params, wantParams)
	}
	if !reflect.DeepEqual(rendered.InputParamNames, map[string]string{"repo": "snapshot_repo", "notes": "snapshot_notes"}) {
		t.Fatalf("input param names = %#v", rendered.InputParamNames)
	}
	if !rendered.Config.Template || len(rendered.Config.Jobs) != 1 || rendered.Config.Jobs[0].Name != "run" {
		t.Fatalf("rendered jobs = %#v", rendered.Config.Jobs)
	}
	if got := rendered.Config.EntryJobs(); !reflect.DeepEqual(got, []string{"run"}) {
		t.Fatalf("entry jobs = %#v", got)
	}
	plan := rendered.Config.Jobs[0].PlanSequence
	if len(plan) != 3 {
		t.Fatalf("plan length = %d, want two loads plus authored node", len(plan))
	}
	wantLoads := []*atc.LoadSnapshotStep{
		{Name: "repo", ID: "((snapshot_repo))", Type: repositoryV1, WorkflowRunID: "((workflow_run_id))"},
		{Name: "notes", ID: "((snapshot_notes))", Type: reviewV1, Optional: true, WorkflowRunID: "((workflow_run_id))"},
	}
	for index, want := range wantLoads {
		got, ok := plan[index].Config.(*atc.LoadSnapshotStep)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("load %d = %#v, want %#v", index, plan[index].Config, want)
		}
	}
	gotAgent, ok := plan[2].Config.(*atc.AgentStep)
	if !ok || gotAgent.FunctionID != "review-agent" || gotAgent.Prompt != "review literally" {
		t.Fatalf("authored DAG was not preserved: %#v", plan[2].Config)
	}
	if gotAgent.SnapshotOutputs["review"].WorkflowDefinitionID != 41 {
		t.Fatalf("public annotation was not preserved: %+v", gotAgent.SnapshotOutputs["review"])
	}

	canonical, err := rendered.Config.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	hash := sha256.New()
	hash.Write([]byte("workflow-target-config/v1\x00"))
	hash.Write(canonical)
	wantHash := hex.EncodeToString(hash.Sum(nil))
	if rendered.TargetConfigHash != wantHash {
		t.Fatalf("hash = %q, want %q", rendered.TargetConfigHash, wantHash)
	}
	if rendered.TargetConfigHash != "5a48c3d7635350ff8a8d371a9d69b611627222e7914baaf4f54ce5c7f26afb30" {
		t.Fatalf("canonical renderer hash vector changed: %s", rendered.TargetConfigHash)
	}
	wantName := "agent-workflow-review-flow-v7-" + wantHash[:12]
	if rendered.TemplateName != wantName {
		t.Fatalf("template name = %q, want %q", rendered.TemplateName, wantName)
	}
	if !target.Signature.Equal(rendered.TargetSignature) {
		t.Fatalf("signature changed: target=%+v rendered=%+v", target.Signature, rendered.TargetSignature)
	}

	// Every returned layer is isolated from both the durable definition and the
	// target value supplied by the caller.
	gotAgent.Prompt = "mutated"
	rendered.InputParamNames["repo"] = "mutated"
	if target.Function.Plan[0].Config.(*atc.AgentStep).Prompt != "review literally" {
		t.Fatal("rendered config aliases target plan")
	}
	if definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Prompt != "review literally" {
		t.Fatal("rendered config aliases definition plan")
	}
}

func TestRenderFunctionRejectsUnsafeOrMutableInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
		want   string
	}{
		{name: "missing durable ID", mutate: func(def *Definition) { def.ID = 0 }, want: "definition ID"},
		{name: "missing durable version", mutate: func(def *Definition) { def.Version = 0 }, want: "version"},
		{name: "legacy schema", mutate: func(def *Definition) { def.SchemaVersion = 2 }, want: "schema_version 3"},
		{name: "unsafe workflow name", mutate: func(def *Definition) { def.Name = "Bad Name" }, want: "identifier"},
		{name: "selected agent skills", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Skills = []string{"review"}
		}, want: "skills"},
		{name: "reserved token injection", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Prompt = "leak ((workflow_run_id))"
		}, want: "reserved"},
		{name: "derived token injection", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Prompt = "leak ((snapshot_repo))"
		}, want: "reserved"},
		{name: "reserved token map key injection", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Env = atc.TaskEnv{"((workflow_run_id))": "leak"}
		}, want: "reserved"},
		{name: "unexpanded capabilities", mutate: func(def *Definition) {
			def.Compiled.Function.Capabilities = map[string]Capability{"dev": {
				Contract: "dev-mcp/v1",
				Sidecar:  atc.SidecarConfig{Name: "dev-mcp", Image: exactDigestImage("d")},
			}}
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Capabilities = []string{"dev"}
		}, want: "capabilities"},
		{name: "mutable literal agent sidecar", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Sidecars = []atc.SidecarSource{{
				Config: &atc.SidecarConfig{Name: "dev-mcp", Image: "registry.example/dev-mcp:latest"},
			}}
		}, want: "literal exact digest"},
		{name: "unsafe function ID", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).FunctionID = "Bad ID"
		}, want: "identifier"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := renderTestDefinition()
			test.mutate(&definition)
			_, err := FullFunctionTarget(definition)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestFullFunctionTargetRendersExactFrozenAgentSkills(t *testing.T) {
	definition := renderTestDefinition()
	agent := definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep)
	agent.Skills = []string{"review"}
	agent.SkillFiles = map[string]string{
		"skills/review/SKILL.md":  "review instructions",
		"skills/review/refs/a.md": "reference",
	}
	definition.Compiled.Function.SkillFiles = map[string]string{
		"skills/review/SKILL.md":  "review instructions",
		"skills/review/refs/a.md": "reference",
	}

	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
	got := rendered.Config.Jobs[0].PlanSequence[len(rendered.Config.Jobs[0].PlanSequence)-1].Config.(*atc.AgentStep)
	if !reflect.DeepEqual(got.SkillFiles, agent.SkillFiles) {
		t.Fatalf("rendered skill files = %#v, want %#v", got.SkillFiles, agent.SkillFiles)
	}
	hasSkillsInput := false
	for _, name := range got.Inputs {
		hasSkillsInput = hasSkillsInput || name == "skills"
	}
	if got.InputMapping["skills"] != "" || hasSkillsInput {
		t.Fatalf("frozen skills acquired a logical input: inputs=%v mapping=%v", got.Inputs, got.InputMapping)
	}
}

func TestFullFunctionTargetRejectsFrozenSkillOutputCollision(t *testing.T) {
	definition := renderTestDefinition()
	agent := definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep)
	agent.Skills = []string{"review"}
	agent.SkillFiles = map[string]string{"skills/review/SKILL.md": "review"}
	agent.Outputs = append(agent.Outputs, "skills")
	agent.SnapshotOutputs["skills"] = atc.SnapshotOutputConfig{Type: reviewV1}
	definition.Compiled.Function.SkillFiles = map[string]string{"skills/review/SKILL.md": "review"}

	if _, err := FullFunctionTarget(definition); err == nil || !strings.Contains(err.Error(), "input or output") {
		t.Fatalf("FullFunctionTarget output collision = %v, want reserved skills output refusal", err)
	}
}

func TestFullFunctionTargetRejectsUnfrozenExecutionDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
		want   string
	}{
		{name: "file-backed task config", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Config = nil
			task.ConfigPath = "ci/task.yml"
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "file-backed"},
		{name: "task image artifact", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.ImageArtifactName = "built-image"
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "image artifact"},
		{name: "task image without explicit version", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Config.ImageResource.Version = nil
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "explicit immutable version"},
		{name: "task image through mutable custom resource type check", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Config.ImageResource.Type = "custom-task-image"
			def.Compiled.Function.ResourceTypes = append(def.Compiled.Function.ResourceTypes, atc.ResourceType{
				Name: "custom-task-image", Type: "registry-image", Source: atc.Source{"repository": "example/task-type"},
			})
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "mutable check chain"},
		{name: "task sidecar file", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Sidecars = []atc.SidecarSource{{File: "repo/sidecars.yml"}}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "sidecar"},
		{name: "task sidecar image artifact", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Sidecars = []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "database", ImageArtifact: "database-image"}}}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "image_artifact"},
		{name: "task sidecar mutable image", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Sidecars = []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "database", Image: "example/database:latest"}}}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "exact digest"},
		{name: "runtime task vars", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Vars = atc.Params{"branch": "main"}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "task vars"},
		{name: "task interpolation", mutate: func(def *Definition) {
			task := renderImmutableTask()
			task.Params = atc.TaskEnv{"BRANCH": "((runtime_branch))"}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, def.Compiled.Function.Plan...)
		}, want: "interpolation"},
		{name: "agent interpolation", mutate: func(def *Definition) {
			def.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Prompt = "review ((runtime_policy))"
		}, want: "interpolation"},
		{name: "dynamic across values", mutate: func(def *Definition) {
			across := &atc.AcrossStep{
				Vars: []atc.AcrossVarConfig{{Var: "item", Values: "((runtime_matrix))"}},
				Step: &atc.AgentStep{Name: "matrix", Prompt: "work"},
			}
			def.Compiled.Function.Plan = append([]atc.Step{{Config: across}}, def.Compiled.Function.Plan...)
		}, want: "across"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := renderTestDefinition()
			test.mutate(&definition)

			_, err := FullFunctionTarget(definition)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestFullFunctionTargetPreservesProvableTasksWithoutLiveResources(t *testing.T) {
	definition := renderTestDefinition()
	task := renderImmutableTask()
	definition.Compiled.Function.Plan = append([]atc.Step{{Config: task}}, definition.Compiled.Function.Plan...)

	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
	if len(rendered.Config.Resources) != 0 {
		t.Fatalf("resources = %#v, want no live-read resources", rendered.Config.Resources)
	}
	taskFound := false
	for _, step := range rendered.Config.Jobs[0].PlanSequence {
		if reflect.TypeOf(step.Config) == reflect.TypeOf(&atc.TaskStep{}) {
			taskFound = true
			break
		}
	}
	if !taskFound {
		t.Fatal("task was not preserved")
	}
}

func TestFunctionTargetsDiscardExpandedCapabilityCatalogAndRetainLiteralSidecars(t *testing.T) {
	definition := renderTestDefinition()
	sidecar := atc.SidecarConfig{Name: "dev-mcp", Image: exactDigestImage("d")}
	definition.Compiled.Function.Capabilities = map[string]Capability{
		"dev": {Contract: "dev-mcp/v1", Sidecar: sidecar},
	}
	definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Sidecars = []atc.SidecarSource{{Config: &sidecar}}

	full, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	if len(full.Function.Capabilities) != 0 {
		t.Fatalf("full target retained author-only capability catalog: %#v", full.Function.Capabilities)
	}
	rendered, err := RenderFunction(full)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
	agent := rendered.Config.Jobs[0].PlanSequence[len(rendered.Config.Jobs[0].PlanSequence)-1].Config.(*atc.AgentStep)
	if len(agent.Sidecars) != 1 || agent.Sidecars[0].Config == nil || agent.Sidecars[0].Config.Image != exactDigestImage("d") {
		t.Fatalf("rendered sidecars = %#v", agent.Sidecars)
	}

	extracted, err := ExtractFunctionTarget(definition, "review-agent")
	if err != nil {
		t.Fatalf("ExtractFunctionTarget: %v", err)
	}
	if len(extracted.Function.Capabilities) != 0 {
		t.Fatalf("extracted target retained author-only capability catalog: %#v", extracted.Function.Capabilities)
	}
}

func TestFullFunctionTargetRejectsAuthoredLoadSnapshotSteps(t *testing.T) {
	definition := renderTestDefinition()
	definition.Compiled.Function.Plan = append([]atc.Step{{Config: &atc.LoadSnapshotStep{
		Name:          "repo",
		ID:            "1",
		Type:          repositoryV1,
		WorkflowRunID: "1",
	}}}, definition.Compiled.Function.Plan...)

	_, err := FullFunctionTarget(definition)
	if err == nil || !strings.Contains(err.Error(), "authored load_snapshot") {
		t.Fatalf("error = %v, want authored load_snapshot refusal", err)
	}
}

func TestFullFunctionTargetRejectsReservedTokenInSpoofedOutputTypesMap(t *testing.T) {
	definition := renderTestDefinition()
	spoof := &atc.TaskStep{
		Name:       "spoof",
		FunctionID: "spoof",
		Config: &atc.TaskConfig{
			Platform: "linux",
			Run:      atc.TaskRunConfig{Path: "/bin/true"},
		},
		Vars: atc.Params{
			"output_types": map[string]any{
				"not-a-real-output": map[string]any{
					"workflow_run_id": "((workflow_run_id))",
				},
			},
		},
	}
	definition.Compiled.Function.Plan = append([]atc.Step{{Config: spoof}}, definition.Compiled.Function.Plan...)

	_, err := FullFunctionTarget(definition)
	if err == nil || !strings.Contains(err.Error(), "reserved renderer token") {
		t.Fatalf("error = %v, want reserved renderer token refusal", err)
	}
}

func TestRenderFunctionRejectsForgedPrivateWorkflowOutputClaim(t *testing.T) {
	definition := renderTestDefinition()
	agent := definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep)
	agent.Outputs = append(agent.Outputs, "private")
	agent.SnapshotOutputs["private"] = atc.SnapshotOutputConfig{Type: reviewV1}
	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	targetAgent := target.Function.Plan[0].Config.(*atc.AgentStep)
	targetAgent.SnapshotOutputs["private"] = atc.SnapshotOutputConfig{
		Type: reviewV1, Retention: snapshot.RetentionClassWorkflow,
		WorkflowPort: "review", WorkflowDefinitionID: definition.ID,
		WorkflowRunID: "((workflow_run_id))",
	}

	_, err = RenderFunction(target)
	if err == nil || !strings.Contains(err.Error(), "workflow output linkage") {
		t.Fatalf("error = %v, want forged workflow output linkage refusal", err)
	}
}

// TestRenderFunctionRejectsInputPortCollidingWithFunctionID pins a contract
// that otherwise holds only by construction: the renderer prepends one
// synthetic load_snapshot step per public input port (and, once sources are
// bound, per resource source) ahead of the authored plan, which is what
// joins input-port and resource-source names to the same workflow-local node
// identity namespace as authored function_ids. Nothing in TypeCheckFunction
// itself enforces that uniformity — a function whose declared Inputs collide
// with a plan node's function_id passes compile-time TypeCheckFunction
// (function.Inputs are bound straight into the environment, never through
// registerNodeIdentity) and is only caught here, once rendering has
// substituted the synthetic load for the declared input.
func TestRenderFunctionRejectsInputPortCollidingWithFunctionID(t *testing.T) {
	definition := renderTestDefinition()
	definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).FunctionID = "repo"

	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}

	_, err = RenderFunction(target)
	if err == nil || !strings.Contains(err.Error(), `duplicate function_id "repo"`) {
		t.Fatalf("error = %v, want a duplicate node identity between the repo input port and the repo function_id", err)
	}
}

func TestRenderFunctionIsDeterministicAndConcurrent(t *testing.T) {
	definition := renderTestDefinition()
	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	want, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}

	const calls = 16
	results := make(chan RenderedFunction, calls)
	errors := make(chan error, calls)
	var group sync.WaitGroup
	for range calls {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := RenderFunction(target)
			results <- result
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent RenderFunction: %v", err)
		}
	}
	for got := range results {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nondeterministic render:\n got %#v\nwant %#v", got, want)
		}
	}
	if definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).SnapshotOutputs["review"].Retention != "" {
		t.Fatal("concurrent rendering mutated the definition")
	}
}

func TestRenderFunctionHashAndNameSensitivity(t *testing.T) {
	render := func(definition Definition) RenderedFunction {
		t.Helper()
		target, err := FullFunctionTarget(definition)
		if err != nil {
			t.Fatalf("FullFunctionTarget: %v", err)
		}
		result, err := RenderFunction(target)
		if err != nil {
			t.Fatalf("RenderFunction: %v", err)
		}
		return result
	}

	baseDefinition := renderTestDefinition()
	base := render(baseDefinition)

	newVersionDefinition := renderTestDefinition()
	newVersionDefinition.Version++
	newVersion := render(newVersionDefinition)
	if newVersion.TargetConfigHash != base.TargetConfigHash {
		t.Fatal("durable definition version leaked into target config hash")
	}
	if newVersion.TemplateName == base.TemplateName || !strings.Contains(newVersion.TemplateName, "-v8-") {
		t.Fatalf("definition version did not change template name: %q vs %q", newVersion.TemplateName, base.TemplateName)
	}

	changedDefinition := renderTestDefinition()
	changedDefinition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Prompt = "changed literal"
	changed := render(changedDefinition)
	if changed.TargetConfigHash == base.TargetConfigHash || changed.TemplateName == base.TemplateName {
		t.Fatal("executable config change did not change hash and name")
	}
}

func TestRenderFunctionPureDurableResumeHelpers(t *testing.T) {
	definition := renderTestDefinition()
	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}

	hash, err := TargetConfigHash(rendered.Config)
	if err != nil {
		t.Fatalf("TargetConfigHash: %v", err)
	}
	if hash != rendered.TargetConfigHash {
		t.Fatalf("hash = %q, want %q", hash, rendered.TargetConfigHash)
	}

	inputParam, err := InputParamName("repo")
	if err != nil {
		t.Fatalf("InputParamName: %v", err)
	}
	if inputParam != rendered.InputParamNames["repo"] {
		t.Fatalf("input param = %q, want %q", inputParam, rendered.InputParamNames["repo"])
	}

	templateName, err := TemplateName(TargetWorkflow, definition.Name, definition.Version, "", hash)
	if err != nil {
		t.Fatalf("TemplateName: %v", err)
	}
	if templateName != rendered.TemplateName {
		t.Fatalf("template name = %q, want %q", templateName, rendered.TemplateName)
	}

	extracted, err := ExtractFunctionTarget(definition, "review-agent")
	if err != nil {
		t.Fatalf("ExtractFunctionTarget: %v", err)
	}
	extractedRendered, err := RenderFunction(extracted)
	if err != nil {
		t.Fatalf("RenderFunction(extracted): %v", err)
	}
	extractedName, err := TemplateName(TargetFunction, definition.Name, definition.Version, "review-agent", extractedRendered.TargetConfigHash)
	if err != nil {
		t.Fatalf("TemplateName(extracted): %v", err)
	}
	if extractedName != extractedRendered.TemplateName {
		t.Fatalf("extracted template name = %q, want %q", extractedName, extractedRendered.TemplateName)
	}
}

func TestRenderFunctionPureDurableResumeHelpersRejectInvalidInputs(t *testing.T) {
	validHash := strings.Repeat("a", sha256.Size*2)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{name: "unsafe port", call: func() error { _, err := InputParamName("Bad Port"); return err }, want: "identifier"},
		{name: "unknown target kind", call: func() error { _, err := TemplateName("future", "flow", 1, "", validHash); return err }, want: "kind"},
		{name: "workflow function ID", call: func() error { _, err := TemplateName(TargetWorkflow, "flow", 1, "node", validHash); return err }, want: "function ID"},
		{name: "missing function ID", call: func() error { _, err := TemplateName(TargetFunction, "flow", 1, "", validHash); return err }, want: "function ID"},
		{name: "zero version", call: func() error { _, err := TemplateName(TargetWorkflow, "flow", 0, "", validHash); return err }, want: "version"},
		{name: "short hash", call: func() error { _, err := TemplateName(TargetWorkflow, "flow", 1, "", "abc"); return err }, want: "hash"},
		{name: "upper-case hash", call: func() error {
			_, err := TemplateName(TargetWorkflow, "flow", 1, "", strings.Repeat("A", sha256.Size*2))
			return err
		}, want: "hash"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRenderFunctionRejectsMalformedTargetUnion(t *testing.T) {
	definition := renderTestDefinition()
	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FunctionTarget)
		want   string
	}{
		{name: "unknown kind", mutate: func(target *FunctionTarget) { target.Kind = "future" }, want: "kind"},
		{name: "workflow with function ID", mutate: func(target *FunctionTarget) { target.FunctionID = "review-agent" }, want: "must not carry"},
		{name: "zero definition ID", mutate: func(target *FunctionTarget) { target.WorkflowDefinitionID = 0 }, want: "definition ID"},
		{name: "zero version", mutate: func(target *FunctionTarget) { target.WorkflowVersion = 0 }, want: "version"},
		{name: "mismatched signature", mutate: func(target *FunctionTarget) { target.Signature.Inputs[0].Optional = true }, want: "signature"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := target
			copy.Signature = clonePublicSignature(target.Signature)
			test.mutate(&copy)
			_, err := RenderFunction(copy)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func renderTestDefinition() Definition {
	agent := &atc.AgentStep{
		Name:       "review",
		FunctionID: "review-agent",
		Prompt:     "review literally",
		Inputs:     []string{"repo", "notes"},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"repo":  {Type: repositoryV1},
			"notes": {Type: reviewV1, Optional: true},
		},
		Outputs: []string{"review"},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"review": {Type: reviewV1},
		},
	}
	function := &FunctionConfig{
		SignatureVersion: 3,
		Inputs: []snapshot.Port{
			{Name: "repo", Type: repositoryV1},
			{Name: "notes", Type: reviewV1, Optional: true},
		},
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: "review", Type: reviewV1},
			From: "review",
		}},
		Plan: []atc.Step{{Config: agent}},
	}
	return Definition{
		ID:               41,
		Name:             "review-flow",
		Version:          7,
		SchemaVersion:    3,
		SignatureVersion: 3,
		Compiled: CompiledDefinition{
			SchemaVersion: 3,
			Name:          "review-flow",
			Function:      function,
		},
	}
}

func renderImmutableTask() *atc.TaskStep {
	return &atc.TaskStep{
		Name:       "compile",
		FunctionID: "compile",
		Config: &atc.TaskConfig{
			Platform: "linux",
			ImageResource: &atc.ImageResource{
				Type:    "registry-image",
				Source:  atc.Source{"repository": "example/task"},
				Version: atc.Version{"digest": "sha256:immutable"},
			},
			Run: atc.TaskRunConfig{Path: "/bin/true"},
		},
	}
}
