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

	t.Run("rejects post-release disposition_output", func(t *testing.T) {
		source := strings.Replace(
			releasedV3("\ninputs: []\noutputs: []\n"),
			"signature_version: 7",
			"signature_version: 7\ndisposition_output: review",
			1,
		)
		if _, _, err := DecodeManifest(map[string]string{"workflow.yml": source}); err == nil {
			t.Fatal("DecodeManifest accepted post-release disposition_output")
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
