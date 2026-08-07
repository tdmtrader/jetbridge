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

			// Two contract invariants a golden file cannot state on its own.
			// The golden pins WHAT this seed's graph is; these pin WHY it is
			// allowed to be that, so a future seed cannot quietly reintroduce
			// either defect by shipping a new golden alongside it.
			assertEndpointEdgesCarryPublicPortNames(t, built)
			assertNoDuplicateEdges(t, built)

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

// assertEndpointEdgesCarryPublicPortNames pins the join the run page depends
// on: an edge touching an endpoint node must be labelled with that endpoint's
// public port name.
//
// The durable run-level binding is keyed by the public port
// (agent_workflow_run_snapshots.port_name, served as OutputManifest.Port), and
// an endpoint node's DisplayName is that same port. Labelling a public-output
// edge with `outputs[].from` instead — which small-fix-v3 and version-upgrade-v3
// both declare differently from the port name — put the graph
// and the run's own bindings in disjoint namespaces, so a node could never be
// shown to have produced the workflow's deliverable.
func assertEndpointEdgesCarryPublicPortNames(t *testing.T, built Graph) {
	t.Helper()
	for _, edge := range built.Edges {
		for _, endpoint := range []string{edge.From, edge.To} {
			node, found := built.Node(endpoint)
			if !found {
				t.Fatalf("edge %+v names node %q, which the graph does not contain", edge, endpoint)
			}
			switch node.Kind {
			case KindInput, KindOutput, KindResourceSource:
				if edge.PortName != node.DisplayName {
					t.Fatalf(
						"edge %+v touches endpoint %q (public port %q) but is labelled %q; "+
							"a run-level binding could never be joined onto it",
						edge, node.ID, node.DisplayName, edge.PortName,
					)
				}
			}
		}
	}
}

// assertNoDuplicateEdges pins Edge's own meaning. An Edge carries no identity
// beyond (From, To, PortName, TypeRef, Optional), so "the labelled connection
// between this producer and this consumer" cannot legitimately occur twice.
// Any consumer that draws one line per edge, or builds an adjacency multiset,
// would otherwise double the dependency.
func assertNoDuplicateEdges(t *testing.T, built Graph) {
	t.Helper()
	seen := make(map[Edge]bool, len(built.Edges))
	for _, edge := range built.Edges {
		if seen[edge] {
			t.Fatalf("duplicate edge %+v", edge)
		}
		seen[edge] = true
	}
}
