package steps

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// featurePaths walks every feature file under ../features, the live tier in
// features/live included. The manifests select separate tiers; the guards
// read the entire corpus, so a phrase used by no tier is still reported dead.
func featurePaths(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir("../features", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".feature") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("glob features: %v", err)
	}
	if len(paths) == 0 {
		wd, _ := os.Getwd()
		t.Fatalf("no feature files found from %s — this test would pass vacuously", wd)
	}
	return paths
}

func loadFeatures(t *testing.T) []*brine.ParsedFeature {
	t.Helper()
	paths := featurePaths(t)
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
	// The registry is immutable for this check. Repeated scenario sentences
	// have the same match; retain every occurrence but resolve its text once.
	matches := map[string]string{}
	for _, line := range allStepLines(t) {
		pattern, cached := matches[line]
		if !cached {
			if def, _, ok := registry.Lookup(line); ok {
				pattern = def.Pattern()
			}
			matches[line] = pattern
		}
		if pattern != "" {
			used[pattern] = true
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
	resolved := map[string]bool{}
	var undefined []string
	for _, line := range allStepLines(t) {
		ok, cached := resolved[line]
		if !cached {
			_, _, ok = registry.Lookup(line)
			resolved[line] = ok
		}
		if !ok && !seen[line] {
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

	matches := map[string][]string{}
	for _, line := range allStepLines(t) {
		matched, cached := matches[line]
		if !cached {
			for i, d := range defs {
				if _, _, ok := singles[i].Lookup(line); ok {
					matched = append(matched, d.Pattern())
				}
			}
			matches[line] = matched
		}
		if len(matched) > 1 {
			sort.Strings(matched)
			t.Errorf("the step %q matches %d definitions, and only the first one runs: %q",
				line, len(matched), matched)
		}
	}
}

// TestActiveFeatureCorpus checks the entire feature corpus. The real-CLI
// selection guard separately requires nonempty, disjoint manifest inventories.
func TestActiveFeatureCorpus(t *testing.T) {
	for _, path := range featurePaths(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(body), "NOT RUN YET") {
			t.Errorf("%s is active but labelled NOT RUN YET", path)
		}
	}
}
