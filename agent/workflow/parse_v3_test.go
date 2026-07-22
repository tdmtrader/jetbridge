package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"
)

const v3ProgramYAML = `schema_version: 3
name: code-review
signature_version: 1
description: Review one repository state relative to another.
inputs:
  - name: before
    type: repository/v1
  - name: after
    type: repository/v1
outputs:
  - name: review
    type: review/v1
    from: review
capabilities:
  dev:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
plan:
  - agent: review
    function_id: review
    prompt: Compare the declared repository snapshots and submit review.json.
    capabilities: [dev]
    inputs: [before, after]
    outputs: [review]
    input_types:
      before:
        type: repository/v1
      after:
        type: repository/v1
    output_types:
      review: review/v1
`

func TestParseV3ProgramExample(t *testing.T) {
	definition, err := workflow.ParseCompiled([]byte(v3ProgramYAML))
	if err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	if definition.SchemaVersion != 3 || definition.Name != "code-review" ||
		definition.Description != "Review one repository state relative to another." {
		t.Fatalf("unexpected envelope: %+v", definition)
	}
	if definition.Legacy != nil || definition.Function == nil {
		t.Fatalf("v3 must populate only the function arm: %+v", definition)
	}

	function := definition.Function
	if function.SignatureVersion != 1 {
		t.Fatalf("signature_version = %d, want 1", function.SignatureVersion)
	}
	if got := []string{function.Inputs[0].Name, function.Inputs[1].Name}; !reflect.DeepEqual(got, []string{"before", "after"}) {
		t.Fatalf("input order = %v", got)
	}
	if len(function.Outputs) != 1 || function.Outputs[0].Name != "review" ||
		function.Outputs[0].Type != snapshot.TypeRef("review/v1") || function.Outputs[0].From != "review" {
		t.Fatalf("unexpected outputs: %+v", function.Outputs)
	}
	capability, found := function.Capabilities["dev"]
	if !found || capability.Contract != "dev-mcp/v1" || capability.Sidecar.Name != "dev-mcp" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	if len(function.Plan) != 1 {
		t.Fatalf("plan length = %d, want 1", len(function.Plan))
	}
	agent, ok := function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("plan node = %T, want *atc.AgentStep", function.Plan[0].Config)
	}
	if agent.FunctionID != "review" || !reflect.DeepEqual(agent.Capabilities, []string{"dev"}) {
		t.Fatalf("agent annotations were not typed: %+v", agent)
	}
	if agent.SnapshotInputs["before"] != (atc.SnapshotInputConfig{Type: snapshot.TypeRef("repository/v1")}) ||
		agent.SnapshotInputs["after"] != (atc.SnapshotInputConfig{Type: snapshot.TypeRef("repository/v1")}) {
		t.Fatalf("agent input types = %+v", agent.SnapshotInputs)
	}
	if agent.SnapshotOutputs["review"].Type != snapshot.TypeRef("review/v1") {
		t.Fatalf("agent output types = %+v", agent.SnapshotOutputs)
	}
}

func TestParseV3YAMLAndJSONRoundTrip(t *testing.T) {
	want, err := workflow.ParseCompiled([]byte(v3ProgramYAML))
	if err != nil {
		t.Fatalf("ParseCompiled YAML: %v", err)
	}

	sourceJSON, err := yaml.YAMLToJSON([]byte(v3ProgramYAML))
	if err != nil {
		t.Fatalf("YAMLToJSON: %v", err)
	}
	fromJSON, err := workflow.ParseCompiled(sourceJSON)
	if err != nil {
		t.Fatalf("ParseCompiled JSON: %v", err)
	}
	assertCompiledDefinitionEqual(t, want, fromJSON)

	modelJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal model JSON: %v", err)
	}
	var jsonRoundTrip workflow.CompiledDefinition
	if err := json.Unmarshal(modelJSON, &jsonRoundTrip); err != nil {
		t.Fatalf("unmarshal model JSON: %v", err)
	}
	if err := jsonRoundTrip.Validate(); err != nil {
		t.Fatalf("validate JSON round trip: %v", err)
	}
	assertCompiledDefinitionEqual(t, want, &jsonRoundTrip)

	modelYAML, err := yaml.Marshal(want)
	if err != nil {
		t.Fatalf("marshal model YAML: %v", err)
	}
	var yamlRoundTrip workflow.CompiledDefinition
	if err := yaml.UnmarshalStrict(modelYAML, &yamlRoundTrip); err != nil {
		t.Fatalf("unmarshal model YAML: %v\n%s", err, modelYAML)
	}
	if err := yamlRoundTrip.Validate(); err != nil {
		t.Fatalf("validate YAML round trip: %v", err)
	}
	assertCompiledDefinitionEqual(t, want, &yamlRoundTrip)
}

func TestParseV3PreservesPublicSignatureOrder(t *testing.T) {
	doc := strings.Replace(v3ProgramYAML,
		"  - name: before\n    type: repository/v1\n  - name: after\n    type: repository/v1",
		"  - name: zebra\n    type: repository/v1\n  - name: alpha\n    type: repository/v1", 1)
	doc = strings.Replace(doc,
		"  - name: review\n    type: review/v1\n    from: review",
		"  - name: zebra\n    type: review/v1\n    from: review-z\n  - name: alpha\n    type: review/v1\n    from: review-a", 1)

	definition, err := workflow.ParseCompiled([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	inputs := []string{definition.Function.Inputs[0].Name, definition.Function.Inputs[1].Name}
	outputs := []string{definition.Function.Outputs[0].Name, definition.Function.Outputs[1].Name}
	if !reflect.DeepEqual(inputs, []string{"zebra", "alpha"}) || !reflect.DeepEqual(outputs, []string{"zebra", "alpha"}) {
		t.Fatalf("signature order changed: inputs=%v outputs=%v", inputs, outputs)
	}
}

func TestParseV3PreservesFunctionAnnotations(t *testing.T) {
	doc := v3WithPlan(`
  - task: transform
    function_id: transform-repository
    config:
      platform: linux
      run: {path: /bin/true}
      inputs: [{name: source}]
      outputs: [{name: result}]
    input_types:
      repository: {type: repository/v1, optional: true}
    output_types:
      change: repository-change/v1
  - agent: review
    function_id: review-change
    capabilities: [dev, jira]
    inputs: [change]
    outputs: [review]
    input_types:
      change: {type: repository-change/v1}
    output_types:
      review: review/v1`)
	definition, err := workflow.ParseCompiled([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	task := definition.Function.Plan[0].Config.(*atc.TaskStep)
	agent := definition.Function.Plan[1].Config.(*atc.AgentStep)
	if task.FunctionID != "transform-repository" || !task.SnapshotInputs["repository"].Optional ||
		task.SnapshotOutputs["change"].Type != snapshot.TypeRef("repository-change/v1") {
		t.Fatalf("task annotations = %+v", task)
	}
	if agent.FunctionID != "review-change" || !reflect.DeepEqual(agent.Capabilities, []string{"dev", "jira"}) ||
		agent.SnapshotOutputs["review"].Type != snapshot.TypeRef("review/v1") {
		t.Fatalf("agent annotations = %+v", agent)
	}
}

func TestParseV3AllowsConcourseDeclarations(t *testing.T) {
	doc := `schema_version: 3
name: declarations
signature_version: 1
inputs: []
outputs: []
resource_types:
  - name: custom-git
    type: registry-image
    source: {repository: example/git-resource}
prototypes:
  - name: notify
    type: registry-image
    source: {repository: example/notify-resource}
resources:
  - name: repo
    type: custom-git
    source: {uri: https://example.invalid/repo.git, opaque_provider_value: true}
var_sources:
  - name: credentials
    type: dummy
    config: {token: ((token)), opaque_provider_value: true}
plan:
  - get: repo
`
	definition, err := workflow.ParseCompiled([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	function := definition.Function
	if len(function.Resources) != 1 || function.Resources[0].Name != "repo" ||
		len(function.ResourceTypes) != 1 || function.ResourceTypes[0].Name != "custom-git" ||
		len(function.Prototypes) != 1 || function.Prototypes[0].Name != "notify" ||
		len(function.VarSources) != 1 || function.VarSources[0].Name != "credentials" {
		t.Fatalf("declarations did not survive: %+v", function)
	}
}

func TestParseV3AllowsOrdinaryNestedSteps(t *testing.T) {
	doc := v3WithPlan(`
  - timeout: 30m
    do:
      - in_parallel:
          limit: 2
          steps:
            - get: repo
            - try:
                task: inspect
                config:
                  platform: linux
                  run: {path: /bin/true}
      - attempts: 2
        task: finish
        function_id: finish
        config:
          platform: linux
          run: {path: /bin/true}
        on_failure:
          agent: diagnose
          function_id: diagnose
          prompt: diagnose
`)
	definition, err := workflow.ParseCompiled([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCompiled nested plan: %v", err)
	}
	timeout, ok := definition.Function.Plan[0].Config.(*atc.TimeoutStep)
	if !ok {
		t.Fatalf("root = %T, want timeout wrapper", definition.Function.Plan[0].Config)
	}
	do, ok := timeout.Step.(*atc.DoStep)
	if !ok || len(do.Steps) != 2 {
		t.Fatalf("timeout child = %#v, want two-step do", timeout.Step)
	}
	if _, ok := do.Steps[0].Config.(*atc.InParallelStep); !ok {
		t.Fatalf("first do child = %T, want in_parallel", do.Steps[0].Config)
	}
	hook, ok := do.Steps[1].Config.(*atc.OnFailureStep)
	if !ok {
		t.Fatalf("second do child = %T, want on_failure wrapper", do.Steps[1].Config)
	}
	if _, ok := hook.Step.(*atc.RetryStep); !ok {
		t.Fatalf("on_failure child = %T, want retry wrapper", hook.Step)
	}
	if _, ok := hook.Hook.Config.(*atc.AgentStep); !ok {
		t.Fatalf("on_failure hook = %T, want agent", hook.Hook.Config)
	}
}

func TestParseV3AllowsInParallelListAndObjectForms(t *testing.T) {
	doc := v3WithPlan(`
  - in_parallel:
      - get: first
      - get: second
  - in_parallel:
      limit: 2
      fail_fast: true
      steps:
        - get: third
        - get: fourth`)
	definition, err := workflow.ParseCompiled([]byte(doc))
	if err != nil {
		t.Fatalf("ParseCompiled parallel forms: %v", err)
	}
	if len(definition.Function.Plan) != 2 {
		t.Fatalf("plan length = %d, want 2", len(definition.Function.Plan))
	}
	list := definition.Function.Plan[0].Config.(*atc.InParallelStep)
	object := definition.Function.Plan[1].Config.(*atc.InParallelStep)
	if len(list.Config.Steps) != 2 || list.Config.Limit != 0 || list.Config.FailFast {
		t.Fatalf("unexpected list form: %+v", list.Config)
	}
	if len(object.Config.Steps) != 2 || object.Config.Limit != 2 || !object.Config.FailFast {
		t.Fatalf("unexpected object form: %+v", object.Config)
	}
}

func TestParseV3PreservesOpaqueStepProviderMaps(t *testing.T) {
	doc := v3WithPlan(`
  - get: repo
    version: {provider_revision: v1}
    params: {provider_get_option: true}
  - task: inspect
    vars: {provider_var: value}
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {provider_source_option: value}
        version: {provider_image_revision: v2}
        params: {provider_image_option: true}
      params: {PROVIDER_ENV: value}
      run: {path: /bin/true}
  - put: result
    params: {provider_put_option: true}
    get_params: {provider_get_option: true}
  - across:
      - var: provider
        values: [{provider_value: true}]
    get: repo`)
	if _, err := workflow.ParseCompiled([]byte(doc)); err != nil {
		t.Fatalf("ParseCompiled opaque provider maps: %v", err)
	}
}

func TestParseV3StrictnessDoesNotChangeOrdinaryATCDecoding(t *testing.T) {
	payload := []byte(`jobs:
  - name: compatibility
    plan:
      - in_parallel:
          steps: [{get: repo}]
          limti: 2
`)
	var config atc.Config
	if err := atc.UnmarshalConfig(payload, &config); err != nil {
		t.Fatalf("ordinary ATC compatibility parse: %v", err)
	}
	parallel, ok := config.Jobs[0].PlanSequence[0].Config.(*atc.InParallelStep)
	if !ok || parallel.Config.Limit != 0 || len(parallel.Config.Steps) != 1 {
		t.Fatalf("unexpected ordinary ATC result: %#v", config.Jobs[0].PlanSequence[0].Config)
	}

	doc := v3WithPlan("\n  - in_parallel:\n      steps: [{get: repo}]\n      limti: 2")
	if _, err := workflow.ParseCompiled([]byte(doc)); err == nil {
		t.Fatal("v3 parser accepted the same misspelled wrapper config")
	}
}

func TestParseV3OutputTypesRequireTypeReferenceStrings(t *testing.T) {
	valid := v3WithPlan(`
  - agent: work
    prompt: work
    output_types:
      result: review/v1`)
	if _, err := workflow.ParseCompiled([]byte(valid)); err != nil {
		t.Fatalf("ParseCompiled scalar output type: %v", err)
	}

	cases := map[string]string{
		"typed object":           `{type: review/v1}`,
		"runtime linkage object": `{type: review/v1, retention: workflow, workflow_port: result, workflow_definition_id: 17, workflow_run_id: "9007199254740993"}`,
		"arbitrary map":          `{provider_value: review/v1}`,
		"numeric value":          `7`,
		"boolean value":          `true`,
		"sequence value":         `[review/v1]`,
		"explicit null value":    `null`,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			doc := v3WithPlan(`
  - agent: work
    prompt: work
    output_types:
      result: ` + value)
			_, err := workflow.ParseCompiled([]byte(doc))
			if err == nil {
				t.Fatalf("accepted non-string output type %s", value)
			}
			if !strings.Contains(err.Error(), "type-reference string") {
				t.Fatalf("error = %q, want type-reference string context", err)
			}
		})
	}

	taskDoc := v3WithPlan(`
  - task: work
    config:
      platform: linux
      run: {path: /bin/true}
    output_types:
      result: {type: review/v1}`)
	if _, err := workflow.ParseCompiled([]byte(taskDoc)); err == nil || !strings.Contains(err.Error(), "type-reference string") {
		t.Fatalf("task output object error = %v, want type-reference string rejection", err)
	}
}

func TestParseV3OutputTypeStrictnessDoesNotChangeOrdinaryATCLongForm(t *testing.T) {
	payload := []byte(`jobs:
  - name: compatibility
    plan:
      - agent: work
        prompt: work
        output_types:
          result:
            type: review/v1
            retention: workflow
            workflow_port: result
            workflow_definition_id: 17
            workflow_run_id: "9007199254740993"
`)
	var config atc.Config
	if err := atc.UnmarshalConfig(payload, &config); err != nil {
		t.Fatalf("ordinary ATC long-form output type: %v", err)
	}
	agent, ok := config.Jobs[0].PlanSequence[0].Config.(*atc.AgentStep)
	if !ok || agent.SnapshotOutputs["result"].Type != snapshot.TypeRef("review/v1") ||
		agent.SnapshotOutputs["result"].Retention != snapshot.RetentionClassWorkflow {
		t.Fatalf("unexpected ordinary ATC output config: %#v", config.Jobs[0].PlanSequence[0].Config)
	}

	doc := v3WithPlan(`
  - agent: work
    prompt: work
    output_types:
      result:
        type: review/v1`)
	if _, err := workflow.ParseCompiled([]byte(doc)); err == nil {
		t.Fatal("v3 source accepted ATC's internal long-form output config")
	}
}

func TestParseV3LegacyArmsAreExclusive(t *testing.T) {
	legacyV1, err := workflow.ParseCompiled([]byte(validV1YAML()))
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	legacyV2, err := workflow.ParseCompiled([]byte(v2YAML))
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	function, err := workflow.ParseCompiled([]byte(v3ProgramYAML))
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	if legacyV1.Legacy == nil || legacyV1.Function != nil || legacyV2.Legacy == nil || legacyV2.Function != nil {
		t.Fatalf("legacy arm selection failed: v1=%+v v2=%+v", legacyV1, legacyV2)
	}
	if function.Legacy != nil || function.Function == nil {
		t.Fatalf("function arm selection failed: %+v", function)
	}

	validFunction := *function.Function
	validLegacy := *legacyV1.Legacy
	invalid := []workflow.CompiledDefinition{
		{SchemaVersion: 3, Name: "bad"},
		{SchemaVersion: 3, Name: "bad", Legacy: &validLegacy, Function: &validFunction},
		{SchemaVersion: 2, Name: validLegacy.Name, Function: &validFunction},
		{SchemaVersion: 1, Name: validLegacy.Name, Legacy: &validLegacy, Function: &validFunction},
		{SchemaVersion: 4, Name: "bad", Function: &validFunction},
	}
	for i := range invalid {
		if err := invalid[i].Validate(); err == nil {
			t.Errorf("invalid union %d unexpectedly validated: %+v", i, invalid[i])
		}
	}
}

func TestCompiledDefinitionValidateRejectsUnknownPlanFields(t *testing.T) {
	var definition workflow.CompiledDefinition
	err := json.Unmarshal([]byte(`{
		"schema_version": 3,
		"name": "direct-model",
		"function": {
			"signature_version": 1,
			"inputs": [],
			"outputs": [],
			"plan": [{"get": "repo", "typo": true}]
		}
	}`), &definition)
	if err != nil {
		t.Fatalf("unmarshal direct model: %v", err)
	}
	if err := definition.Validate(); err == nil {
		t.Fatal("directly decoded model with an unknown plan field unexpectedly validated")
	}
}

func TestParseV1V2StoredFixturesUnchanged(t *testing.T) {
	paths, err := filepath.Glob("seeds/*.yaml")
	if err != nil {
		t.Fatalf("glob seeds: %v", err)
	}
	paths = append(paths, "parse-v1-inline", "parse-v2-inline")
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			var raw []byte
			switch path {
			case "parse-v1-inline":
				raw = []byte(validV1YAML())
			case "parse-v2-inline":
				raw = []byte(v2YAML)
			default:
				var err error
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatalf("read fixture: %v", err)
				}
			}
			legacy, err := workflow.Parse(raw)
			if err != nil {
				t.Fatalf("legacy Parse: %v", err)
			}
			compiled, err := workflow.ParseCompiled(raw)
			if err != nil {
				t.Fatalf("ParseCompiled: %v", err)
			}
			if compiled.Function != nil || compiled.Legacy == nil || !reflect.DeepEqual(legacy, compiled.Legacy) {
				t.Fatalf("legacy semantics drifted:\nlegacy=%+v\ncompiled=%+v", legacy, compiled)
			}
		})
	}
}

func TestParseV3StrictErrors(t *testing.T) {
	base := v3WithPlan(`
  - agent: work
    function_id: work
    inputs: [input]
    outputs: [output]
    input_types:
      input: {type: repository/v1}
    output_types:
      output: review/v1`)
	base = strings.Replace(base, "inputs: []", "inputs:\n  - name: input\n    type: repository/v1", 1)
	base = strings.Replace(base, "outputs: []", "outputs:\n  - name: output\n    type: review/v1\n    from: output", 1)

	cases := map[string]string{
		"missing schema version":                  strings.Replace(base, "schema_version: 3\n", "", 1),
		"zero schema version":                     strings.Replace(base, "schema_version: 3", "schema_version: 0", 1),
		"string schema version":                   strings.Replace(base, "schema_version: 3", `schema_version: "3"`, 1),
		"fractional schema version":               strings.Replace(base, "schema_version: 3", "schema_version: 3.5", 1),
		"unsupported schema version":              strings.Replace(base, "schema_version: 3", "schema_version: 4", 1),
		"unknown top-level":                       base + "scheam_version: 3\n",
		"wrong-case top-level":                    strings.Replace(base, "name: strict-plan", "Name: strict-plan", 1),
		"second document":                         base + "---\nschema_version: 3\n",
		"duplicate mapping key":                   strings.Replace(base, "name: strict-plan", "name: strict-plan\nname: duplicate", 1),
		"duplicate capability key":                strings.Replace(base, "plan:\n", "capabilities:\n  dev: {contract: dev-mcp/v1, sidecar: {name: one, image: example/one}}\n  dev: {contract: dev-mcp/v1, sidecar: {name: two, image: example/two}}\nplan:\n", 1),
		"missing signature version":               strings.Replace(base, "signature_version: 1\n", "", 1),
		"zero signature version":                  strings.Replace(base, "signature_version: 1", "signature_version: 0", 1),
		"negative signature version":              strings.Replace(base, "signature_version: 1", "signature_version: -1", 1),
		"string signature version":                strings.Replace(base, "signature_version: 1", `signature_version: "1"`, 1),
		"fractional signature version":            strings.Replace(base, "signature_version: 1", "signature_version: 1.5", 1),
		"blank name":                              strings.Replace(base, "name: strict-plan", `name: " "`, 1),
		"missing input name":                      strings.Replace(base, "  - name: input\n    type: repository/v1", "  - type: repository/v1", 1),
		"numeric input name":                      strings.Replace(base, "name: input", "name: 7", 1),
		"missing input type":                      strings.Replace(base, "  - name: input\n    type: repository/v1", "  - name: input", 1),
		"numeric input type":                      strings.Replace(base, "type: repository/v1", "type: 7", 1),
		"malformed input type":                    strings.Replace(base, "type: repository/v1", "type: Repository/v1", 1),
		"duplicate input name":                    strings.Replace(base, "  - name: input\n    type: repository/v1", "  - name: input\n    type: repository/v1\n  - name: input\n    type: opaque/v1", 1),
		"missing output name":                     strings.Replace(base, "  - name: output\n    type: review/v1\n    from: output", "  - type: review/v1\n    from: output", 1),
		"missing output type":                     strings.Replace(base, "    type: review/v1\n    from: output", "    from: output", 1),
		"duplicate output name":                   strings.Replace(base, "  - name: output\n    type: review/v1\n    from: output", "  - name: output\n    type: review/v1\n    from: output\n  - name: output\n    type: opaque/v1\n    from: other", 1),
		"missing output from":                     strings.Replace(base, "    from: output\n", "", 1),
		"blank output from":                       strings.Replace(base, "from: output", `from: " "`, 1),
		"numeric output from":                     strings.Replace(base, "from: output", "from: 7", 1),
		"unknown input field":                     strings.Replace(base, "    type: repository/v1", "    type: repository/v1\n    optionel: true", 1),
		"wrong-case input field":                  strings.Replace(base, "  - name: input", "  - Name: input", 1),
		"unknown output field":                    strings.Replace(base, "    from: output", "    from: output\n    form: output", 1),
		"wrong-case output field":                 strings.Replace(base, "    from: output", "    From: output", 1),
		"unknown capability field":                strings.Replace(base, "plan:\n", "capabilities:\n  dev:\n    contract: dev-mcp/v1\n    typo: true\n    sidecar: {name: dev, image: example/dev}\nplan:\n", 1),
		"wrong-case capability field":             strings.Replace(base, "plan:\n", "capabilities:\n  dev:\n    Contract: dev-mcp/v1\n    sidecar: {name: dev, image: example/dev}\nplan:\n", 1),
		"unknown capability sidecar field":        strings.Replace(base, "plan:\n", "capabilities:\n  dev:\n    contract: dev-mcp/v1\n    sidecar: {name: dev, image: example/dev, typo: true}\nplan:\n", 1),
		"numeric capability contract":             strings.Replace(base, "plan:\n", "capabilities:\n  dev:\n    contract: 7\n    sidecar: {name: dev, image: example/dev}\nplan:\n", 1),
		"numeric capability sidecar name":         strings.Replace(base, "plan:\n", "capabilities:\n  dev:\n    contract: dev-mcp/v1\n    sidecar: {name: 7, image: example/dev}\nplan:\n", 1),
		"empty plan":                              strings.Split(base, "plan:")[0] + "plan: []\n",
		"missing plan":                            strings.Split(base, "plan:")[0],
		"jobs field":                              base + "jobs: [{name: hidden, plan: [{get: x}]}]\n",
		"legacy steps field":                      base + "steps: [{agent: hidden}]\n",
		"legacy spec delivery field":              base + "spec_delivery: files\n",
		"legacy defaults field":                   base + "defaults: {}\n",
		"legacy budget field":                     base + "budget: {ticket_usd: 1}\n",
		"legacy sidecars field":                   base + "sidecars: {}\n",
		"legacy prompts field":                    base + "prompts: {}\n",
		"legacy schemas field":                    base + "schemas: {}\n",
		"legacy hitl field":                       base + "hitl: {}\n",
		"legacy gate policy field":                base + "gate_policy: {}\n",
		"legacy judge field":                      base + "judge: {}\n",
		"legacy prompt files field":               base + "prompt_files: {}\n",
		"legacy skills field":                     base + "skills: []\n",
		"legacy system prompt field":              base + "system_prompt: hidden\n",
		"legacy system prompt file field":         base + "system_prompt_file: hidden.md\n",
		"legacy context field":                    base + "context: []\n",
		"ordinary groups field":                   base + "groups: []\n",
		"ordinary display field":                  base + "display: {}\n",
		"ordinary template field":                 base + "template: true\n",
		"ordinary params field":                   base + "params: []\n",
		"ordinary run retention field":            base + "run_retention: {}\n",
		"no core step":                            v3WithPlan("\n  - timeout: 1m"),
		"two core steps":                          v3WithPlan("\n  - get: one\n    put: two"),
		"unknown top-level embedded step field":   v3WithPlan("\n  - get: repo\n    typo: true"),
		"unknown do child field":                  v3WithPlan("\n  - do:\n      - get: repo\n        typo: true"),
		"unknown parallel child field":            v3WithPlan("\n  - in_parallel:\n      - get: repo\n        typo: true"),
		"unknown parallel object config field":    v3WithPlan("\n  - in_parallel:\n      steps: [{get: repo}]\n      limti: 2"),
		"wrong-case parallel object config field": v3WithPlan("\n  - in_parallel:\n      steps: [{get: repo}]\n      Limit: 2"),
		"unknown try child field":                 v3WithPlan("\n  - try:\n      get: repo\n      typo: true"),
		"unknown across child field":              v3WithPlan("\n  - across: [{var: branch, values: [main]}]\n    get: repo\n    typo: true"),
		"unknown across config field":             v3WithPlan("\n  - across: [{var: branch, values: [main], typo: true}]\n    get: repo"),
		"wrong-case across config field":          v3WithPlan("\n  - across: [{Var: branch, values: [main]}]\n    get: repo"),
		"unknown retry child field":               v3WithPlan("\n  - attempts: 2\n    get: repo\n    typo: true"),
		"unknown timeout child field":             v3WithPlan("\n  - timeout: 1m\n    do:\n      - get: repo\n        typo: true"),
		"unknown on-success hook field":           v3WithPlan("\n  - get: repo\n    on_success:\n      put: report\n      typo: true"),
		"unknown hook child field":                v3WithPlan("\n  - get: repo\n    on_failure:\n      put: report\n      typo: true"),
		"unknown on-abort hook field":             v3WithPlan("\n  - get: repo\n    on_abort:\n      put: report\n      typo: true"),
		"unknown on-error hook field":             v3WithPlan("\n  - get: repo\n    on_error:\n      put: report\n      typo: true"),
		"unknown ensure hook field":               v3WithPlan("\n  - get: repo\n    ensure:\n      put: report\n      typo: true"),
		"unknown task config field":               v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      platfrom: linux\n      run: {path: /bin/true}"),
		"wrong-case task config field":            v3WithPlan("\n  - task: work\n    config:\n      Platform: linux\n      run: {path: /bin/true}"),
		"unknown task run config field":           v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      run: {path: /bin/true, paht: /bin/false}"),
		"unknown task input config field":         v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      run: {path: /bin/true}\n      inputs: [{name: source, typo: true}]"),
		"unknown task output config field":        v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      run: {path: /bin/true}\n      outputs: [{name: result, typo: true}]"),
		"unknown task cache config field":         v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      run: {path: /bin/true}\n      caches: [{path: cache, typo: true}]"),
		"unknown task scratch config field":       v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      run: {path: /bin/true}\n      scratch_paths: [{path: scratch, typo: true}]"),
		"unknown task image config field":         v3WithPlan("\n  - task: work\n    config:\n      platform: linux\n      image_resource: {type: registry-image, source: {repository: example/image}, typo: true}\n      run: {path: /bin/true}"),
		"unknown container limit field":           v3WithPlan("\n  - task: work\n    container_limits: {cpu: 1, typo: 2}\n    config:\n      platform: linux\n      run: {path: /bin/true}"),
		"wrong-case step input type field":        v3WithPlan("\n  - agent: work\n    prompt: work\n    input_types: {source: {Type: repository/v1}}"),
		"wrong-case step output type field":       v3WithPlan("\n  - agent: work\n    prompt: work\n    output_types: {result: {Type: repository/v1}}"),
		"unknown inline sidecar field":            v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, image: example/helper, typo: true}]"),
		"wrong-case inline sidecar field":         v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, Image: example/helper}]"),
		"unknown inline sidecar env field":        v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, image: example/helper, env: [{name: TOKEN, value: x, typo: true}]}]"),
		"unknown inline sidecar port field":       v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, image: example/helper, ports: [{containerPort: 8080, typo: true}]}]"),
		"unknown inline sidecar resources field":  v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, image: example/helper, resources: {requests: {cpu: 1}, typo: true}}]"),
		"unknown inline sidecar quantity field":   v3WithPlan("\n  - agent: work\n    prompt: work\n    sidecars: [{name: helper, image: example/helper, resources: {requests: {cpu: 1, typo: 2}}}]"),
		"unknown harvest gate policy field":       v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n    gate_policy: {typo: true}"),
		"wrong-case harvest gate policy field":    v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n    gate_policy: {Gates: []}"),
		"unknown harvest gate field":              v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n    gate_policy: {gates: [{gate: test, scope: full, typo: true}]}"),
		"unknown harvest judge field":             v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n    judge: {rubric: [{name: correctness, weight: 1, guidance: good}], pass_threshold: 1, typo: true}"),
		"unknown harvest rubric field":            v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n    judge: {rubric: [{name: correctness, weight: 1, guidance: good, typo: true}], pass_threshold: 1}"),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.ParseCompiled([]byte(doc)); err == nil {
				t.Fatalf("expected strict parse error for:\n%s", doc)
			}
		})
	}

	for _, invalid := range []string{"review/v0", "review/v01"} {
		t.Run("invalid type "+invalid, func(t *testing.T) {
			doc := strings.Replace(base, "type: review/v1", "type: "+invalid, 1)
			if _, err := workflow.ParseCompiled([]byte(doc)); err == nil {
				t.Fatalf("expected invalid type error for %q", invalid)
			}
		})
	}
}

func TestParseV3ValidatesAgentAssetFieldPresenceBeforeTypedDecoding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty inline prompt still conflicts with prompt file",
			body: "    prompt: ''\n    prompt_file: prompts/work.md",
			want: "prompt and prompt_file are mutually exclusive",
		},
		{
			name: "null inline prompt still conflicts with prompt file",
			body: "    prompt: null\n    prompt_file: prompts/work.md",
			want: "prompt and prompt_file are mutually exclusive",
		},
		{
			name: "empty prompt file",
			body: "    prompt_file: ''",
			want: "prompt_file must be a nonempty string",
		},
		{
			name: "null prompt file",
			body: "    prompt_file: null",
			want: "prompt_file must be a nonempty string",
		},
		{
			name: "empty inline system prompt still conflicts with system prompt file",
			body: "    prompt: work\n    system_prompt: ''\n    system_prompt_file: prompts/system.md",
			want: "system_prompt and system_prompt_file are mutually exclusive",
		},
		{
			name: "null inline system prompt still conflicts with system prompt file",
			body: "    prompt: work\n    system_prompt: null\n    system_prompt_file: prompts/system.md",
			want: "system_prompt and system_prompt_file are mutually exclusive",
		},
		{
			name: "empty system prompt file",
			body: "    prompt: work\n    system_prompt_file: ''",
			want: "system_prompt_file must be a nonempty string",
		},
		{
			name: "null system prompt file",
			body: "    prompt: work\n    system_prompt_file: null",
			want: "system_prompt_file must be a nonempty string",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := v3WithPlan(`
  - do:
      - agent: work
        function_id: inspect
` + strings.ReplaceAll(test.body, "    ", "        "))
			definition, err := workflow.ParseCompiled([]byte(document))
			if err == nil || definition != nil {
				t.Fatalf("ParseCompiled = (%+v, %v), want nil and error", definition, err)
			}
			for _, want := range []string{test.want, `workflow.plan[0].do[0]`, `agent "work"`, `function_id "inspect"`} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err, want)
				}
			}
		})
	}
}

func v3WithPlan(plan string) string {
	return `schema_version: 3
name: strict-plan
signature_version: 1
inputs: []
outputs: []
plan:` + plan + "\n"
}

func assertCompiledDefinitionEqual(t *testing.T, want, got *workflow.CompiledDefinition) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("compiled definitions differ (-want +got):\n%s", diff)
	}
	if got.Function != nil && len(got.Function.Plan) > 0 {
		if reflect.TypeOf(want.Function.Plan[0].Config) != reflect.TypeOf(got.Function.Plan[0].Config) {
			t.Fatalf("concrete step type changed: want %T got %T", want.Function.Plan[0].Config, got.Function.Plan[0].Config)
		}
	}
}
