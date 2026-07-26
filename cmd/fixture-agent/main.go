// Command fixture-agent is a deterministic stand-in for the claude-backed
// agent-runner. It honors the §8.1 agent-step environment contract, writes one
// fixture tree per declared AGENT_OUTPUT_<NAME> destination, writes a minimal
// flight recorder, and exits 0.
//
// The image built from this binary installs it as /usr/local/bin/agent-runner,
// because atc/exec/agent_step.go execs a process whose Path is literally
// "agent-runner". Renaming it breaks the behavioral tier silently.
//
// FIXTURE_CASE selects the tree (default review-accept); hostile-* cases emit
// the adversarial catalog so a live cluster can prove the seal boundary refuses
// them, not just a unit test.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/fixtureagent"
	"github.com/concourse/concourse/agent/schema"
)

// Mirrors agent/runner/runner.go's patterns exactly. The ordering matters:
// AGENT_OUTPUT_REVIEW_RECORD_TYPE also matches outputEnvPattern, so authority
// rows must be classified first or a record-type row would be mistaken for an
// output destination.
var (
	outputEnvPattern          = regexp.MustCompile(`^AGENT_OUTPUT_[A-Z0-9_]+$`)
	inputAuthorityEnvPattern  = regexp.MustCompile(`^(AGENT_INPUT_[A-Z0-9_]+)_SNAPSHOT_(TYPE|DIGEST)$`)
	outputAuthorityEnvPattern = regexp.MustCompile(`^(AGENT_OUTPUT_[A-Z0-9_]+)_RECORD_(TYPE|SCHEMA)$`)
)

type inputAuthority struct {
	port   string
	typ    string
	digest string
}

type outputAuthority struct {
	typ    string
	schema string
}

func main() {
	env := map[string]string{}
	for _, row := range os.Environ() {
		name, value, ok := strings.Cut(row, "=")
		if ok {
			env[name] = value
		}
	}
	os.Exit(run(env, os.Stderr))
}

// run returns the step contract's exit code: 0 = the fixture produced its
// outputs, 2 = platform error.
func run(env map[string]string, stderr io.Writer) int {
	start := time.Now()
	flightDir := env["AGENT_FLIGHT_DIR"]
	stepName := env["AGENT_STEP_NAME"]

	fail := func(format string, args ...any) int {
		message := fmt.Sprintf(format, args...)
		fmt.Fprintf(stderr, "fixture-agent: %s\n", message)
		writeFlight(flightDir, stepName, env, schema.StatusError, message, start)
		return 2
	}

	destinations, outputs, inputs := classify(env)
	if len(destinations) == 0 {
		return fail("no AGENT_OUTPUT_<NAME> destinations in the environment")
	}

	caseName := env["FIXTURE_CASE"]
	if caseName == "" {
		caseName = fixtureagent.CaseReviewAccept
	}
	oversize, _ := strconv.Atoi(env["FIXTURE_PAYLOAD_BYTES"])

	subject, err := primarySubject(inputs, env["FIXTURE_SUBJECT_INPUT"])
	if err != nil {
		return fail("%v", err)
	}

	names := make([]string, 0, len(destinations))
	for name := range destinations {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		authority, declared := outputs[name]
		if !declared {
			// An untyped output: leave a marker file so the mount is not empty
			// and the step's legacy registration still has something to carry.
			if err := os.WriteFile(filepath.Join(destinations[name], "fixture.txt"), []byte("fixture agent\n"), 0o644); err != nil {
				return fail("write untyped output %s: %v", name, err)
			}
			continue
		}
		if err := fixtureagent.WriteTree(destinations[name], caseName, fixtureagent.Authority{
			RecordType:    authority.typ,
			RecordSchema:  authority.schema,
			SubjectInput:  subject.port,
			SubjectType:   subject.typ,
			SubjectDigest: subject.digest,
			OversizeBytes: oversize,
		}); err != nil {
			return fail("materialize %s for %s: %v", caseName, name, err)
		}
	}

	writeFlight(flightDir, stepName, env, schema.StatusPass, "fixture agent wrote "+caseName, start)
	return 0
}

func classify(env map[string]string) (map[string]string, map[string]outputAuthority, map[string]inputAuthority) {
	destinations := map[string]string{}
	outputs := map[string]outputAuthority{}
	inputs := map[string]inputAuthority{}

	for name, value := range env {
		if value == "" {
			continue
		}
		if match := inputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := inputs[match[1]]
			if match[2] == "TYPE" {
				authority.typ = value
			} else {
				authority.digest = value
			}
			authority.port = portName(strings.TrimPrefix(match[1], "AGENT_INPUT_"))
			inputs[match[1]] = authority
			continue
		}
		if match := outputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := outputs[match[1]]
			if match[2] == "TYPE" {
				authority.typ = value
			} else {
				authority.schema = value
			}
			outputs[match[1]] = authority
			continue
		}
		if name != "AGENT_OUTPUT_SCHEMA" && outputEnvPattern.MatchString(name) {
			destinations[name] = value
		}
	}
	return destinations, outputs, inputs
}

// portName reverses the exec's AGENT_OUTPUT_<NAME>/AGENT_INPUT_<NAME> mangling
// (uppercase, dashes to underscores). The reverse is lossy for artifact names
// that genuinely contain an underscore; FIXTURE_SUBJECT_INPUT is the explicit
// escape hatch for those.
func portName(suffix string) string {
	return strings.ReplaceAll(strings.ToLower(suffix), "_", "-")
}

func primarySubject(inputs map[string]inputAuthority, override string) (inputAuthority, error) {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if override != "" {
		for _, key := range keys {
			if inputs[key].port == override {
				return inputs[key], nil
			}
		}
		return inputAuthority{}, fmt.Errorf("FIXTURE_SUBJECT_INPUT=%q names no declared typed input", override)
	}
	if len(keys) != 1 {
		return inputAuthority{}, fmt.Errorf("expected exactly one typed input, found %d; set FIXTURE_SUBJECT_INPUT", len(keys))
	}
	return inputs[keys[0]], nil
}

func writeFlight(flightDir, stepName string, env map[string]string, status schema.Status, summary string, start time.Time) {
	if flightDir == "" {
		return
	}
	if err := os.MkdirAll(flightDir, 0o755); err != nil {
		return
	}

	events, err := os.Create(filepath.Join(flightDir, "events.ndjson"))
	if err == nil {
		defer events.Close()
		writer := schema.NewEventWriter(events)
		buildID, _ := strconv.Atoi(env["BUILD_ID"])
		_ = writeEvent(writer, schema.EventStepStart, schema.StepStartData{
			StepName: stepName, BuildID: buildID, PlanID: env["AGENT_PLAN_ID"],
		})
		// StepEndData.Status speaks the three-way ok|failed|error vocabulary the
		// real runner emits, not the results.json pass/fail vocabulary.
		threeWay, _ := schema.ThreeWayStatus(status)
		_ = writeEvent(writer, schema.EventStepEnd, schema.StepEndData{
			StepName: stepName, Status: threeWay, Summary: summary,
			WallTimeSeconds: int(time.Since(start).Seconds()),
		})
	}

	results := schema.Results{
		SchemaVersion: "1.0", Status: status, Confidence: 1,
		Summary: summary, Artifacts: []schema.Artifact{},
	}
	if raw, err := json.Marshal(results); err == nil {
		_ = os.WriteFile(filepath.Join(flightDir, "results.json"), raw, 0o644)
	}
	_ = os.WriteFile(
		filepath.Join(flightDir, "transcript.ndjson"),
		[]byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":0}`+"\n"),
		0o644,
	)
}

func writeEvent(writer *schema.EventWriter, kind schema.EventType, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writer.Write(schema.Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      kind,
		Data:      raw,
	})
}
