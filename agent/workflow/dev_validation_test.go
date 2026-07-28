package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func devValidationManifest(config string) workflow.Manifest {
	return workflow.Manifest{
		workflow.WorkflowFileName: `schema_version: 3
name: validate
signature_version: 1
inputs: []
outputs: []
capabilities:
  validator:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      ports: [{containerPort: 7780, protocol: TCP}]
dev_validation_profiles:
  - name: check
    capability: validator
    profile_file: validation/profile.yml
    config_file: validation/config.yml
    candidate: {name: candidate, type: opaque/v1}
    base_inputs:
      - {name: base, type: opaque/v1}
plan:
  - agent: work
    function_id: work
    prompt: do work
`,
		"validation/profile.yml": "schema_version: 1\nname: check\nchecks: []\n",
		"validation/config.yml":  config,
	}
}

func TestCompileDefinitionFreezesDevValidationAuthority(t *testing.T) {
	profile := "schema_version: 1\nname: check\nchecks: []\n"
	config := "schema_version: 1\ncomponents: []\n"
	manifest := workflow.Manifest{
		workflow.WorkflowFileName: `schema_version: 3
name: validate
signature_version: 1
inputs: []
outputs: []
capabilities:
  validator:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      ports: [{containerPort: 7780, protocol: TCP}]
dev_validation_profiles:
  - name: check
    capability: validator
    profile_file: validation/profile.yml
    config_file: validation/config.yml
    candidate: {name: candidate, type: opaque/v1}
    base_inputs:
      - {name: base, type: opaque/v1}
plan:
  - agent: work
    function_id: work
    prompt: do work
`,
		"validation/profile.yml": profile,
		"validation/config.yml":  config,
	}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	compiled, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal compiled definition: %v", err)
	}
	text := string(compiled)
	for _, want := range []string{
		`"dev_validation_profiles"`, `"capability_image"`,
		`"profile_digest"`, `"protected_config_digest"`,
		`"dev_validation_provenance_hash"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compiled definition missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "profile_file") || strings.Contains(text, "config_file") || strings.Contains(text, "\"capability\":\"validator\"") {
		t.Fatalf("compiled definition retained source authority: %s", text)
	}
}

func TestParseCompiledRequiresFrozenValidationAuthority(t *testing.T) {
	manifest := devValidationManifest("schema_version: 1\ncomponents: []\n")
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := workflow.ParseCompiled(raw)
	if err != nil {
		t.Fatalf("ParseCompiled compiled JSON: %v", err)
	}
	if len(parsed.Function.DevValidationProfiles) != 1 || parsed.Function.DevValidationProvenanceHash == "" {
		t.Fatalf("compiled authority lost: %+v", parsed.Function)
	}
	if _, err := workflow.ParseCompiled([]byte(manifest[workflow.WorkflowFileName])); err == nil {
		t.Fatal("ParseCompiled accepted a human source document")
	}
	if _, err := workflow.ParseCompiled([]byte(`{"schema_version":3,"name":"compiled","function":{"signature_version":1,"inputs":[],"outputs":[],"plan":[],"capabilities":{}}}`)); err == nil {
		t.Fatal("ParseCompiled accepted unresolved source capabilities")
	}
	if _, err := workflow.ParseCompiled(append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)); err == nil {
		t.Fatal("ParseCompiled accepted an unknown compiled field")
	}
	for _, mutation := range []func(*workflow.FunctionConfig){
		func(function *workflow.FunctionConfig) { function.DevValidationProfiles[0].Profile[0] ^= 1 },
		func(function *workflow.FunctionConfig) {
			function.DevValidationProfiles[0].CapabilityImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		func(function *workflow.FunctionConfig) {
			function.DevValidationProfiles[0].BaseInputs = append(function.DevValidationProfiles[0].BaseInputs, workflow.DevValidationContract{Name: "candidate", Type: "opaque/v1"})
		},
	} {
		copy := *parsed.Function
		copy.DevValidationProfiles = append([]workflow.CompiledDevValidationProfile(nil), parsed.Function.DevValidationProfiles...)
		copy.DevValidationProfiles[0].Profile = append([]byte(nil), parsed.Function.DevValidationProfiles[0].Profile...)
		copy.DevValidationProfiles[0].BaseInputs = append([]workflow.DevValidationContract(nil), parsed.Function.DevValidationProfiles[0].BaseInputs...)
		mutation(&copy)
		if err := copy.Validate(); err == nil {
			t.Fatal("tampered compiled authority unexpectedly validated")
		}
	}
}

func TestCompileDefinitionRejectsInvalidDevValidationAuthoritySource(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(workflow.Manifest)
	}{
		{name: "wrong capability contract", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "contract: dev-mcp/v1", "contract: other/v1", 1)
		}},
		{name: "candidate base overlap", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "{name: base, type: opaque/v1}", "{name: candidate, type: opaque/v1}", 1)
		}},
		{name: "optional contract", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "candidate: {name: candidate, type: opaque/v1}", "candidate: {name: candidate, type: opaque/v1, optional: true}", 1)
		}},
		{name: "url asset", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "validation/profile.yml", "https://example.invalid/profile.yml", 1)
		}},
		{name: "mutable root asset", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "validation/profile.yml", "candidate/profile.yml", 1)
			manifest["candidate/profile.yml"] = manifest["validation/profile.yml"]
		}},
		{name: "profile nested unknown", mutate: func(manifest workflow.Manifest) {
			manifest["validation/profile.yml"] = "schema_version: 1\nname: check\nchecks: []\nunknown: true\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := devValidationManifest("schema_version: 1\ncomponents: []\n")
			test.mutate(manifest)
			if _, err := workflow.CompileDefinition(manifest); err == nil {
				t.Fatal("invalid validation authority source compiled")
			}
		})
	}
}

func TestRenderedValidationAuthorityIsDeepCopiedAndBoundIntoIdentity(t *testing.T) {
	first, err := workflow.CompileDefinition(devValidationManifest("schema_version: 1\ncomponents: []\n"))
	if err != nil {
		t.Fatalf("compile first: %v", err)
	}
	second, err := workflow.CompileDefinition(devValidationManifest("schema_version: 1\ncomponents:\n  - id: changed\n    description: changed\n    paths: [.]\n    kind: go\n"))
	if err != nil {
		t.Fatalf("compile second: %v", err)
	}
	makeDefinition := func(compiled workflow.CompiledDefinition) workflow.Definition {
		return workflow.Definition{ID: 1, Name: "validate", Version: 1, SchemaVersion: 3, SignatureVersion: 1, ContentHash: strings.Repeat("a", 64), Compiled: compiled}
	}
	target, err := workflow.FullFunctionTarget(makeDefinition(*first))
	if err != nil {
		t.Fatalf("full target: %v", err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	otherTarget, err := workflow.FullFunctionTarget(makeDefinition(*second))
	if err != nil {
		t.Fatalf("second target: %v", err)
	}
	other, err := workflow.RenderFunction(otherTarget)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if rendered.TargetConfigHash == other.TargetConfigHash {
		t.Fatal("validation authority did not affect rendered target identity")
	}
	rendered.DevValidationProfiles[0].Profile[0] ^= 1
	if target.Function.DevValidationProfiles[0].Profile[0] == rendered.DevValidationProfiles[0].Profile[0] {
		t.Fatal("rendered authority aliases target authority")
	}
	if len(rendered.Config.Jobs[0].PlanSequence) != 1 {
		t.Fatalf("validation compilation materialized executable task: %+v", rendered.Config.Jobs[0].PlanSequence)
	}
}
