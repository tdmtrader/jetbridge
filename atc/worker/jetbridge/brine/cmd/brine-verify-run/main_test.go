package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func floors(mins ...int) []requirement {
	names := []string{"local", "live"}
	required := make([]requirement, len(mins))
	for i, min := range mins {
		required[i] = requirement{name: names[i], min: min, source: "the test asks for"}
	}
	return required
}

func TestVerifyRequiresActualPassingCasesInEveryManifest(t *testing.T) {
	local := "{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"one\",\"status\":\"passed\"}\n"
	live := "{\"type\":\"scenario_end\",\"manifest\":\"live\",\"name\":\"two\",\"status\":\"passed\"}\n"
	end := "{\"type\":\"run_end\",\"failed\":0}\n"
	for _, tc := range []struct {
		name, log string
		valid     bool
	}{
		{"both tiers with diagnostics", "diagnostic\n" + local + live + end, true},
		{"live header but no live case", local + "--- (live) ---\n" + end, false},
		{"missing local tier", live + end, false},
		{"empty run", end, false},
		{"unfinished run", local + live, false},
		{"failed case", local + strings.Replace(live, "passed", "failed", 1) + end, false},
		{"skipped case", local + strings.Replace(live, "passed", "skipped", 1) + end, false},
		{"unexpected manifest", local + strings.Replace(live, "live", "elsewhere", 1) + end, false},
		{"failed summary", local + live + strings.Replace(end, ":0", ":1", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts, err := verify(strings.NewReader(tc.log), floors(1, 1))
			if (err == nil) != tc.valid {
				t.Fatalf("counts=%v error=%v", counts, err)
			}
			if tc.valid && (counts["local"] != 1 || counts["live"] != 1) {
				t.Fatalf("wrong counts: %v", counts)
			}
		})
	}
	if _, err := verify(strings.NewReader(local+live+end), nil); err == nil {
		t.Fatal("empty manifest requirement accepted")
	}
}

// A run that executed three of five hundred scenarios used to certify itself,
// because the only floor was "more than nothing".
func TestVerifyEnforcesEachManifestsExpectedCount(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 3; i++ {
		log.WriteString("{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"one\",\"status\":\"passed\"}\n")
	}
	log.WriteString("{\"type\":\"scenario_end\",\"manifest\":\"live\",\"name\":\"two\",\"status\":\"passed\"}\n")
	log.WriteString("{\"type\":\"run_end\",\"failed\":0}\n")

	if _, err := verify(strings.NewReader(log.String()), floors(3, 1)); err != nil {
		t.Fatalf("a run that met its floor was rejected: %v", err)
	}
	_, err := verify(strings.NewReader(log.String()), floors(4, 1))
	if err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("a short run was accepted: %v", err)
	}
	if _, err := verify(strings.NewReader(log.String()), floors(3, 2)); err == nil {
		t.Fatal("a short live tier was accepted")
	}
}

// Schema drift used to be a `continue`: the record vanished, and with it any
// scenario it was reporting a failure for.
func TestVerifyRefusesRecordsItCannotDecode(t *testing.T) {
	good := "{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"one\",\"status\":\"passed\"}\n"
	end := "{\"type\":\"run_end\",\"failed\":0}\n"
	for _, tc := range []struct{ name, record string }{
		{"status became an object", "{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"two\",\"status\":{\"outcome\":\"failed\"}}\n"},
		{"manifest became a list", "{\"type\":\"scenario_end\",\"manifest\":[\"local\"],\"name\":\"two\",\"status\":\"passed\"}\n"},
		{"status is a word nobody knows", "{\"type\":\"scenario_end\",\"manifest\":\"local\",\"name\":\"two\",\"status\":\"fine\"}\n"},
		{"failed count became a string", "{\"type\":\"run_end\",\"failed\":\"0\"}\n"},
		{"type is not a string", "{\"type\":[\"scenario_end\"],\"manifest\":\"local\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verify(strings.NewReader(good+tc.record+end), floors(1)); err == nil {
				t.Fatal("an undecodable record passed verification")
			}
		})
	}
	// A diagnostic that happens to be valid JSON but carries no type is not a
	// protocol record and must still be ignored.
	if _, err := verify(strings.NewReader("{\"level\":\"info\"}\n"+good+end), floors(1)); err != nil {
		t.Fatalf("a non-protocol JSON line was treated as a record: %v", err)
	}
}

func TestCountScenariosReadsOutlinesByTheirExamplesRows(t *testing.T) {
	feature := `Feature: counting
  Background:
    Given something

  Scenario: a plain one
    Given a step

  # Scenario: commented out
  Scenario Outline: a table of three
    Given <thing>

    Examples:
      | thing |
      | one   |
      | two   |

    Examples:
      | thing |
      | three |

  Scenario: another plain one
    Given a step with a data table
      | not | an | examples | row |
`
	count, err := countScenarios(strings.NewReader(feature))
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("counted %d scenarios, want 5 (2 plain + 3 examples rows)", count)
	}
}

func TestMinimumComesOutOfTheManifestsFeatureFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("features/one.feature", "Feature: one\n  Scenario: a\n    Given x\n  Scenario: b\n    Given x\n")
	write("features/two.feature", "Feature: two\n  Scenario: c\n    Given x\n")
	write(".brine", "runner:\n  name: jetbridge\n  features: ignored/*.feature\nfeatures: \"features/*.feature\"\nbudgets:\n  undefined: 0\n")

	req, err := parseRequirement("local="+filepath.Join(dir, ".brine"), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if req.name != "local" || req.min != 3 {
		t.Fatalf("derived %+v, want a floor of 3 for local", req)
	}

	if _, err := parseRequirement("local", 0, false); err == nil {
		t.Fatal("a manifest with no declared minimum was accepted")
	}
	if req, err := parseRequirement("local", 7, true); err != nil || req.min != 7 {
		t.Fatalf("--min-scenarios was not applied: %+v %v", req, err)
	}
	if req, err := parseRequirement("local=12", 0, false); err != nil || req.min != 12 {
		t.Fatalf("an explicit count was not applied: %+v %v", req, err)
	}
	if _, err := parseRequirement("local="+filepath.Join(dir, "absent.brine"), 0, false); err == nil {
		t.Fatal("a missing manifest path was accepted")
	}
}

// The manifest's own budgets.scenarios_min is the de-enrolment guard: the
// feature-file count alone shrinks with every file that leaves the glob.
func TestDeclaredFloorIsNeverUndercutByTheFeatureFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("features/one.feature", "Feature: one\n  Scenario: a\n    Given x\n  Scenario: b\n    Given x\n")
	manifest := func(budgets string) string {
		write(".brine", "runner:\n  name: jetbridge\nfeatures: \"features/*.feature\"\nbudgets:\n"+budgets)
		return "local=" + filepath.Join(dir, ".brine")
	}

	if got, err := parseRequirement(manifest("  undefined: 0\n  scenarios_min: 2 # exact\n"), 0, false); err != nil || got.min != 2 {
		t.Fatalf("floor equal to the count: %+v %v", got, err)
	}
	if got, err := parseRequirement(manifest("  scenarios_min: 1\n"), 0, false); err != nil || got.min != 2 {
		t.Fatalf("a floor below the count must yield the count: %+v %v", got, err)
	}
	if _, err := parseRequirement(manifest("  scenarios_min: 3\n"), 0, false); err == nil {
		t.Fatal("a floor the feature files no longer reach was accepted; that is a scenario leaving the claim")
	}
	if _, err := parseRequirement(manifest("  scenarios_min: many\n"), 0, false); err == nil {
		t.Fatal("a non-numeric floor was accepted")
	}
	if got, err := parseRequirement(manifest("  undefined: 0\n"), 0, false); err != nil || got.min != 2 {
		t.Fatalf("no declared floor must fall back to the count: %+v %v", got, err)
	}
}
