package graph

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden graphs")

// Every shipped seed workflow must produce a stable graph. Regenerate with:
//
//	go test ./agent/workflow/graph/ -run TestSeedGraphs -update-golden
func TestSeedGraphs(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "seeds", "*", "workflow.yaml"))
	if err != nil {
		t.Fatalf("globbing seeds: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected to find seed workflows")
	}

	for _, match := range matches {
		name := filepath.Base(filepath.Dir(match))
		t.Run(name, func(t *testing.T) {
			dir := filepath.Dir(match)
			manifest, err := workflow.ManifestFromDir(dir)
			if err != nil {
				t.Fatalf("ManifestFromDir(%q): %v", name, err)
			}
			definition, err := workflow.CompileDefinition(manifest)
			if err != nil {
				t.Fatalf("CompileDefinition(%q): %v", name, err)
			}

			built, err := Build(definition.Function)
			if err != nil {
				t.Fatalf("Build returned an error: %v", err)
			}

			actual, err := json.MarshalIndent(built, "", "  ")
			if err != nil {
				t.Fatalf("marshalling graph: %v", err)
			}
			actual = append(actual, '\n')

			golden := filepath.Join("testdata", name+".json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("creating testdata: %v", err)
				}
				if err := os.WriteFile(golden, actual, 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				return
			}

			expected, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (regenerate with -update-golden): %v", err)
			}
			if string(expected) != string(actual) {
				t.Fatalf("graph for %s changed.\nwant:\n%s\ngot:\n%s", name, expected, actual)
			}
		})
	}
}
