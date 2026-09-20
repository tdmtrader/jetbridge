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
// features/live included. The .brine manifest at the module root runs only
// features/*.feature and live/.brine runs the rest; the guards read both, so a
// phrase defined for one tier and said by neither is still reported dead.
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

// TestEveryHangarScenarioCitesARequirement is the fourth guard, and the Hangar
// family is why it exists.
//
// Convention 4 of learnings/brine-as-the-test-runner.md asks every scenario in
// this family to cite the numbered requirement it is about, as an @HOP-<n> tag
// ON THE SCENARIO and not only on the feature. The reason is measurable: of the
// 143 archive requirement IDs, 98 are greppable as tags and 45 are not, and the
// proposal's own finding is that the problem was never coverage but
// traceability. A citation grep is the only thing that makes traceability real,
// and a grep can only find a tag that is there.
//
// It is scoped to hangar-*.feature deliberately. Retro-fitting the rule onto
// the 35 files that predate it would be a different change, and the families
// migrated from Go suites that never had IDs say so and cite mutations instead.
func TestEveryHangarScenarioCitesARequirement(t *testing.T) {
	var checked int
	for _, path := range featurePaths(t) {
		if !strings.HasPrefix(filepath.Base(path), "hangar-") {
			continue
		}
		parsed, err := brine.ParseFeatureFile(path)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, feat := range parsed.Features {
			for _, sc := range feat.Scenarios {
				checked++
				if !citesHangarRequirement(sc.Tags) {
					t.Errorf("%s: scenario %q carries no @HOP-<n> tag, so no requirement grep "+
						"will ever find it. Tag the scenario with the spec.md requirement "+
						"numbers it is about.", filepath.Base(path), sc.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no hangar-*.feature scenarios were examined — this test would pass vacuously")
	}
}

func citesHangarRequirement(tags []string) bool {
	for _, tag := range tags {
		if strings.HasPrefix(tag, "@HOP-") && len(tag) > len("@HOP-") {
			return true
		}
	}
	return false
}

// TestPendingHangarFeaturesAreNotRun pins the arrangement the guards above
// depend on: features/pending/ is CHECKED, never executed.
//
// Without this, the directory would be one manifest edit away from turning a
// set of deliberately-red skeletons into a red suite — or, worse, from being
// quietly counted as coverage. The manifest glob is the fact, so the manifest
// is what this reads.
//
// BOTH tiers have had one, and NEITHER has one now. features/pending/ held
// the Hangar output family until its production plane landed; every
// hangar-*.feature then moved up into features/, where the @HOP- guard above
// reads them, and the directory has not existed since. features/live/pending/
// held live scenarios whose production change was written and then reverted
// (commit 388692eb54): resource_process.go, exec_status.go, the watch.go
// expiry fix, volume.go's mkdir, sidecar_logs.go, and two process.go
// clauses. Those fixes have since been re-applied and every one of its
// scenarios moved up into features/live/.
//
// The two tiers are still guarded differently, because the claims differ.
// For the live tier the guard asserts the OPPOSITE of parking: the directory
// stays gone, and nothing in the running live corpus is marked NOT RUN YET.
// Every scenario it held has landed, so a live scenario that is red against
// production again is a production regression to fix, not a file to park.
// For the local tier the guard only pins the arrangement -- the glob never
// reaches a pending directory -- because parking a skeleton there is still a
// legitimate move for a phase nobody has written, and the point is that such
// a skeleton can neither be run nor be counted as coverage.
func TestPendingHangarFeaturesAreNotRun(t *testing.T) {
	for _, tier := range []struct {
		manifest string
		glob     string
		pending  string
		running  string
		landed   bool
		why      string
	}{
		{
			manifest: "../.brine",
			glob:     `features: "features/*.feature"`,
			pending:  "../features/pending/*.feature",
			running:  "../features/*.feature",
			why: `If the glob now reaches features/pending/, every skeleton in it runs and ` +
				`fails: move the scenarios up a directory as their phases land instead of ` +
				`widening the glob.`,
		},
		{
			manifest: "../live/.brine",
			glob:     "features: ../features/live/*.feature",
			pending:  "../features/live/pending/*.feature",
			running:  "../features/live/*.feature",
			landed:   true,
			why: `If the glob now reaches features/live/pending/, the live tier goes red on ` +
				`scenarios that were red BY DESIGN against core: restore the production change ` +
				`each pending file names and move the scenario up a directory instead of ` +
				`widening the glob.`,
		},
	} {
		manifest, err := os.ReadFile(tier.manifest)
		if err != nil {
			t.Fatalf("read the brine manifest %s: %v", tier.manifest, err)
		}
		if !strings.Contains(string(manifest), tier.glob) {
			t.Fatalf("%s no longer says %s. %s", tier.manifest, tier.glob, tier.why)
		}
		pending, err := filepath.Glob(tier.pending)
		if err != nil {
			t.Fatalf("glob %s: %v", tier.pending, err)
		}
		if tier.landed && len(pending) > 0 {
			t.Errorf("%d file(s) match %s, but every scenario that directory held has landed "+
				"in production and moved up: %v. A live scenario that is red against production "+
				"is a regression to fix, not a file to park; do not recreate the directory.",
				len(pending), tier.pending, pending)
		}
		for _, path := range pending {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if !strings.Contains(string(body), "NOT RUN YET") {
				t.Errorf("%s does not say NOT RUN YET in its Feature description. A reader who "+
					"finds a pending file has to be told it is not coverage.", path)
			}
		}
		// The running corpus is the other half of the arrangement: a file the
		// glob DOES reach must never carry the marker, or a reader is told a
		// scenario that runs on every push is not coverage.
		running, err := filepath.Glob(tier.running)
		if err != nil {
			t.Fatalf("glob %s: %v", tier.running, err)
		}
		if len(running) == 0 {
			t.Fatalf("%s matches nothing; the tier's corpus has moved and this guard is looking at the wrong place", tier.running)
		}
		for _, path := range running {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if strings.Contains(string(body), "NOT RUN YET") {
				t.Errorf("%s says NOT RUN YET but the manifest glob runs it. Either the scenario "+
					"runs, in which case the marker lies, or it may not, in which case it does "+
					"not belong under the glob.", path)
			}
		}
	}
}
