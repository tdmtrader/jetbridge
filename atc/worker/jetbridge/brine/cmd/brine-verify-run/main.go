// Verify that every required manifest actually ran, from structured events.
//
// Two blind spots this tool had, both of which let a nearly empty run certify
// itself:
//
//   - One anonymous struct decoded both scenario_end and run_end, and a decode
//     error was a `continue`. Any drift in the event schema -- a status that
//     became an object, a count that became a string -- turned every affected
//     record into a line this tool skipped in silence, including the records
//     for FAILING scenarios. A record that says it is a scenario_end and then
//     will not decode is now fatal; only a line that is not a protocol record
//     at all (the diagnostics that share the log) is skipped.
//
//   - The floor was one passing scenario per manifest. A run that executed 3
//     of 568 scenarios passed verification. Each manifest now carries a
//     minimum, either given outright or counted out of the feature files its
//     .brine manifest globs.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// requirement is one manifest that must have run, and the least number of
// passing scenarios that counts as having run it.
type requirement struct {
	name string
	min  int
	// source says where min came from, so a failure can be argued with.
	source string
}

// scenarioEnd and runEnd are separate types on purpose: one struct covering
// both is how a run_end's shape came to be enforced against scenario records
// and neither's against itself.
type scenarioEnd struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Manifest string `json:"manifest"`
}

type runEnd struct {
	Failed int `json:"failed"`
}

// terminalStatuses are the scenario outcomes this tool knows how to judge.
// Anything else is schema drift and is reported as such rather than being
// folded into "not passed", which would name the wrong problem.
var terminalStatuses = map[string]bool{
	"passed": true, "failed": true, "errored": true,
	"skipped": true, "undefined": true, "pending": true,
}

func verify(input io.Reader, required []requirement) (map[string]int, error) {
	counts := map[string]int{}
	for _, req := range required {
		if _, duplicate := counts[req.name]; duplicate {
			return nil, fmt.Errorf("manifest %q required twice", req.name)
		}
		counts[req.name] = 0
	}
	if len(counts) == 0 {
		return nil, fmt.Errorf("no required manifests")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	ended := false
	for record := 1; scanner.Scan(); record++ {
		line := scanner.Bytes()
		// Diagnostics share this file. A line that is not a JSON object is
		// one of them; a line that IS one is held to the protocol.
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		rawType, present := fields["type"]
		if !present {
			continue
		}
		var eventType string
		if err := json.Unmarshal(rawType, &eventType); err != nil {
			return nil, fmt.Errorf("record %d has a non-string type: %s", record, excerpt(line))
		}
		switch eventType {
		case "scenario_end":
			var event scenarioEnd
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, fmt.Errorf("record %d says scenario_end and will not decode: %v: %s",
					record, err, excerpt(line))
			}
			if _, ok := counts[event.Manifest]; !ok {
				return nil, fmt.Errorf("scenario %q belongs to unexpected manifest %q", event.Name, event.Manifest)
			}
			if !terminalStatuses[event.Status] {
				return nil, fmt.Errorf("scenario %q reports unknown status %q", event.Name, event.Status)
			}
			if event.Status != "passed" {
				return nil, fmt.Errorf("scenario %q: %s", event.Name, event.Status)
			}
			counts[event.Manifest]++
		case "run_end":
			var event runEnd
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, fmt.Errorf("record %d says run_end and will not decode: %v: %s",
					record, err, excerpt(line))
			}
			if event.Failed != 0 {
				return nil, fmt.Errorf("run reports %d failures", event.Failed)
			}
			ended = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !ended {
		return nil, fmt.Errorf("missing run_end record")
	}
	for _, req := range required {
		if counts[req.name] < req.min {
			return nil, fmt.Errorf("required manifest %q passed %d scenarios, short of the %d %s",
				req.name, counts[req.name], req.min, req.source)
		}
	}
	return counts, nil
}

// excerpt keeps a malformed record readable in an error message.
func excerpt(line []byte) string {
	const limit = 200
	if len(line) > limit {
		return string(line[:limit]) + "..."
	}
	return string(line)
}

// parseRequirement reads one MANIFEST argument: NAME, NAME=COUNT, or
// NAME=PATH-TO-.brine. A bare NAME needs --min-scenarios, because a manifest
// with no declared floor is the hole this tool was closing.
func parseRequirement(argument string, fallback int, hasFallback bool) (requirement, error) {
	name, spec, given := strings.Cut(argument, "=")
	if name == "" {
		return requirement{}, fmt.Errorf("empty manifest name in %q", argument)
	}
	if !given {
		if !hasFallback {
			return requirement{}, fmt.Errorf(
				"manifest %q declares no minimum: pass %s=COUNT, %s=PATH/.brine, or --min-scenarios N",
				name, name, name)
		}
		return requirement{name: name, min: fallback, source: "--min-scenarios asks for"}, nil
	}
	if count, err := strconv.Atoi(spec); err == nil {
		if count < 1 {
			return requirement{}, fmt.Errorf("manifest %q asks for a minimum of %d; a run of nothing is the failure this checks for", name, count)
		}
		return requirement{name: name, min: count, source: "the argument asks for"}, nil
	}
	count, source, err := minimumFromManifest(spec)
	if err != nil {
		return requirement{}, fmt.Errorf("manifest %q: %w", name, err)
	}
	return requirement{name: name, min: count, source: source}, nil
}

// minimumFromManifest derives a .brine manifest's floor. It counts the
// scenarios the manifest's feature files declare -- every one of them must
// pass, since this tool already treats a skipped scenario as a failure -- and
// takes the larger of that and the manifest's own `budgets.scenarios_min`.
// The count alone is self-referential: a feature file dropped from the glob
// shrinks the run and the count together, which is the de-enrolment the
// declared floor exists to catch. A declared floor the feature files no
// longer reach is reported as such rather than quietly lowered.
func minimumFromManifest(path string) (int, string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, "", fmt.Errorf("reading the manifest: %w", err)
	}
	counted, err := countManifestScenarios(path, string(body))
	if err != nil {
		return 0, "", err
	}
	floor, declared, err := manifestScenarioFloor(string(body))
	if err != nil {
		return 0, "", err
	}
	if !declared {
		return counted, fmt.Sprintf("scenarios %s declares", path), nil
	}
	if counted < floor {
		return 0, "", fmt.Errorf(
			"%s declares budgets.scenarios_min: %d but its feature files declare only %d scenarios; "+
				"a scenario that left the claim needs a disposition, not a lower floor",
			path, floor, counted)
	}
	if counted > floor {
		return counted, fmt.Sprintf("scenarios %s declares (above its budgets.scenarios_min of %d)", path, floor), nil
	}
	return floor, fmt.Sprintf("budgets.scenarios_min in %s asks for", path), nil
}

// countManifestScenarios counts the scenarios the manifest's feature glob
// resolves to.
func countManifestScenarios(path, body string) (int, error) {
	pattern := manifestFeatureGlob(body)
	if pattern == "" {
		return 0, fmt.Errorf("%s declares no top-level features: glob", path)
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(filepath.Dir(path), pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("the features glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("the features glob %s matches no files", pattern)
	}
	total := 0
	for _, match := range matches {
		feature, err := os.Open(match)
		if err != nil {
			return 0, err
		}
		count, err := countScenarios(feature)
		feature.Close()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", match, err)
		}
		total += count
	}
	if total == 0 {
		return 0, fmt.Errorf("the features glob %s declares no scenarios", pattern)
	}
	return total, nil
}

// manifestScenarioFloor reads `budgets.scenarios_min` out of a .brine
// manifest: the indented key under a column-0 `budgets:` block. A value that
// is not a non-negative whole number is an error, matching brine's own
// refusal to let a floor quietly evaporate into "no floor".
func manifestScenarioFloor(body string) (floor int, declared bool, err error) {
	inBudgets := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inBudgets = strings.HasPrefix(line, "budgets:")
			continue
		}
		if !inBudgets || !strings.HasPrefix(trimmed, "scenarios_min:") {
			continue
		}
		value := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(trimmed, "scenarios_min:"), " #", 2)[0])
		floor, err = strconv.Atoi(value)
		if err != nil || floor < 0 {
			return 0, true, fmt.Errorf("budgets.scenarios_min: %q is not a non-negative whole number", value)
		}
		return floor, true, nil
	}
	return 0, false, nil
}

// manifestFeatureGlob pulls the top-level `features:` value out of a .brine
// manifest. Top-level is the whole subset that is read: the nested keys under
// runner: and budgets: are indented, and a features: key at column 0 is the
// only one this tool has any business with.
func manifestFeatureGlob(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "features:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "features:"))
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return strings.TrimSpace(strings.SplitN(value, " #", 2)[0])
	}
	return ""
}

// countScenarios counts the scenarios a Gherkin feature file will execute: one
// per Scenario, and one per Examples DATA row under a Scenario Outline (the
// first row of an Examples table is its header).
func countScenarios(input io.Reader) (int, error) {
	const (
		outside = iota
		outline
		examples
	)
	state := outside
	rows := 0
	count := 0
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "Scenario Outline:"), strings.HasPrefix(line, "Scenario Template:"):
			state, rows = outline, 0
		case strings.HasPrefix(line, "Scenario:"), strings.HasPrefix(line, "Example:"):
			state = outside
			count++
		case strings.HasPrefix(line, "Examples:"), strings.HasPrefix(line, "Scenarios:"):
			if state == outside {
				return 0, fmt.Errorf("an Examples table with no Scenario Outline above it")
			}
			state, rows = examples, 0
		case state == examples && strings.HasPrefix(line, "|"):
			rows++
			if rows > 1 {
				count++
			}
		case strings.HasPrefix(line, "Feature:"), strings.HasPrefix(line, "Rule:"), strings.HasPrefix(line, "Background:"):
			state = outside
		case state == examples:
			// Anything else ends the table.
			state = outline
		}
	}
	return count, scanner.Err()
}

const usage = `usage: brine-verify-run [--min-scenarios N] LOG MANIFEST[=COUNT|=PATH/.brine]...

Each MANIFEST must have passed at least its minimum number of scenarios.
The minimum is the COUNT given after '=', or the number of scenarios the
.brine manifest at that path globs, or --min-scenarios for the rest.`

func main() {
	arguments := os.Args[1:]
	fallback, hasFallback := 0, false
	for len(arguments) > 0 && strings.HasPrefix(arguments[0], "--") {
		flag := arguments[0]
		if flag != "--min-scenarios" {
			fmt.Fprintf(os.Stderr, "unknown flag %q\n%s\n", flag, usage)
			os.Exit(2)
		}
		if len(arguments) < 2 {
			fmt.Fprintf(os.Stderr, "%s needs a count\n%s\n", flag, usage)
			os.Exit(2)
		}
		count, err := strconv.Atoi(arguments[1])
		if err != nil || count < 1 {
			fmt.Fprintf(os.Stderr, "%s needs a positive count, got %q\n", flag, arguments[1])
			os.Exit(2)
		}
		fallback, hasFallback = count, true
		arguments = arguments[2:]
	}
	if len(arguments) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	required := make([]requirement, 0, len(arguments)-1)
	for _, argument := range arguments[1:] {
		req, err := parseRequirement(argument, fallback, hasFallback)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		required = append(required, req)
	}
	file, err := os.Open(arguments[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	counts, err := verify(file, required)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	for _, name := range names {
		fmt.Printf("%s: %d passed\n", name, counts[name])
		total += counts[name]
	}
	fmt.Printf("%d passed, 0 failed across %d manifests\n", total, len(counts))
}
