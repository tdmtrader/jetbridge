package legacyworkflow

import (
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
}

func releasedV3(signature string) string {
	return `schema_version: 3
name: released-v3
signature_version: 7
` + signature + `plan:
- agent: work
  function_id: work
  prompt: work
`
}
