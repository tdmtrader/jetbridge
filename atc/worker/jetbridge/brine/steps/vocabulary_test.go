package steps

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// Guards on the step vocabulary itself.
//
// A step definition nobody says is dead test code: it compiles, it reads like
// coverage, and it can never run. Sixteen of them had accumulated by the time
// anyone counted — mostly the fake-cluster half of a family whose scenarios
// had moved to a real API server, left behind because nothing pointed at them.
// Worse, their sentences were still occupying the vocabulary, which is why the
// live steps beside them had to be spelled "the runtime is REALLY told the pod
// was deleted" to stay distinct from a step no scenario used.
//
// These are cheap to check and they only get more useful as the corpus grows.

func loadFeatures(t *testing.T) []*brine.ParsedFeature {
	t.Helper()
	paths, err := filepath.Glob("../features/*.feature")
	if err != nil {
		t.Fatalf("glob features: %v", err)
	}
	if len(paths) == 0 {
		wd, _ := os.Getwd()
		t.Fatalf("no feature files found from %s — this test would pass vacuously", wd)
	}
	parsed := make([]*brine.ParsedFeature, 0, len(paths))
	for _, p := range paths {
		f, err := brine.ParseFeatureFile(p)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed = append(parsed, f)
	}
	return parsed
}

// allStepLines returns every step line the suite would execute, with outlines
// already expanded by the parser.
func allStepLines(t *testing.T) []string {
	t.Helper()
	var lines []string
	for _, f := range loadFeatures(t) {
		// A .feature file may declare more than one Feature; walk them all.
		for _, feat := range f.Features {
			for _, sc := range feat.Scenarios {
				for _, st := range sc.Steps {
					lines = append(lines, st.Text)
				}
			}
		}
	}
	if len(lines) == 0 {
		t.Fatal("no step lines parsed — this test would pass vacuously")
	}
	return lines
}

func TestEveryStepDefinitionIsUsedByAScenario(t *testing.T) {
	defs := Definitions()
	registry := brine.NewStepRegistry(defs)

	used := map[string]bool{}
	for _, line := range allStepLines(t) {
		if def, _, ok := registry.Lookup(line); ok {
			used[def.Pattern()] = true
		}
	}

	var dead []string
	for _, d := range defs {
		if !used[d.Pattern()] {
			dead = append(dead, d.Pattern())
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("%d step definitions are not used by any scenario. A definition no "+
			"sentence reaches cannot run, and its pattern still crowds the vocabulary. "+
			"Delete it, or write the scenario that needed it:", len(dead))
		for _, p := range dead {
			t.Errorf("    %q", p)
		}
	}
}

func TestEveryScenarioStepResolvesToADefinition(t *testing.T) {
	registry := brine.NewStepRegistry(Definitions())

	seen := map[string]bool{}
	var undefined []string
	for _, line := range allStepLines(t) {
		if _, _, ok := registry.Lookup(line); !ok && !seen[line] {
			seen[line] = true
			undefined = append(undefined, line)
		}
	}
	sort.Strings(undefined)
	for _, l := range undefined {
		t.Errorf("no step definition matches: %q", l)
	}
}

func TestNoStepLineMatchesTwoDefinitions(t *testing.T) {
	defs := Definitions()

	// Lookup returns the FIRST definition whose pattern matches, so a pattern
	// general enough to cover another one's sentences shadows it silently:
	// the shadowed step still looks used, still compiles, and never runs.
	// Checking each definition one-against-all finds that before it is a
	// mystery about which body executed.
	singles := make([]brine.StepRegistry, len(defs))
	for i, d := range defs {
		singles[i] = brine.NewStepRegistry([]brine.StepDefinition{d})
	}

	for _, line := range allStepLines(t) {
		var matched []string
		for i, d := range defs {
			if _, _, ok := singles[i].Lookup(line); ok {
				matched = append(matched, d.Pattern())
			}
		}
		if len(matched) > 1 {
			sort.Strings(matched)
			t.Errorf("the step %q matches %d definitions, and only the first one runs: %q",
				line, len(matched), matched)
		}
	}
}
