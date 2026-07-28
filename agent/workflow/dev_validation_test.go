package workflow_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
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

func TestParseCompiledRejectsAggregateValidationAuthorityOverflow(t *testing.T) {
	profiles := make([]workflow.CompiledDevValidationProfile, 0, workflow.MaxCompiledAssetBytes/(2*workflow.MaxManifestFileBytes)+1)
	for index := 0; index < cap(profiles); index++ {
		name := fmt.Sprintf("profile-%d", index)
		profile := []byte("schema_version: 1\nname: " + name + "\nchecks: []\n")
		profile = append(profile, bytes.Repeat([]byte(" "), workflow.MaxManifestFileBytes-len(profile))...)
		config := []byte("schema_version: 1\ncomponents: []\n")
		config = append(config, bytes.Repeat([]byte(" "), workflow.MaxManifestFileBytes-len(config))...)
		profiles = append(profiles, workflow.CompiledDevValidationProfile{Name: name, Candidate: workflow.DevValidationContract{Name: "candidate", Type: "opaque/v1"}, CapabilityImage: "registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CapabilityImageDigest: snapshot.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), Command: []string{"/usr/local/bin/dev-capability", "validate"}, Profile: profile, ProfileDigest: snapshot.Digest("sha256:" + workflow.Hash(profile)), ProtectedConfig: config, ProtectedConfigDigest: snapshot.Digest("sha256:" + workflow.Hash(config))})
	}
	provenance, err := workflow.DevValidationProvenanceHash(profiles)
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	definition := workflow.CompiledDefinition{SchemaVersion: 3, Name: "aggregate", Function: &workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.AgentStep{Name: "work", FunctionID: "work", Prompt: "work"}}}, DevValidationProfiles: profiles, DevValidationProvenanceHash: provenance}}
	if err := definition.Validate(); err == nil {
		t.Fatal("FunctionConfig accepted aggregate validation authority above budget")
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := workflow.ParseCompiled(raw); err == nil {
		t.Fatal("ParseCompiled accepted aggregate validation authority above budget")
	}
}

func TestDevValidationAuthorityIgnoresInteractiveCapabilityRuntimeFields(t *testing.T) {
	base, err := workflow.CompileDefinition(devValidationManifest("schema_version: 1\ncomponents: []\n"))
	if err != nil {
		t.Fatalf("compile base: %v", err)
	}
	changedManifest := devValidationManifest("schema_version: 1\ncomponents: []\n")
	changedManifest[workflow.WorkflowFileName] = strings.Replace(changedManifest[workflow.WorkflowFileName], "      ports: [{containerPort: 7780, protocol: TCP}]", "      command: [serve]\n      args: [--stdio]\n      env: [{name: MODE, value: interactive}]\n      ports: [{containerPort: 7780, protocol: TCP}]", 1)
	changed, err := workflow.CompileDefinition(changedManifest)
	if err != nil {
		t.Fatalf("compile changed interactive capability: %v", err)
	}
	if !reflect.DeepEqual(base.Function.DevValidationProfiles, changed.Function.DevValidationProfiles) || base.Function.DevValidationProvenanceHash != changed.Function.DevValidationProvenanceHash {
		t.Fatal("interactive sidecar runtime fields changed frozen validation authority")
	}

	selectedManifest := changedManifest
	selectedManifest[workflow.WorkflowFileName] = strings.Replace(selectedManifest[workflow.WorkflowFileName], "    prompt: do work", "    prompt: do work\n    capabilities: [validator]", 1)
	selected, err := workflow.CompileDefinition(selectedManifest)
	if err != nil {
		t.Fatalf("compile selected interactive capability: %v", err)
	}
	agent := selected.Function.Plan[0].Config.(*atc.AgentStep)
	if len(agent.Sidecars) != 1 || agent.Sidecars[0].Config == nil || !reflect.DeepEqual(agent.Sidecars[0].Config.Command, []string{"serve"}) || len(agent.Sidecars[0].Config.Env) != 1 {
		t.Fatalf("interactive reuse was not preserved: %+v", agent.Sidecars)
	}
	if !reflect.DeepEqual(changed.Function.DevValidationProfiles, selected.Function.DevValidationProfiles) || changed.Function.DevValidationProvenanceHash != selected.Function.DevValidationProvenanceHash {
		t.Fatal("interactive selection changed validation authority")
	}
	encoded, err := json.Marshal(selected.Function.DevValidationProfiles[0])
	if err != nil {
		t.Fatalf("marshal authority: %v", err)
	}
	for _, forbidden := range []string{"ports", "env", "args", "workingDir", "credentials"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("validation authority retained interactive field %q: %s", forbidden, encoded)
		}
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
		{name: "interpolated asset", mutate: func(manifest workflow.Manifest) {
			manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "validation/profile.yml", "((profile_file))", 1)
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

func TestParseCompiledRejectsUnknownNestedSidecarField(t *testing.T) {
	manifest := devValidationManifest("schema_version: 1\ncomponents: []\n")
	manifest[workflow.WorkflowFileName] = strings.Replace(manifest[workflow.WorkflowFileName], "    prompt: do work", "    prompt: do work\n    capabilities: [validator]", 1)
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mutated := strings.Replace(string(raw), `"image":"registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, `"image":"registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","unknown":true`, 1)
	if _, err := workflow.ParseCompiled([]byte(mutated)); err == nil {
		t.Fatal("ParseCompiled accepted unknown nested sidecar field")
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
