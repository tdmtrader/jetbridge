package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestExtractFunctionTargetSelectsDirectLeafWithLexicalSignature(t *testing.T) {
	selected := &atc.AgentStep{
		Name:       "display-name-is-not-identity",
		FunctionID: "chosen",
		Prompt:     "literal prompt",
		Inputs:     []string{"zeta", "alpha"},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"zeta":  {Type: reviewV1, Optional: true},
			"alpha": {Type: repositoryV1},
		},
		Outputs: []string{"z-output", "a-output"},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"z-output": {Type: reviewV1, Optional: true},
			"a-output": {Type: repositoryV1},
		},
		Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{
			Name:  "review-api",
			Image: exactDigestImage("1"),
		}}},
	}
	definition := extractTestDefinition([]atc.Step{
		{Config: &atc.AgentStep{Name: "chosen", FunctionID: "other", Prompt: "ignore me"}},
		{Config: &atc.AgentStep{Name: "unrelated", FunctionID: "unrelated", Prompt: "ignore me too"}},
		{Config: selected},
	}, []snapshot.Port{
		{Name: "zeta", Type: reviewV1, Optional: true},
		{Name: "alpha", Type: repositoryV1},
	})

	target, err := ExtractFunctionTarget(definition, "chosen")
	if err != nil {
		t.Fatalf("ExtractFunctionTarget: %v", err)
	}
	if target.Kind != TargetFunction || target.FunctionID != "chosen" || len(target.Function.Plan) != 1 {
		t.Fatalf("target identity/plan = kind %q id %q plan %d", target.Kind, target.FunctionID, len(target.Function.Plan))
	}
	wantSignature := PublicSignature{
		Inputs: []SignaturePort{
			{Name: "alpha", Type: repositoryV1},
			{Name: "zeta", Type: reviewV1, Optional: true},
		},
		Outputs: []SignaturePort{
			{Name: "a-output", Type: repositoryV1},
			{Name: "z-output", Type: reviewV1, Optional: true},
		},
	}
	if !reflect.DeepEqual(target.Signature, wantSignature) {
		t.Fatalf("signature = %#v, want %#v", target.Signature, wantSignature)
	}
	if len(target.Function.Resources) != 0 || len(target.Function.ResourceTypes) != 0 || len(target.Function.Prototypes) != 0 || len(target.Function.VarSources) != 0 {
		t.Fatalf("unrelated declarations leaked into extraction: %+v", target.Function)
	}
	extracted := target.Function.Plan[0].Config.(*atc.AgentStep)
	for name, output := range extracted.SnapshotOutputs {
		if output.Retention != snapshot.RetentionClassWorkflow || output.WorkflowPort != name || output.WorkflowDefinitionID != definition.ID || output.WorkflowRunID != "((workflow_run_id))" {
			t.Fatalf("output %q annotation = %+v", name, output)
		}
	}
	if selected.SnapshotOutputs["a-output"].Retention != "" {
		t.Fatal("extraction mutated the full definition")
	}

	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction(extracted): %v", err)
	}
	if !strings.HasPrefix(rendered.TemplateName, "agent-function-extract-flow-v9-chosen-") {
		t.Fatalf("template name = %q", rendered.TemplateName)
	}
	if got := rendered.Config.Params[1].Name; got != "snapshot_alpha" {
		t.Fatalf("first extracted input param = %q, want lexical alpha", got)
	}

	extracted.Prompt = "mutated"
	extracted.Sidecars[0].Config.Image = exactDigestImage("f")
	if selected.Prompt != "literal prompt" || selected.Sidecars[0].Config.Image != exactDigestImage("1") {
		t.Fatal("extracted leaf aliases the full definition")
	}
}

func TestExtractFunctionTargetAcceptsSelfContainedInlineTask(t *testing.T) {
	task := &atc.TaskStep{
		Name:       "compile",
		FunctionID: "compile",
		Config: &atc.TaskConfig{
			Platform: "linux",
			Run:      atc.TaskRunConfig{Path: "/bin/true"},
			Inputs:   []atc.TaskInputConfig{{Name: "logical-input"}},
			Outputs:  []atc.TaskOutputConfig{{Name: "logical-output"}},
			ImageResource: &atc.ImageResource{
				Type: "custom-task-image",
				Source: atc.Source{
					"repository": "example/task",
				},
				Version: atc.Version{"digest": "sha256:immutable"},
			},
		},
		InputMapping:  map[string]string{"logical-input": "repo"},
		OutputMapping: map[string]string{"logical-output": "result"},
		SnapshotInputs: map[string]atc.SnapshotInputConfig{
			"repo": {Type: repositoryV1},
		},
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"result": {Type: reviewV1},
		},
	}
	definition := extractTestDefinition([]atc.Step{{Config: task}}, []snapshot.Port{{Name: "repo", Type: repositoryV1}})
	definition.Compiled.Function.ResourceTypes = atc.ResourceTypes{{Name: "custom-task-image", Image: exactDigestImage("3")}}
	target, err := ExtractFunctionTarget(definition, "compile")
	if err != nil {
		t.Fatalf("ExtractFunctionTarget: %v", err)
	}
	if got := target.Signature.Inputs[0].Name; got != "repo" {
		t.Fatalf("mapped input = %q, want repo", got)
	}
	if got := target.Signature.Outputs[0].Name; got != "result" {
		t.Fatalf("mapped output = %q, want result", got)
	}
	if !reflect.DeepEqual(target.Function.ResourceTypes, definition.Compiled.Function.ResourceTypes) {
		t.Fatalf("minimal immutable image closure = %#v, want %#v", target.Function.ResourceTypes, definition.Compiled.Function.ResourceTypes)
	}
	if _, err := RenderFunction(target); err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
}

func TestExtractFunctionTargetRetainsOnlySelectedAgentFrozenSkills(t *testing.T) {
	selected := &atc.AgentStep{
		Name: "selected", FunctionID: "selected", Prompt: "work", Skills: []string{"review"},
		SkillFiles: map[string]string{"skills/review/SKILL.md": "review"},
	}
	other := &atc.AgentStep{
		Name: "other", FunctionID: "other", Prompt: "work", Skills: []string{"testing"},
		SkillFiles: map[string]string{"skills/testing/SKILL.md": "testing"},
	}
	definition := extractTestDefinition([]atc.Step{{Config: selected}, {Config: other}}, nil)
	definition.Compiled.Function.SkillFiles = map[string]string{
		"skills/review/SKILL.md":  "review",
		"skills/testing/SKILL.md": "testing",
	}

	target, err := ExtractFunctionTarget(definition, "selected")
	if err != nil {
		t.Fatalf("ExtractFunctionTarget: %v", err)
	}
	want := map[string]string{"skills/review/SKILL.md": "review"}
	if !reflect.DeepEqual(target.Function.SkillFiles, want) {
		t.Fatalf("extracted skill files = %#v, want %#v", target.Function.SkillFiles, want)
	}
	if got := target.Function.Plan[0].Config.(*atc.AgentStep).SkillFiles; !reflect.DeepEqual(got, want) {
		t.Fatalf("extracted agent skill files = %#v, want %#v", got, want)
	}
}

func TestExtractFunctionTargetRejectsNestedMatches(t *testing.T) {
	selected := func() atc.Step {
		return atc.Step{Config: &atc.AgentStep{Name: "selected", FunctionID: "selected", Prompt: "work"}}
	}
	plain := func(id string) atc.Step {
		return atc.Step{Config: &atc.AgentStep{Name: id, FunctionID: id, Prompt: "work"}}
	}
	tests := []struct {
		name string
		step atc.Step
	}{
		{name: "do", step: atc.Step{Config: &atc.DoStep{Steps: []atc.Step{selected()}}}},
		{name: "parallel", step: atc.Step{Config: &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{selected()}}}}},
		{name: "try", step: atc.Step{Config: &atc.TryStep{Step: selected()}}},
		{name: "retry", step: atc.Step{Config: &atc.RetryStep{Attempts: 2, Step: selected().Config}}},
		{name: "timeout", step: atc.Step{Config: &atc.TimeoutStep{Duration: "1m", Step: selected().Config}}},
		{name: "on success", step: atc.Step{Config: &atc.OnSuccessStep{Step: selected().Config, Hook: plain("hook")}}},
		{name: "on failure", step: atc.Step{Config: &atc.OnFailureStep{Step: selected().Config, Hook: plain("hook")}}},
		{name: "on error", step: atc.Step{Config: &atc.OnErrorStep{Step: selected().Config, Hook: plain("hook")}}},
		{name: "on abort", step: atc.Step{Config: &atc.OnAbortStep{Step: selected().Config, Hook: plain("hook")}}},
		{name: "ensure", step: atc.Step{Config: &atc.EnsureStep{Step: selected().Config, Hook: plain("hook")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := extractTestDefinition([]atc.Step{test.step}, nil)
			_, err := ExtractFunctionTarget(definition, "selected")
			if err == nil || !strings.Contains(err.Error(), "direct top-level") {
				t.Fatalf("error = %v, want direct top-level refusal", err)
			}
		})
	}
}

func TestExtractFunctionTargetRejectsAcrossBeforeExtraction(t *testing.T) {
	selected := atc.Step{Config: &atc.AgentStep{Name: "selected", FunctionID: "selected", Prompt: "work"}}
	definition := extractTestDefinition([]atc.Step{{Config: &atc.AcrossStep{
		Vars: []atc.AcrossVarConfig{{Var: "item", Values: []string{"one"}}}, Step: selected.Config,
	}}}, nil)
	_, err := ExtractFunctionTarget(definition, "selected")
	if err == nil || !strings.Contains(err.Error(), "across") || !strings.Contains(err.Error(), "exact execution provenance") {
		t.Fatalf("error = %v, want immutable-provenance refusal", err)
	}
}

func TestExtractFunctionTargetRejectsHiddenOrMutableDependencies(t *testing.T) {
	validAgent := func() *atc.AgentStep {
		return &atc.AgentStep{Name: "selected", FunctionID: "selected", Prompt: "literal"}
	}
	validTask := func() *atc.TaskStep {
		return &atc.TaskStep{
			Name:       "selected",
			FunctionID: "selected",
			Config:     &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "/bin/true"}},
		}
	}
	tests := []struct {
		name string
		step func() atc.Step
		want string
	}{
		{name: "file task", step: func() atc.Step {
			task := validTask()
			task.Config = nil
			task.ConfigPath = "task.yml"
			return atc.Step{Config: task}
		}, want: "file"},
		{name: "image artifact", step: func() atc.Step { task := validTask(); task.ImageArtifactName = "image"; return atc.Step{Config: task} }, want: "image artifact"},
		{name: "task sidecar file", step: func() atc.Step {
			task := validTask()
			task.Sidecars = []atc.SidecarSource{{File: "sidecar.yml"}}
			return atc.Step{Config: task}
		}, want: "sidecar"},
		{name: "agent sidecar image artifact", step: func() atc.Step {
			agent := validAgent()
			agent.Sidecars = []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "api", ImageArtifact: "image"}}}
			return atc.Step{Config: agent}
		}, want: "image_artifact"},
		{name: "mutable sidecar image", step: func() atc.Step {
			agent := validAgent()
			agent.Sidecars = []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "api", Image: "example/api:latest"}}}
			return atc.Step{Config: agent}
		}, want: "digest"},
		{name: "agent skills", step: func() atc.Step {
			agent := validAgent()
			agent.Skills = []string{"skill"}
			return atc.Step{Config: agent}
		}, want: "skills"},
		{name: "leaf timeout modifier", step: func() atc.Step {
			agent := validAgent()
			agent.Timeout = "1m"
			return atc.Step{Config: agent}
		}, want: "timeout"},
		{name: "unresolved literal token", step: func() atc.Step {
			agent := validAgent()
			agent.Prompt = "use ((runtime_value))"
			return atc.Step{Config: agent}
		}, want: "interpolation"},
		{name: "unresolved interpolation map key", step: func() atc.Step {
			agent := validAgent()
			agent.Env = atc.TaskEnv{"((runtime_key))": "value"}
			return atc.Step{Config: agent}
		}, want: "interpolation"},
		{name: "task cache", step: func() atc.Step {
			task := validTask()
			task.Config.Caches = []atc.TaskCacheConfig{{Path: "/tmp/cache"}}
			return atc.Step{Config: task}
		}, want: "cache"},
		{name: "task rootfs uri", step: func() atc.Step {
			task := validTask()
			task.Config.RootfsURI = "docker:///mutable:latest"
			return atc.Step{Config: task}
		}, want: "rootfs_uri"},
		{name: "missing task image", step: func() atc.Step {
			return atc.Step{Config: validTask()}
		}, want: "image_resource"},
		{name: "unpinned task image", step: func() atc.Step {
			task := validTask()
			task.Config.ImageResource = &atc.ImageResource{Type: "registry-image", Source: atc.Source{"repository": "example/task"}}
			return atc.Step{Config: task}
		}, want: "version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := extractTestDefinition([]atc.Step{test.step()}, nil)
			_, err := ExtractFunctionTarget(definition, "selected")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestExtractFunctionTargetRejectsInterpolationInRetainedResourceType(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*atc.ResourceType)
	}{
		{name: "source", mutate: func(resourceType *atc.ResourceType) {
			resourceType.Source = atc.Source{"repository": "((source_repository))"}
		}},
		{name: "defaults", mutate: func(resourceType *atc.ResourceType) {
			resourceType.Defaults = atc.Source{"repository": "((default_repository))"}
		}},
		{name: "params", mutate: func(resourceType *atc.ResourceType) {
			resourceType.Params = atc.Params{"token": "((resource_token))"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := &atc.TaskStep{
				Name:       "selected",
				FunctionID: "selected",
				Config: &atc.TaskConfig{
					Platform: "linux",
					Run:      atc.TaskRunConfig{Path: "/bin/true"},
					ImageResource: &atc.ImageResource{
						Type:    "custom-task-image",
						Source:  atc.Source{"repository": "example/task"},
						Version: atc.Version{"digest": "sha256:immutable"},
					},
				},
			}
			resourceType := atc.ResourceType{Name: "custom-task-image", Image: exactDigestImage("4")}
			test.mutate(&resourceType)
			definition := extractTestDefinition([]atc.Step{{Config: task}}, nil)
			definition.Compiled.Function.ResourceTypes = atc.ResourceTypes{resourceType}

			_, err := ExtractFunctionTarget(definition, "selected")
			if err == nil || !strings.Contains(err.Error(), "interpolation") {
				t.Fatalf("error = %v, want retained resource-type interpolation refusal", err)
			}
		})
	}
}

func TestExtractFunctionTargetRejectsMissingAndDuplicateIDs(t *testing.T) {
	definition := extractTestDefinition([]atc.Step{{Config: &atc.AgentStep{Name: "one", FunctionID: "one", Prompt: "work"}}}, nil)
	_, err := ExtractFunctionTarget(definition, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing error = %v", err)
	}
	var notFound FunctionNotFoundError
	if !errors.As(err, &notFound) || notFound.FunctionID != "missing" {
		t.Fatalf("missing error = %T %v, want FunctionNotFoundError for missing", err, err)
	}

	definition.Compiled.Function.Plan = append(definition.Compiled.Function.Plan,
		atc.Step{Config: &atc.AgentStep{Name: "two", FunctionID: "one", Prompt: "work"}},
	)
	if _, err := ExtractFunctionTarget(definition, "one"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestExtractFunctionTargetRejectsInterpolationInSpoofedOutputTypesMap(t *testing.T) {
	task := &atc.TaskStep{
		Name:       "selected",
		FunctionID: "selected",
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
	definition := extractTestDefinition([]atc.Step{{Config: task}}, nil)

	_, err := ExtractFunctionTarget(definition, "selected")
	if err == nil || !strings.Contains(err.Error(), "interpolation") {
		t.Fatalf("error = %v, want interpolation refusal", err)
	}
}

func TestExtractFunctionTargetRejectsUnexpandedGlobalAssets(t *testing.T) {
	newDefinition := func() Definition {
		return extractTestDefinition([]atc.Step{{Config: &atc.AgentStep{Name: "selected", FunctionID: "selected", Prompt: "work"}}}, nil)
	}

	withCapabilities := newDefinition()
	withCapabilities.Compiled.Function.Capabilities = map[string]Capability{"dev": {
		Contract: "dev-mcp/v1",
		Sidecar:  atc.SidecarConfig{Name: "dev-mcp", Image: exactDigestImage("d")},
	}}
	withCapabilities.Compiled.Function.Plan[0].Config.(*atc.AgentStep).Capabilities = []string{"dev"}
	if _, err := ExtractFunctionTarget(withCapabilities, "selected"); err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("capability error = %v", err)
	}

	withSkills := newDefinition()
	withSkills.Compiled.Function.SkillFiles = map[string]string{"skills/dev/SKILL.md": "content"}
	if _, err := ExtractFunctionTarget(withSkills, "selected"); err == nil || !strings.Contains(err.Error(), "skills") {
		t.Fatalf("skill error = %v", err)
	}
}

func extractTestDefinition(plan []atc.Step, inputs []snapshot.Port) Definition {
	function := &FunctionConfig{SignatureVersion: 2, Inputs: inputs, Plan: plan}
	return Definition{
		ID:               52,
		Name:             "extract-flow",
		Version:          9,
		SchemaVersion:    3,
		SignatureVersion: 2,
		Compiled: CompiledDefinition{
			SchemaVersion: 3,
			Name:          "extract-flow",
			Function:      function,
		},
	}
}

func exactDigestImage(character string) string {
	return "registry.example/image@sha256:" + strings.Repeat(character, 64)
}
