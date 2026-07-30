package workflow_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
)

// TestEverySeedPortTypeHasABuiltInValidator is the missing end-to-end pin
// between the shipped workflow seeds and the built-in validator registry.
//
// The seal path looks a port's declared type up in that registry by exact match
// (agent/snapshot/sealer.go:185, :238). A seed that declares an output type the
// registry does not carry therefore cannot seal that output, the step fails with
// `snapshot contracts: unsupported type ...`, and no operator can repair it:
// the type name is compiled into the seed and the validator is compiled into the
// server. That is a dead workflow, and nothing before this test noticed — the
// seed tests assert the declared type strings and the registry tests assert the
// registered type strings, with no assertion that the two sets agree.
func TestEverySeedPortTypeHasABuiltInValidator(t *testing.T) {
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	entries, err := os.ReadDir("seeds")
	if err != nil {
		t.Fatalf("read seeds: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no seeds found; this test would pass vacuously")
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join("seeds", entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(directory)
			if err != nil {
				t.Fatalf("ManifestFromDir(%q): %v", directory, err)
			}
			if _, isNodePackage := manifest[workflow.NodeFileName]; isNodePackage {
				if _, alsoWorkflow := manifest[workflow.WorkflowFileName]; alsoWorkflow {
					t.Fatalf("%q mixes %s and %s; shipped seed packages must have exactly one manifest kind",
						directory, workflow.NodeFileName, workflow.WorkflowFileName)
				}
				// This enumerator proves workflow ports are backed by the
				// validator registry. Reusable-node packages have their own
				// authoritative compile-and-freeze contract in seed_test.go.
				return
			}
			compiled, err := workflow.CompileDefinition(manifest)
			if err != nil {
				t.Fatalf("CompileDefinition(%q): %v", directory, err)
			}
			signature, err := compiled.PublicSignature()
			if err != nil {
				t.Fatalf("PublicSignature(%q): %v", directory, err)
			}

			ports := make([]workflow.SignaturePort, 0, len(signature.Inputs)+len(signature.Outputs))
			ports = append(ports, signature.Inputs...)
			ports = append(ports, signature.Outputs...)
			if len(ports) == 0 {
				t.Fatalf("%q declares no ports; this subtest would pass vacuously", directory)
			}
			for _, port := range ports {
				if _, err := registry.Lookup(port.Type); err != nil {
					t.Errorf("seed %q port %q declares type %q, which the built-in registry cannot validate: %v; registered types are %q",
						directory, port.Name, port.Type, err, sortedTypeRefs(registry.Types()))
				}
			}
		})
	}
}

func sortedTypeRefs(refs []snapshot.TypeRef) []snapshot.TypeRef {
	sorted := append([]snapshot.TypeRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}
