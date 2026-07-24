package legacyworkflow

import (
	"fmt"
	"strings"
	"testing"
)

const releasedDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDecodeManifestReleasedSchemas(t *testing.T) {
	t.Run("raw schema 1 metadata", func(t *testing.T) {
		metadata, signature, err := DecodeManifest(map[string]string{
			"workflow.yml": `schema_version: 1
name: migrated-v1
prompts: {work: work}
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
		})
		if err != nil {
			t.Fatalf("DecodeManifest: %v", err)
		}
		if metadata != (Metadata{Name: "migrated-v1", SchemaVersion: 1}) {
			t.Fatalf("metadata = %+v", metadata)
		}
		if signature != nil {
			t.Fatalf("signature = %+v, want nil", signature)
		}
	})

	t.Run("manifest-backed schema 2 resolves an asset", func(t *testing.T) {
		metadata, signature, err := DecodeManifest(map[string]string{
			"workflow.yml": `schema_version: 2
name: migrated-v2
prompt_files:
  work: prompts/work.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
			"prompts/work.md": "work from manifest",
		})
		if err != nil {
			t.Fatalf("DecodeManifest: %v", err)
		}
		if metadata != (Metadata{Name: "migrated-v2", SchemaVersion: 2}) {
			t.Fatalf("metadata = %+v", metadata)
		}
		if signature != nil {
			t.Fatalf("signature = %+v, want nil", signature)
		}
	})

	t.Run("schema 3 returns the ordered public signature", func(t *testing.T) {
		metadata, signature, err := DecodeManifest(map[string]string{
			"workflow.yml": releasedV3(`
inputs:
- {name: before, type: repository/v1}
- {name: after, type: repository/v1, optional: true}
outputs:
- {name: review, type: review/v1, from: result}
`),
		})
		if err != nil {
			t.Fatalf("DecodeManifest: %v", err)
		}
		if metadata != (Metadata{Name: "released-v3", SchemaVersion: 3, SignatureVersion: 7}) {
			t.Fatalf("metadata = %+v", metadata)
		}
		want := PublicSignature{
			SignatureVersion: 7,
			Inputs: []Port{
				{Name: "before", Type: "repository/v1"},
				{Name: "after", Type: "repository/v1", Optional: true},
			},
			Outputs: []Port{{Name: "review", Type: "review/v1"}},
		}
		if signature == nil || !signature.Equal(want) {
			t.Fatalf("signature = %+v, want %+v", signature, want)
		}
	})
}

func TestPublicSignatureIdentityIncludesOptionalityAndOrder(t *testing.T) {
	base := PublicSignature{
		SignatureVersion: 1,
		Inputs: []Port{
			{Name: "before", Type: "repository/v1"},
			{Name: "after", Type: "repository/v1"},
		},
		Outputs: []Port{{Name: "review", Type: "review/v1"}},
	}
	if !base.Equal(base) {
		t.Fatal("signature is not equal to itself")
	}

	optional := base
	optional.Inputs = append([]Port(nil), base.Inputs...)
	optional.Inputs[1].Optional = true
	if base.Equal(optional) {
		t.Fatal("different optionality compared equal")
	}

	reordered := base
	reordered.Inputs = []Port{base.Inputs[1], base.Inputs[0]}
	if base.Equal(reordered) {
		t.Fatal("different port order compared equal")
	}
}

func TestPublicSignatureExcludesDescriptionsAndOutputMappings(t *testing.T) {
	first, firstSignature, err := DecodeManifest(map[string]string{
		"workflow.yml": releasedV3(`
inputs:
- {name: repository, type: repository/v1, description: first input description}
outputs:
- {name: review, type: review/v1, description: first output description, from: first-result}
`),
	})
	if err != nil {
		t.Fatalf("first DecodeManifest: %v", err)
	}
	second, secondSignature, err := DecodeManifest(map[string]string{
		"workflow.yml": releasedV3(`
inputs:
- {name: repository, type: repository/v1, description: changed input description}
outputs:
- {name: review, type: review/v1, description: changed output description, from: changed-result}
`),
	})
	if err != nil {
		t.Fatalf("second DecodeManifest: %v", err)
	}
	if first != second {
		t.Fatalf("metadata differs: %+v != %+v", first, second)
	}
	if firstSignature == nil || secondSignature == nil || !firstSignature.Equal(*secondSignature) {
		t.Fatalf("description/from changed identity: %+v != %+v", firstSignature, secondSignature)
	}
}

func TestDecodeManifestRejectsMissingOrMalformedSource(t *testing.T) {
	tests := map[string]map[string]string{
		"missing workflow.yml": {
			"notes.txt": "nothing",
		},
		"malformed YAML": {
			"workflow.yml": "schema_version: [",
		},
		"missing schema-2 asset": {
			"workflow.yml": `schema_version: 2
name: missing-asset
prompt_files:
  work: prompts/missing.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
		},
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			if metadata, signature, err := DecodeManifest(manifest); err == nil {
				t.Fatalf("DecodeManifest = (%+v, %+v, nil), want error", metadata, signature)
			}
		})
	}
}

func TestDecodeManifestRejectsReleasedManifestViolations(t *testing.T) {
	valid := `schema_version: 1
name: valid
prompts: {work: work}
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`
	tooMany := map[string]string{"workflow.yml": valid}
	for index := 0; index < 512; index++ {
		tooMany["notes/"+strings.Repeat("x", index/26)+string(rune('a'+index%26))] = "x"
	}
	tooLargeTotal := map[string]string{"workflow.yml": valid}
	for index := 0; index < 11; index++ {
		tooLargeTotal["notes/"+string(rune('a'+index))] = strings.Repeat("x", 1<<20)
	}

	tests := map[string]map[string]string{
		"empty manifest": {},
		"unsafe path": {
			"workflow.yml": valid,
			"../notes.txt": "escape",
		},
		"invalid UTF-8": {
			"workflow.yml": valid,
			"notes.txt":    string([]byte{0xff}),
		},
		"file byte limit": {
			"workflow.yml": valid,
			"notes.txt":    strings.Repeat("x", (1<<20)+1),
		},
		"file count limit": tooMany,
		"total byte limit": tooLargeTotal,
	}
	for name, manifest := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodeManifest(manifest); err == nil {
				t.Fatal("DecodeManifest succeeded, want manifest validation error")
			}
		})
	}
}

func TestDecodeManifestPreservesReleasedSchema3Grammar(t *testing.T) {
	t.Run("accepts a no-port capability", func(t *testing.T) {
		source := `schema_version: 3
name: released-v3
signature_version: 7
inputs: []
outputs: []
capabilities:
  Release_Tools:
    contract: acme.tools/v1
    sidecar:
      name: dev
      image: registry.example/acme/tools@sha256:` + releasedDigest + `
plan:
- agent: work
  prompt: work
  capabilities: [Release_Tools]
`
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err != nil {
			t.Fatalf("DecodeManifest rejected released no-port capability shape: %v", err)
		}
	})

	t.Run("reads disposition_output naming a required public output", func(t *testing.T) {
		source := strings.Replace(
			releasedV3("\ninputs: []\noutputs:\n- {name: review, type: review/v1, from: result}\n"),
			"signature_version: 7",
			"signature_version: 7\ndisposition_output: review",
			1,
		)
		metadata, _, err := DecodeManifest(map[string]string{"workflow.yml": source})
		if err != nil {
			t.Fatalf("DecodeManifest rejected disposition_output naming a required output: %v", err)
		}
		if metadata.DispositionOutput != "review" {
			t.Fatalf("DispositionOutput = %q, want review", metadata.DispositionOutput)
		}
	})

	t.Run("rejects disposition_output naming an undeclared output", func(t *testing.T) {
		source := strings.Replace(
			releasedV3("\ninputs: []\noutputs: []\n"),
			"signature_version: 7",
			"signature_version: 7\ndisposition_output: review",
			1,
		)
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
			t.Fatal("DecodeManifest accepted disposition_output naming an undeclared output")
		}
	})

	t.Run("rejects disposition_output naming an optional output", func(t *testing.T) {
		source := strings.Replace(
			releasedV3("\ninputs: []\noutputs:\n- {name: review, type: review/v1, from: result, optional: true}\n"),
			"signature_version: 7",
			"signature_version: 7\ndisposition_output: review",
			1,
		)
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
			t.Fatal("DecodeManifest accepted disposition_output naming an optional output")
		}
	})

	t.Run("rejects post-release long-form plan output type", func(t *testing.T) {
		source := `schema_version: 3
name: released-v3
signature_version: 7
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  output_types:
    result: {type: review/v1, optional: true}
`
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
			t.Fatal("DecodeManifest accepted post-release long-form output_types")
		}
	})

	t.Run("rejects an invalid OCI repository with a valid digest", func(t *testing.T) {
		source := `schema_version: 3
name: released-v3
signature_version: 7
inputs: []
outputs: []
capabilities:
  tools:
    contract: acme.tools/v1
    sidecar:
      name: tools
      image: registry.example/Invalid_Name/tools@sha256:` + releasedDigest + `
plan:
- agent: work
  prompt: work
  capabilities: [tools]
`
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
			t.Fatal("DecodeManifest accepted an invalid OCI repository name")
		}
	})
}

func TestDecodeManifestPreservesReleasedSchema3SemanticValidation(t *testing.T) {
	tests := map[string]string{
		"unavailable public output": releasedV3(`
inputs: []
outputs:
- {name: review, type: review/v1, from: unavailable}
`),
		"duplicate function IDs": `schema_version: 3
name: duplicate-functions
signature_version: 1
inputs: []
outputs: []
plan:
- agent: first
  function_id: duplicate
  prompt: first
- agent: second
  function_id: duplicate
  prompt: second
`,
		"invalid ordinary task config": `schema_version: 3
name: invalid-task
signature_version: 1
inputs: []
outputs: []
plan:
- task: invalid
  function_id: invalid
  config:
    run: {path: /bin/true}
`,
		"unknown plan envelope field": `schema_version: 3
name: unknown-envelope
signature_version: 1
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  typo: true
`,
		"unknown nested gate field": `schema_version: 3
name: unknown-gate
signature_version: 1
inputs: []
outputs: []
plan:
- harvest: publish
  workspace: workspace
  repo: example/repo
  gate_policy:
    gates:
    - {gate: test, scope: full, typo: true}
`,
		"unknown nested rubric field": `schema_version: 3
name: unknown-rubric
signature_version: 1
inputs: []
outputs: []
plan:
- harvest: publish
  workspace: workspace
  repo: example/repo
  judge:
    rubric:
    - {name: correctness, weight: 1, guidance: good, typo: true}
    pass_threshold: 1
`,
	}

	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
				t.Fatal("DecodeManifest accepted source rejected by the released compiler")
			}
		})
	}
}

func TestDecodeManifestMatchesReleasedTypedOrdinaryDecode(t *testing.T) {
	tests := map[string]string{
		"agent outputs must be a string list": `schema_version: 3
name: typed-agent-outputs
signature_version: 1
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  outputs: not-a-list
`,
		"agent budget slice must be numeric": `schema_version: 3
name: typed-agent-budget
signature_version: 1
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  budget_slice_usd: not-a-number
`,
		"agent max turns must be an integer": `schema_version: 3
name: typed-agent-turns
signature_version: 1
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  max_turns: not-an-integer
`,
		"inline task sidecar must be valid": `schema_version: 3
name: invalid-task-sidecar
signature_version: 1
inputs: []
outputs: []
plan:
- task: work
  config:
    platform: linux
    run: {path: /bin/true}
  sidecars:
  - {}
`,
		"get passed must name an existing job": `schema_version: 3
name: invalid-passed-job
signature_version: 1
inputs: []
outputs: []
resources:
- name: repo
  type: mock
plan:
- get: repo
  passed: [does-not-exist]
`,
		"run message must be a legal identifier": `schema_version: 3
name: invalid-run-identifier
signature_version: 1
inputs: []
outputs: []
prototypes:
- name: messenger
  type: mock
  source: {}
plan:
- run: invalid/message
  type: messenger
`,
	}

	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
				t.Fatal("DecodeManifest accepted source rejected by release fca502000f")
			}
		})
	}

	t.Run("accepts released numeric agent fields", func(t *testing.T) {
		source := `schema_version: 3
name: typed-agent-numbers
signature_version: 1
inputs: []
outputs: []
plan:
- agent: work
  prompt: work
  outputs: [result]
  max_turns: 4
  budget_slice_usd: 1.25
`
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err != nil {
			t.Fatalf("DecodeManifest rejected release-valid numeric fields: %v", err)
		}
	})

	t.Run("retains released task sidecar duplicate behavior", func(t *testing.T) {
		source := `schema_version: 3
name: task-sidecars
signature_version: 1
inputs: []
outputs: []
plan:
- task: work
  config:
    platform: linux
    run: {path: /bin/true}
  sidecars:
  - {name: tools, image: registry.example/tools:latest}
  - {name: tools, image: registry.example/tools:latest}
`
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err != nil {
			t.Fatalf("DecodeManifest rejected release-valid task sidecars: %v", err)
		}
	})
}

func TestDecodeManifestMatchesFinalReleaseProbeMatrix(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		accepted bool
	}{
		{"baseline", "plan:\n- {agent: work, prompt: work}\n", true},
		{"in_parallel object without steps", "plan:\n- in_parallel: {}\n", true},
		{"in_parallel object with null steps", "plan:\n- in_parallel: {steps: null}\n", true},
		{"do null", "plan:\n- do: null\n", true},
		{"across entry without var", "plan:\n- across: [{values: [one]}]\n  agent: work\n  prompt: work\n", true},
		{"across null entry", "plan:\n- across: [null]\n  agent: work\n  prompt: work\n", true},
		{"across explicit null max in flight", "plan:\n- across: [{var: item, values: [one], max_in_flight: null}]\n  agent: work\n  prompt: work\n", true},
		{"duplicate across vars", "plan:\n- across:\n  - {var: item, values: [one]}\n  - {var: item, values: [two]}\n  agent: work\n  prompt: work\n", false},
		{"duplicate load vars", "plan:\n- do:\n  - {load_var: value, file: first.txt}\n  - {load_var: value, file: second.txt}\n", false},
		{"self passed dependency", "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo, passed: [entry]}\n", false},
		{"task null cache entry", "plan:\n- task: work\n  config:\n    platform: linux\n    caches: [null]\n", true},
		{"task null scratch entry", "plan:\n- task: work\n  config:\n    platform: linux\n    scratch_paths: [null]\n", true},
		{"task sidecar null env entry", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars:\n  - name: tools\n    image: image\n    env: [null]\n", true},
		{"task sidecar null port entry", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars:\n  - name: tools\n    image: image\n    ports: [null]\n", true},
		{"harvest null gate entry", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  dev_mcp: {name: dev, image: image}\n  gate_policy: {gates: [null]}\n", true},
		{"harvest null rubric entry", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  judge: {rubric: [null], pass_threshold: 1}\n", true},
		{"harvest empty dev mcp", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: {}\n", true},
		{"harvest null dev mcp", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: null\n", false},
		{"uppercase resource fields", "resources:\n- {Name: repo, Type: mock}\nplan:\n- {get: repo}\n", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := DecodeManifest(map[string]string{"workflow.yml": ordinaryV3(test.body)})
			if (err == nil) != test.accepted {
				t.Fatalf("DecodeManifest accepted=%t, want %t (err: %v)", err == nil, test.accepted, err)
			}
		})
	}
}

func TestDecodeManifestMatchesFinalRereviewHarvestPortSemantics(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		accepted bool
	}{
		{
			name:     "ordinary task sidecar rejects invalid protocol",
			body:     "plan:\n- task: work\n  config: {platform: linux}\n  sidecars:\n  - {name: tools, image: image, ports: [{containerPort: 8080, protocol: INVALID}]}\n",
			accepted: false,
		},
		{
			name:     "harvest dev mcp accepts invalid protocol without gates",
			body:     "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  dev_mcp: {ports: [{containerPort: 8080, protocol: INVALID}]}\n",
			accepted: true,
		},
		{
			name:     "harvest dev mcp accepts invalid protocol with gates",
			body:     "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: {ports: [{containerPort: 8080, protocol: INVALID}]}\n",
			accepted: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := DecodeManifest(map[string]string{"workflow.yml": ordinaryV3(test.body)})
			if (err == nil) != test.accepted {
				t.Fatalf("DecodeManifest accepted=%t, want %t (err: %v)", err == nil, test.accepted, err)
			}
		})
	}
}

func TestDecodeManifestMatchesAdjacentReleaseDecodeMatrix(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		accepted bool
	}{
		{"in parallel empty list", "plan:\n- in_parallel: []\n", true},
		{"in parallel null", "plan:\n- in_parallel: null\n", false},
		{"in parallel null element", "plan:\n- in_parallel: [null]\n", false},
		{"do empty list", "plan:\n- do: []\n", true},
		{"do null element", "plan:\n- do: [null]\n", false},
		{"across null list", "plan:\n- across: null\n  agent: work\n  prompt: work\n", false},
		{"across empty list", "plan:\n- across: []\n  agent: work\n  prompt: work\n", false},
		{"across two null entries", "plan:\n- across: [null, null]\n  agent: work\n  prompt: work\n", false},
		{"nested across shadow", "plan:\n- across: [{var: item, values: [one]}]\n  do:\n  - across: [{var: item, values: [two]}]\n    agent: work\n    prompt: work\n", true},
		{"sibling across same var", "plan:\n- do:\n  - across: [{var: item, values: [one]}]\n    agent: first\n    prompt: first\n  - across: [{var: item, values: [two]}]\n    agent: second\n    prompt: second\n", true},
		{"duplicate load vars parallel", "plan:\n- in_parallel:\n  - {load_var: value, file: first.txt}\n  - {load_var: value, file: second.txt}\n", false},
		{"passed wildcard self cycle", "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo, passed: ['*']}\n", false},
		{"passed null", "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo, passed: null}\n", true},
		{"task config null", "plan:\n- {task: work, config: null}\n", false},
		{"task pointer fields null", "plan:\n- task: work\n  config: {platform: linux, image_resource: null, container_limits: null, container_requests: null}\n  container_limits: null\n  container_requests: null\n", true},
		{"task object lists null", "plan:\n- task: work\n  config: {platform: linux, inputs: null, outputs: null, caches: null, scratch_paths: null}\n", true},
		{"task sidecars null", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: null\n", true},
		{"task null sidecar entry", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: [null]\n", true},
		{"task empty sidecar entry", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: [{}]\n", false},
		{"agent sidecars null", "plan:\n- agent: work\n  prompt: work\n  sidecars: null\n", true},
		{"task sidecar object lists null", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: [{name: tools, image: image, env: null, ports: null}]\n", true},
		{"harvest gate list null", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: null}\n", true},
		{"harvest rubric list null", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  judge: {rubric: null, pass_threshold: 1}\n", true},
		{"declaration lists null", "resources: null\nresource_types: null\nprototypes: null\nvar_sources: null\nplan:\n- {agent: work, prompt: work}\n", true},
		{"harvest dev mcp empty string", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: ''\n", true},
		{"harvest dev mcp bool", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: true\n", false},
		{"harvest dev mcp wrong command", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n  dev_mcp: {command: run}\n", false},
		{"mixed case resource fields", "resources:\n- {nAmE: repo, tYpE: mock}\nplan:\n- {get: repo}\n", true},
		{"mixed case resource type fields", "resource_types:\n- {nAmE: custom, iMaGe: image}\nplan:\n- {agent: work, prompt: work}\n", true},
		{"mixed case prototype fields", "prototypes:\n- {nAmE: runner, tYpE: mock}\nplan:\n- {run: invoke, type: runner}\n", true},
		{"null resource declaration", "resources: [null]\nplan:\n- {agent: work, prompt: work}\n", false},
		{"task null input entry", "plan:\n- task: work\n  config: {platform: linux, inputs: [null]}\n", false},
		{"task null output entry", "plan:\n- task: work\n  config: {platform: linux, outputs: [null]}\n", false},
		{"across wrapped duplicate load var", "plan:\n- across: [{var: item, values: [one]}]\n  load_var: item\n  file: value.txt\n", false},
		{"across hook same load var", "plan:\n- across: [{var: item, values: [one]}]\n  agent: work\n  prompt: work\n  on_success: {load_var: item, file: value.txt}\n", true},
		{"outer load var nested across shadow", "plan:\n- do:\n  - {load_var: item, file: value.txt}\n  - across: [{var: item, values: [one]}]\n    agent: work\n    prompt: work\n", true},
		{"outer load var nested do duplicate", "plan:\n- do:\n  - {load_var: item, file: first.txt}\n  - do:\n    - {load_var: item, file: second.txt}\n", false},
		{"self cycle in hook", "resources:\n- {name: repo, type: mock}\nplan:\n- agent: work\n  prompt: work\n  on_success: {get: repo, passed: [entry]}\n", false},
		{"case collision exact name wins", "resources:\n- {Name: other, name: repo, Type: mock}\nplan:\n- {get: repo}\n", true},
		{"uppercase step core", "plan:\n- {Agent: work, Prompt: work}\n", false},
		{"uppercase task config field", "plan:\n- task: work\n  config: {Platform: linux}\n", false},
		{"uppercase sidecar field", "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: [{Name: tools, Image: image}]\n", false},
		{"uppercase gate field", "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {Gates: []}\n", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := DecodeManifest(map[string]string{"workflow.yml": ordinaryV3(test.body)})
			if (err == nil) != test.accepted {
				t.Fatalf("DecodeManifest accepted=%t, want %t (err: %v)", err == nil, test.accepted, err)
			}
		})
	}
}

func TestFrozenOrdinaryTypeSchemaCoversEveryReleasedField(t *testing.T) {
	assertFrozenShapeTable(t, "declarations", releasedOrdinaryDeclarationShapes, frozenOrdinaryDeclarationTypeFields)
	assertFrozenShapeTable(t, "steps", releasedOrdinaryStepShapes, frozenOrdinaryStepTypeFields)
	assertFrozenShapeFields(t, "wrappers", releasedOrdinaryWrapperShapes, frozenOrdinaryWrapperTypeFields)

	for section, fields := range releasedOrdinaryDeclarationShapes {
		for field, shape := range fields {
			assertFrozenShapeBoundary(t, section+"."+field, shape, frozenOrdinaryDeclarationTypeFields[section][field])
		}
	}
	for core, fields := range releasedOrdinaryStepShapes {
		for field, shape := range fields {
			assertFrozenShapeBoundary(t, core+"."+field, shape, frozenOrdinaryStepTypeFields[core][field])
		}
	}
	for field, shape := range releasedOrdinaryWrapperShapes {
		validator := frozenOrdinaryWrapperTypeFields[field]
		if field == "fail_fast" {
			validator = frozenBoolType
		}
		assertFrozenShapeBoundary(t, "wrapper."+field, shape, validator)
	}
}

type releasedWireShape string

const (
	releasedAny            releasedWireShape = "any"
	releasedString         releasedWireShape = "string"
	releasedBool           releasedWireShape = "boolean"
	releasedInt            releasedWireShape = "integer"
	releasedNumber         releasedWireShape = "number"
	releasedObject         releasedWireShape = "object"
	releasedStringList     releasedWireShape = "string-list"
	releasedStringMap      releasedWireShape = "string-map"
	releasedCheckEvery     releasedWireShape = "check-every"
	releasedVersion        releasedWireShape = "version"
	releasedPutInputs      releasedWireShape = "put-inputs"
	releasedLimits         releasedWireShape = "container-limits"
	releasedSnapshotInputs releasedWireShape = "snapshot-input-map"
	releasedSnapshotOutput releasedWireShape = "snapshot-output-map"
	releasedTaskConfig     releasedWireShape = "task-config"
	releasedSidecar        releasedWireShape = "sidecar"
	releasedSidecarList    releasedWireShape = "sidecar-list"
	releasedHarvestDevMCP  releasedWireShape = "harvest-dev-mcp"
	releasedGatePolicy     releasedWireShape = "gate-policy"
	releasedJudge          releasedWireShape = "judge"
	releasedStepObject     releasedWireShape = "step-object"
	releasedStepList       releasedWireShape = "step-list"
	releasedInParallel     releasedWireShape = "in-parallel"
	releasedAcross         releasedWireShape = "across"
)

var releasedOrdinaryDeclarationShapes = map[string]map[string]releasedWireShape{
	"resources": {
		"name": releasedString, "old_name": releasedString, "public": releasedBool,
		"webhook_token": releasedString, "type": releasedString, "source": releasedObject,
		"check_every": releasedCheckEvery, "check_timeout": releasedString,
		"tags": releasedStringList, "version": releasedStringMap, "icon": releasedString,
		"expose_build_created_by": releasedBool,
	},
	"resource_types": {
		"name": releasedString, "type": releasedString, "image": releasedString,
		"source": releasedObject, "defaults": releasedObject, "privileged": releasedBool,
		"check_every": releasedCheckEvery, "tags": releasedStringList, "params": releasedObject,
	},
	"prototypes": {
		"name": releasedString, "type": releasedString, "source": releasedObject,
		"defaults": releasedObject, "privileged": releasedBool, "check_every": releasedCheckEvery,
		"tags": releasedStringList, "params": releasedObject,
	},
	"var_sources": {"name": releasedString, "type": releasedString, "config": releasedAny},
}

var releasedOrdinaryStepShapes = map[string]map[string]releasedWireShape{
	"agent": {
		"agent": releasedString, "function_id": releasedString, "prompt": releasedString,
		"prompt_file": releasedString, "system_prompt_file": releasedString,
		"context_files": releasedStringList, "model": releasedString, "max_turns": releasedInt,
		"budget_slice_usd": releasedNumber, "output_schema": releasedString,
		"system_prompt": releasedString, "context": releasedString, "skills": releasedStringList,
		"sidecars": releasedSidecarList, "inputs": releasedStringList, "outputs": releasedStringList,
		"capabilities": releasedStringList, "input_types": releasedSnapshotInputs,
		"output_types": releasedSnapshotOutput, "env": releasedObject, "timeout": releasedString,
		"container_limits": releasedLimits, "container_requests": releasedLimits,
	},
	"harvest": {
		"harvest": releasedString, "workspace": releasedString, "repo": releasedString,
		"target_branch": releasedString, "ticket_id": releasedInt, "pipeline_run_id": releasedInt,
		"branch": releasedString, "push": releasedBool, "env": releasedObject,
		"dev_mcp": releasedHarvestDevMCP, "gate_policy": releasedGatePolicy,
		"judge": releasedJudge, "timeout": releasedString,
	},
	"run": {
		"run": releasedString, "type": releasedString, "params": releasedObject,
		"privileged": releasedBool, "tags": releasedStringList, "container_limits": releasedLimits,
		"container_requests": releasedLimits, "timeout": releasedString,
	},
	"task": {
		"task": releasedString, "function_id": releasedString, "privileged": releasedBool,
		"hermetic": releasedBool, "file": releasedString, "container_limits": releasedLimits,
		"container_requests": releasedLimits, "config": releasedTaskConfig,
		"params": releasedObject, "vars": releasedObject, "tags": releasedStringList,
		"input_mapping": releasedStringMap, "output_mapping": releasedStringMap,
		"input_types": releasedSnapshotInputs, "output_types": releasedSnapshotOutput,
		"image": releasedString, "timeout": releasedString, "sidecars": releasedSidecarList,
	},
	"put": {
		"put": releasedString, "resource": releasedString, "params": releasedObject,
		"inputs": releasedPutInputs, "tags": releasedStringList, "get_params": releasedObject,
		"timeout": releasedString, "no_get": releasedBool,
	},
	"get": {
		"get": releasedString, "resource": releasedString, "version": releasedVersion,
		"params": releasedObject, "passed": releasedStringList, "trigger": releasedBool,
		"tags": releasedStringList, "timeout": releasedString, "skip_download": releasedBool,
	},
	"set_pipeline": {
		"set_pipeline": releasedString, "file": releasedString, "team": releasedString,
		"vars": releasedObject, "var_files": releasedStringList, "instance_vars": releasedObject,
	},
	"load_var": {
		"load_var": releasedString, "file": releasedString,
		"format": releasedString, "reveal": releasedBool,
	},
	"try":         {"try": releasedStepObject},
	"do":          {"do": releasedStepList},
	"in_parallel": {"in_parallel": releasedInParallel},
}

var releasedOrdinaryWrapperShapes = map[string]releasedWireShape{
	"ensure": releasedStepObject, "on_error": releasedStepObject,
	"on_abort": releasedStepObject, "on_failure": releasedStepObject,
	"on_success": releasedStepObject, "across": releasedAcross,
	"attempts": releasedInt, "timeout": releasedString, "fail_fast": releasedBool,
}

func assertFrozenShapeTable(
	t *testing.T,
	group string,
	released map[string]map[string]releasedWireShape,
	frozen map[string]map[string]frozenTypeValidator,
) {
	t.Helper()
	for kind, fields := range released {
		for field := range fields {
			if _, found := frozen[kind][field]; !found {
				t.Errorf("%s %s field %q has no frozen type boundary", group, kind, field)
			}
		}
	}
	for kind, fields := range frozen {
		for field := range fields {
			if _, found := released[kind][field]; !found {
				t.Errorf("frozen %s %s field %q is absent from the released grammar", group, kind, field)
			}
		}
	}
}

func assertFrozenShapeFields(
	t *testing.T,
	group string,
	released map[string]releasedWireShape,
	frozen map[string]frozenTypeValidator,
) {
	t.Helper()
	for field := range released {
		if field == "fail_fast" {
			continue
		}
		if _, found := frozen[field]; !found {
			t.Errorf("%s field %q has no frozen type boundary", group, field)
		}
	}
	for field := range frozen {
		if _, found := released[field]; !found {
			t.Errorf("frozen %s field %q is absent from the released grammar", group, field)
		}
	}
}

func assertFrozenShapeBoundary(t *testing.T, field string, shape releasedWireShape, validator frozenTypeValidator) {
	t.Helper()
	type sample struct {
		name  string
		shape releasedWireShape
		value any
	}
	samples := []sample{
		{name: "string", shape: releasedString, value: "1m"},
		{name: "integer", shape: releasedInt, value: 2},
		{name: "number", shape: releasedNumber, value: 1.5},
		{name: "boolean", shape: releasedBool, value: true},
		{name: "object", shape: releasedObject, value: map[string]any{}},
		{name: "list", shape: releasedStepList, value: []any{}},
	}
	for _, candidate := range samples {
		if releasedShapesOverlap(shape, candidate.shape) {
			continue
		}
		if err := validator(candidate.value, field); err == nil {
			t.Errorf("%s (%s) accepted incompatible %s", field, shape, candidate.name)
		}
	}
	for name, valid := range releasedValidShapeValues(shape) {
		if err := validator(valid, field); err != nil {
			t.Errorf("%s (%s) rejected release-valid %s: %v", field, shape, name, err)
		}
	}
	for name, invalid := range releasedInvalidShapeValues(shape) {
		if err := validator(invalid, field); err == nil {
			t.Errorf("%s (%s) accepted release-invalid %s", field, shape, name)
		}
	}
}

func releasedShapesOverlap(want, candidate releasedWireShape) bool {
	if want == releasedAny {
		return true
	}
	switch candidate {
	case releasedString:
		return want == releasedString || want == releasedCheckEvery ||
			want == releasedVersion || want == releasedPutInputs ||
			want == releasedSidecar || want == releasedHarvestDevMCP
	case releasedInt:
		return want == releasedInt || want == releasedNumber
	case releasedNumber:
		return want == releasedNumber
	case releasedBool:
		return want == releasedBool
	case releasedObject:
		switch want {
		case releasedObject, releasedStringMap, releasedVersion, releasedLimits,
			releasedSnapshotInputs, releasedSnapshotOutput, releasedTaskConfig,
			releasedSidecar, releasedGatePolicy, releasedJudge, releasedStepObject,
			releasedInParallel, releasedHarvestDevMCP:
			return true
		}
	case releasedStepList:
		switch want {
		case releasedStringList, releasedPutInputs, releasedSidecarList,
			releasedStepList, releasedInParallel, releasedAcross:
			return true
		}
	}
	return false
}

func releasedValidShapeValues(shape releasedWireShape) map[string]any {
	sidecar := map[string]any{"name": "tools", "image": "registry.example/tools:latest"}
	switch shape {
	case releasedAny:
		return map[string]any{
			"string": "value", "number": 1.5, "boolean": true,
			"object": map[string]any{"nested": true}, "list": []any{true}, "null": nil,
		}
	case releasedString:
		return map[string]any{"string": "value", "null": nil}
	case releasedBool:
		return map[string]any{"boolean": true, "null": nil}
	case releasedInt:
		return map[string]any{"integer": 2, "whole-number": float64(2), "null": nil}
	case releasedNumber:
		return map[string]any{"integer": 2, "fraction": 1.5, "null": nil}
	case releasedObject:
		return map[string]any{"empty-object": map[string]any{}, "nested-object": map[string]any{"nested": true}, "null": nil}
	case releasedStringList:
		return map[string]any{"empty-list": []any{}, "string-list": []any{"value"}, "null-element": []any{nil}, "null": nil}
	case releasedStringMap:
		return map[string]any{"empty-map": map[string]any{}, "string-map": map[string]any{"key": "value"}, "null-value": map[string]any{"key": nil}, "null": nil}
	case releasedCheckEvery:
		return map[string]any{"duration": "1m", "never": "never", "empty": "", "null": nil}
	case releasedVersion:
		return map[string]any{"latest": "latest", "every": "every", "pinned": map[string]any{"ref": "v1"}, "null": nil}
	case releasedPutInputs:
		return map[string]any{"all": "all", "detect": "detect", "specified": []any{"artifact"}, "null": nil}
	case releasedLimits:
		return map[string]any{"empty": map[string]any{}, "quantities": map[string]any{"cpu": 1, "memory": "1G", "ephemeral_storage": 1024}, "null": nil}
	case releasedSnapshotInputs:
		return map[string]any{"empty": map[string]any{}, "typed": map[string]any{"input": map[string]any{"type": "review/v1", "optional": true}}, "null": nil}
	case releasedSnapshotOutput:
		return map[string]any{"empty": map[string]any{}, "typed": map[string]any{"output": "review/v1"}, "null": nil}
	case releasedTaskConfig:
		return map[string]any{
			"empty": map[string]any{},
			"typed": map[string]any{
				"platform": "linux", "run": map[string]any{"path": "/bin/true"},
			},
			"null-path-entries": map[string]any{
				"platform": "linux", "caches": []any{nil}, "scratch_paths": []any{nil},
			},
			"null": nil,
		}
	case releasedSidecar:
		return map[string]any{
			"file": "sidecar.yml", "inline": sidecar,
			"inline-null-entries": map[string]any{
				"name": "tools", "image": "registry.example/tools:latest",
				"env": []any{nil}, "ports": []any{nil},
			},
			"null": nil,
		}
	case releasedSidecarList:
		return map[string]any{
			"empty": []any{}, "files": []any{"sidecar.yml"}, "inline": []any{sidecar},
			"null-entry": []any{nil}, "null": nil,
		}
	case releasedHarvestDevMCP:
		return map[string]any{
			"file": "sidecar.yml", "empty-file": "",
			"empty-object": map[string]any{}, "inline-object": sidecar, "null": nil,
		}
	case releasedGatePolicy:
		return map[string]any{
			"empty": map[string]any{}, "typed": map[string]any{"gates": []any{}},
			"null-gate-entry": map[string]any{"gates": []any{nil}}, "null": nil,
		}
	case releasedJudge:
		return map[string]any{
			"empty":             map[string]any{},
			"typed":             map[string]any{"rubric": []any{}, "pass_threshold": 1.5},
			"null-rubric-entry": map[string]any{"rubric": []any{nil}},
			"null":              nil,
		}
	case releasedStepObject:
		return map[string]any{"object": map[string]any{}, "null": nil}
	case releasedStepList:
		return map[string]any{"list": []any{}, "null": nil}
	case releasedInParallel:
		return map[string]any{
			"list": []any{}, "config": map[string]any{"steps": []any{}, "limit": 1, "fail_fast": true},
			"config-without-steps": map[string]any{},
			"config-null-steps":    map[string]any{"steps": nil},
			"null":                 nil,
		}
	case releasedAcross:
		return map[string]any{
			"empty": []any{},
			"typed": []any{
				map[string]any{"var": "item", "values": []any{}, "max_in_flight": "all"},
			},
			"missing-var": []any{map[string]any{"values": []any{"one"}}},
			"null-entry":  []any{nil},
			"null-max":    []any{map[string]any{"var": "item", "max_in_flight": nil}},
			"null-list":   nil,
		}
	default:
		return nil
	}
}

func releasedInvalidShapeValues(shape releasedWireShape) map[string]any {
	switch shape {
	case releasedStringList:
		return map[string]any{"boolean-element": []any{true}, "object-element": []any{map[string]any{}}}
	case releasedStringMap:
		return map[string]any{"boolean-value": map[string]any{"key": true}, "list-value": map[string]any{"key": []any{}}}
	case releasedCheckEvery:
		return map[string]any{"invalid-duration": "not-a-duration"}
	case releasedVersion:
		return map[string]any{"non-string-pinned-value": map[string]any{"ref": true}}
	case releasedPutInputs:
		return map[string]any{"non-string-input": []any{true}}
	case releasedLimits:
		return map[string]any{
			"string-cpu":                map[string]any{"cpu": "1"},
			"invalid-memory":            map[string]any{"memory": "not-a-quantity"},
			"invalid-ephemeral-storage": map[string]any{"ephemeral_storage": "not-a-quantity"},
		}
	case releasedSnapshotInputs:
		return map[string]any{
			"scalar-entry":   map[string]any{"input": "review/v1"},
			"wrong-optional": map[string]any{"input": map[string]any{"type": "review/v1", "optional": "yes"}},
		}
	case releasedSnapshotOutput:
		return map[string]any{"object-entry": map[string]any{"output": map[string]any{"type": "review/v1"}}}
	case releasedTaskConfig:
		return map[string]any{
			"wrong-platform": map[string]any{"platform": true},
			"wrong-run":      map[string]any{"run": "run.sh"},
			"wrong-input":    map[string]any{"inputs": []any{true}},
		}
	case releasedSidecar:
		return map[string]any{
			"invalid-inline": map[string]any{},
			"wrong-command":  map[string]any{"name": "tools", "image": "image", "command": "run"},
		}
	case releasedHarvestDevMCP:
		return map[string]any{
			"boolean":       true,
			"number":        1,
			"list":          []any{},
			"wrong-command": map[string]any{"command": "run"},
		}
	case releasedSidecarList:
		return map[string]any{"non-union-entry": []any{true}, "invalid-inline": []any{map[string]any{}}}
	case releasedGatePolicy:
		return map[string]any{
			"wrong-gates":   map[string]any{"gates": "test"},
			"wrong-retries": map[string]any{"gates": []any{map[string]any{"retries": "two"}}},
		}
	case releasedJudge:
		return map[string]any{
			"wrong-threshold": map[string]any{"pass_threshold": "ten"},
			"wrong-weight":    map[string]any{"rubric": []any{map[string]any{"weight": "one"}}},
		}
	case releasedStepList:
		return map[string]any{"non-step-entry": []any{true}}
	case releasedInParallel:
		return map[string]any{
			"wrong-steps":    map[string]any{"steps": "work"},
			"non-step-entry": []any{true},
		}
	case releasedAcross:
		return map[string]any{
			"non-object-entry": []any{true},
			"wrong-var":        []any{map[string]any{"var": true}},
			"wrong-limit":      []any{map[string]any{"var": "item", "max_in_flight": true}},
		}
	default:
		return nil
	}
}

func TestFrozenOrdinarySemanticSchemaMatchesReleasedValidator(t *testing.T) {
	tests := map[string]struct {
		body string
		want string
	}{
		"resource requires name": {
			body: "resources:\n- {type: mock}\n" + validAgentPlan(),
			want: "has no name",
		},
		"resource requires type": {
			body: "resources:\n- {name: repo}\nplan:\n- {get: repo}\n",
			want: "has no type",
		},
		"resource names are unique": {
			body: "resources:\n- {name: repo, type: mock}\n- {name: repo, type: mock}\nplan:\n- {get: repo}\n",
			want: "duplicate resource",
		},
		"resources must be used": {
			body: "resources:\n- {name: repo, type: mock}\n" + validAgentPlan(),
			want: "is not used",
		},
		"resource type requires a backing type": {
			body: "resource_types:\n- {name: custom}\n" + validAgentPlan(),
			want: "has no type",
		},
		"resource type cannot have image and type": {
			body: "resource_types:\n- {name: custom, type: mock, image: image}\n" + validAgentPlan(),
			want: "cannot specify both",
		},
		"resource type names are unique": {
			body: "resource_types:\n- {name: custom, type: mock}\n- {name: custom, type: mock}\n" + validAgentPlan(),
			want: "duplicate resource type",
		},
		"prototype requires a type": {
			body: "prototypes:\n- {name: message, source: {}}\n" + validAgentPlan(),
			want: "has no type",
		},
		"prototype and resource type names cannot collide": {
			body: "resource_types:\n- {name: custom, type: mock}\nprototypes:\n- {name: custom, type: mock, source: {}}\n" + validAgentPlan(),
			want: "same name",
		},
		"variable source requires name": {
			body: "var_sources:\n- {type: dummy, config: {}}\n" + validAgentPlan(),
			want: "has no name",
		},
		"variable source names are unique": {
			body: "var_sources:\n- {name: vars, type: dummy, config: {vars: {}}}\n- {name: vars, type: dummy, config: {vars: {}}}\n" + validAgentPlan(),
			want: "duplicate variable source",
		},
		"variable source type is supported": {
			body: "var_sources:\n- {name: vars, type: unknown, config: {}}\n" + validAgentPlan(),
			want: "unknown credential manager type",
		},
		"dummy variable source config is validated": {
			body: "var_sources:\n- {name: vars, type: dummy, config: {}}\n" + validAgentPlan(),
			want: "invalid vars config",
		},
		"variable source dependencies must resolve": {
			body: "var_sources:\n- {name: vars, type: dummy, config: {vars: {token: '((missing:token))'}}}\n" + validAgentPlan(),
			want: "could not resolve inter-dependent var sources",
		},
		"task requires file or config": {
			body: "plan:\n- {task: work}\n",
			want: "must specify either",
		},
		"task file and config are exclusive": {
			body: "plan:\n- task: work\n  file: task.yml\n  config: {platform: linux}\n",
			want: "must specify one",
		},
		"inline task requires platform": {
			body: "plan:\n- task: work\n  config: {run: {path: /bin/true}}\n",
			want: "missing 'platform'",
		},
		"task input requires name": {
			body: "plan:\n- task: work\n  config: {platform: linux, inputs: [{}]}\n",
			want: "input",
		},
		"task output requires name": {
			body: "plan:\n- task: work\n  config: {platform: linux, outputs: [{}]}\n",
			want: "output",
		},
		"task typed input must name an effective input": {
			body: "plan:\n- agent: producer\n  function_id: producer\n  prompt: produce\n  outputs: [other]\n  output_types: {other: review/v1}\n- task: work\n  function_id: work\n  config: {platform: linux, inputs: [{name: input}]}\n  input_types: {other: {type: review/v1}}\n",
			want: "does not name an effective task input",
		},
		"task typed output must name an effective output": {
			body: "plan:\n- task: work\n  function_id: work\n  config: {platform: linux, outputs: [{name: output}]}\n  output_types: {other: review/v1}\n",
			want: "does not name an effective task output",
		},
		"task effective typed outputs are unique": {
			body: "plan:\n- task: work\n  function_id: work\n  config: {platform: linux, outputs: [{name: first}, {name: second}]}\n  output_mapping: {first: result, second: result}\n  output_types: {result: review/v1}\n",
			want: "duplicate effective task output",
		},
		"task sidecar names are not reserved": {
			body: "plan:\n- task: work\n  config: {platform: linux}\n  sidecars: [{name: main, image: image}]\n",
			want: "reserved container name",
		},
		"get requires a known resource": {
			body: "plan:\n- {get: repo}\n",
			want: "unknown resource",
		},
		"get names are unique within the job": {
			body: "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo}\n- {get: repo}\n",
			want: "repeated name",
		},
		"get skip download requires image resource": {
			body: "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo, skip_download: true}\n",
			want: "skip_download",
		},
		"get passed requires a matching job": {
			body: "resources:\n- {name: repo, type: mock}\nplan:\n- {get: repo, passed: [missing]}\n",
			want: "no matching job",
		},
		"put requires a known resource": {
			body: "plan:\n- {put: repo}\n",
			want: "unknown resource",
		},
		"run requires a known prototype": {
			body: "plan:\n- {run: message, type: missing}\n",
			want: "unknown prototype",
		},
		"agent requires a prompt source": {
			body: "plan:\n- {agent: work}\n",
			want: "prompt is required",
		},
		"agent prompt sources are exclusive": {
			body: "plan:\n- {agent: work, prompt: work, prompt_file: prompt.md}\n",
			want: "mutually exclusive",
		},
		"agent budget cannot be negative": {
			body: "plan:\n- {agent: work, prompt: work, budget_slice_usd: -1}\n",
			want: "budget_slice_usd",
		},
		"agent turns cannot be negative": {
			body: "plan:\n- {agent: work, prompt: work, max_turns: -1}\n",
			want: "max_turns",
		},
		"agent typed input must name a declared input": {
			body: "plan:\n- agent: producer\n  function_id: producer\n  prompt: produce\n  outputs: [other]\n  output_types: {other: review/v1}\n- agent: work\n  function_id: work\n  prompt: work\n  inputs: [input]\n  input_types: {other: {type: review/v1}}\n",
			want: "does not name a declared agent input",
		},
		"agent typed output must name a declared output": {
			body: "plan:\n- agent: work\n  function_id: work\n  prompt: work\n  outputs: [output]\n  output_types: {other: review/v1}\n",
			want: "does not name a declared agent output",
		},
		"agent output cannot be flight": {
			body: "plan:\n- {agent: work, prompt: work, outputs: [flight]}\n",
			want: "reserved for the flight recorder",
		},
		"agent output must be env safe": {
			body: "plan:\n- {agent: work, prompt: work, outputs: ['bad=value']}\n",
			want: "must contain only",
		},
		"agent output cannot collide with schema env": {
			body: "plan:\n- {agent: work, prompt: work, outputs: [schema]}\n",
			want: "reserved AGENT_OUTPUT_SCHEMA",
		},
		"agent output env names cannot collide": {
			body: "plan:\n- {agent: work, prompt: work, outputs: [a-b, a_b]}\n",
			want: "collide after",
		},
		"agent env is static only": {
			body: "plan:\n- agent: work\n  prompt: work\n  env: {TOKEN: '((vault:token))'}\n",
			want: "static-only",
		},
		"direct agent sidecars remain forbidden by source grammar": {
			body: "plan:\n- agent: work\n  prompt: work\n  sidecars:\n  - {name: tools, image: image}\n  - {name: tools, image: image}\n",
			want: "direct sidecars are not allowed",
		},
		"harvest requires workspace": {
			body: "plan:\n- {harvest: publish, repo: owner/repo}\n",
			want: "workspace",
		},
		"harvest requires repo": {
			body: "plan:\n- {harvest: publish, workspace: workspace}\n",
			want: "repo",
		},
		"harvest push requires branch": {
			body: "plan:\n- {harvest: publish, workspace: workspace, repo: owner/repo, push: true}\n",
			want: "requires `branch:`",
		},
		"harvest gates require dev mcp": {
			body: "plan:\n- harvest: publish\n  workspace: workspace\n  repo: owner/repo\n  gate_policy: {gates: [{gate: test, scope: full}]}\n",
			want: "require `dev_mcp:`",
		},
		"set pipeline requires file": {
			body: "plan:\n- {set_pipeline: child}\n",
			want: "no file specified",
		},
		"load var requires file": {
			body: "plan:\n- {load_var: value}\n",
			want: "no file specified",
		},
		"across requires vars": {
			body: "plan:\n- across: []\n  agent: work\n  prompt: work\n",
			want: "no vars specified",
		},
		"across limit must be positive": {
			body: "plan:\n- across: [{var: item, values: [one], max_in_flight: 0}]\n  agent: work\n  prompt: work\n",
			want: "must be greater than 0",
		},
		"attempts must be positive": {
			body: "plan:\n- attempts: 0\n  agent: work\n  prompt: work\n",
			want: "must be greater than 0",
		},
		"wrapper timeout must be a duration": {
			body: "plan:\n- timeout: not-a-duration\n  do:\n  - {agent: work, prompt: work}\n",
			want: "invalid duration",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := DecodeManifest(map[string]string{"workflow.yml": ordinaryV3(test.body)})
			if err == nil {
				t.Fatal("DecodeManifest accepted source rejected by release fca502000f")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeManifest error %q does not contain %q", err, test.want)
			}
		})
	}
}

func ordinaryV3(body string) string {
	return "schema_version: 3\nname: ordinary-validation\nsignature_version: 1\ninputs: []\noutputs: []\n" + body
}

func validAgentPlan() string {
	return "plan:\n- {agent: work, prompt: work}\n"
}

func TestFrozenOrdinarySemanticSchemaRetainsReleasedAcceptedEdges(t *testing.T) {
	tests := map[string]string{
		"custom image resource permits skip download":   "resource_types:\n- {name: custom-image, image: image}\nresources:\n- {name: repo, type: custom-image}\nplan:\n- {get: repo, skip_download: true}\n",
		"bare agent env references remain static":       "plan:\n- agent: work\n  prompt: work\n  env: {RUN_ID: '((run_id))'}\n",
		"forward variable source dependencies resolve":  "var_sources:\n- {name: first, type: dummy, config: {vars: {token: '((second:token))'}}}\n- {name: second, type: dummy, config: {vars: {token: value}}}\n" + validAgentPlan(),
		"released memory unmarshal default is retained": "plan:\n- agent: work\n  prompt: work\n  container_limits: {memory: true, ephemeral_storage: {}}\n",
		"identifier warnings do not become errors":      "plan:\n- {agent: _work, prompt: work}\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodeManifest(map[string]string{"workflow.yml": ordinaryV3(body)}); err != nil {
				t.Fatalf("DecodeManifest rejected source accepted by release fca502000f: %v", err)
			}
		})
	}
}

func TestDecodeManifestPreservesReleasedCompiledAssetLimits(t *testing.T) {
	t.Run("counts repeated prompt expansion per node", func(t *testing.T) {
		prompt := strings.Repeat("p", 1<<20)
		atBoundary := map[string]string{
			"workflow.yml":    repeatedPromptFunction(10),
			"prompts/work.md": prompt,
		}
		if _, _, err := DecodeManifest(atBoundary); err != nil {
			t.Fatalf("DecodeManifest rejected exact compiled-asset boundary: %v", err)
		}

		aboveBoundary := map[string]string{
			"workflow.yml":    repeatedPromptFunction(11),
			"prompts/work.md": prompt,
		}
		if _, _, err := DecodeManifest(aboveBoundary); err == nil {
			t.Fatal("DecodeManifest accepted repeated expanded assets above 10 MiB")
		}
	})

	t.Run("counts the selected skill tree union once", func(t *testing.T) {
		source := `schema_version: 3
name: skill-union
signature_version: 1
inputs: []
outputs: []
plan:
- agent: first
  prompt: first
  skills: [testing]
- agent: second
  prompt: second
  skills: [testing]
`
		atBoundary := map[string]string{
			"workflow.yml":            source,
			"skills/testing/SKILL.md": strings.Repeat("s", 512<<10),
		}
		if _, _, err := DecodeManifest(atBoundary); err != nil {
			t.Fatalf("DecodeManifest rejected exact selected-skill boundary: %v", err)
		}

		aboveBoundary := map[string]string{
			"workflow.yml":                 source,
			"skills/testing/SKILL.md":      strings.Repeat("s", 512<<10),
			"skills/testing/refs/extra.md": "x",
		}
		if _, _, err := DecodeManifest(aboveBoundary); err == nil {
			t.Fatal("DecodeManifest accepted selected skill union above 512 KiB")
		}
	})
}

func releasedV3(signature string) string {
	return `schema_version: 3
name: released-v3
signature_version: 7
` + signature + `plan:
- agent: work
  function_id: work
  prompt: work
  outputs: [result, first-result, changed-result]
  output_types:
    result: review/v1
    first-result: review/v1
    changed-result: review/v1
`
}

func repeatedPromptFunction(agents int) string {
	var plan strings.Builder
	for index := 0; index < agents; index++ {
		fmt.Fprintf(&plan, "- agent: work-%d\n  prompt_file: prompts/work.md\n", index)
	}
	return `schema_version: 3
name: repeated-assets
signature_version: 1
inputs: []
outputs: []
plan:
` + plan.String()
}
