package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func envFixture(t *testing.T, outputDir, flightDir string) map[string]string {
	t.Helper()
	digest, found := contracts.SchemaDigestFor(snapshot.TypeRef("review/v1"))
	if !found {
		t.Fatal("review/v1 has no schema digest")
	}
	return map[string]string{
		"AGENT_FLIGHT_DIR":                   flightDir,
		"AGENT_STEP_NAME":                    "fixture-review",
		"AGENT_OUTPUT_REVIEW":                outputDir,
		"AGENT_OUTPUT_REVIEW_RECORD_TYPE":    "review/v1",
		"AGENT_OUTPUT_REVIEW_RECORD_SCHEMA":  digest.String(),
		"AGENT_INPUT_CHANGE_SNAPSHOT_TYPE":   "opaque/v1",
		"AGENT_INPUT_CHANGE_SNAPSHOT_DIGEST": "sha256:" + strings.Repeat("d", 64),
		"AGENT_OUTPUT_SCHEMA":                "ignored-advisory-value",
	}
}

func TestRunWritesEveryDeclaredRecordOutputAndTheFlightTrio(t *testing.T) {
	outputDir, flightDir := t.TempDir(), t.TempDir()
	env := envFixture(t, outputDir, flightDir)

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}

	body, err := os.ReadFile(filepath.Join(outputDir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Schema.String() != env["AGENT_OUTPUT_REVIEW_RECORD_SCHEMA"] {
		t.Fatalf("schema = %q, want the injected value", record.Schema)
	}
	if record.Subjects[0].Input != "change" {
		t.Fatalf("subject input = %q, want the de-mangled port name", record.Subjects[0].Input)
	}
	if record.Subjects[0].Digest.String() != env["AGENT_INPUT_CHANGE_SNAPSHOT_DIGEST"] {
		t.Fatalf("subject digest = %q, want the injected value", record.Subjects[0].Digest)
	}

	results, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var decoded schema.Results
	if err := json.Unmarshal(results, &decoded); err != nil {
		t.Fatalf("decode results.json: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("results.json is not a valid Results document: %v", err)
	}
	if decoded.Status != schema.StatusPass {
		t.Fatalf("status = %q", decoded.Status)
	}

	events, err := os.ReadFile(filepath.Join(flightDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(events)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"step.start"`) || !strings.Contains(lines[1], `"step.end"`) {
		t.Fatalf("events = %s", events)
	}
	if _, err := os.Stat(filepath.Join(flightDir, "transcript.ndjson")); err != nil {
		t.Fatalf("stat transcript.ndjson: %v", err)
	}
}

func TestRunHonorsFixtureCaseAndSubjectInputOverride(t *testing.T) {
	outputDir, flightDir := t.TempDir(), t.TempDir()
	env := envFixture(t, outputDir, flightDir)
	// A second typed input, so the override has something to select. This is
	// the case the knob exists for: with more than one declared typed input
	// there is no single obvious primary subject, and FIXTURE_SUBJECT_INPUT
	// names which one the record binds to. AGENT_INPUT_WORK_ITEM_* de-mangles
	// to the port name "work-item".
	env["AGENT_INPUT_WORK_ITEM_SNAPSHOT_TYPE"] = "work-item/v1"
	env["AGENT_INPUT_WORK_ITEM_SNAPSHOT_DIGEST"] = "sha256:" + strings.Repeat("e", 64)
	env["FIXTURE_CASE"] = "hostile-schema-digest"
	env["FIXTURE_SUBJECT_INPUT"] = "work-item"

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(outputDir, "record.json"))
	if err != nil {
		t.Fatalf("read record.json: %v", err)
	}
	var record contracts.Record[contracts.ReviewBody]
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("decode record.json: %v", err)
	}
	if record.Schema.String() == env["AGENT_OUTPUT_REVIEW_RECORD_SCHEMA"] {
		t.Fatal("hostile-schema-digest did not corrupt the schema digest")
	}
	if record.Subjects[0].Input != "work-item" {
		t.Fatalf("subject input = %q, want the FIXTURE_SUBJECT_INPUT override", record.Subjects[0].Input)
	}
	// The override selects a whole input, not just a name: the subject's type
	// and digest must come from the input it named, never from the other one.
	if record.Subjects[0].Digest.String() != env["AGENT_INPUT_WORK_ITEM_SNAPSHOT_DIGEST"] {
		t.Fatalf("subject digest = %q, want the selected input's digest", record.Subjects[0].Digest)
	}
	if record.Subjects[0].Type.String() != "work-item/v1" {
		t.Fatalf("subject type = %q, want the selected input's type", record.Subjects[0].Type)
	}
}

func TestRunFailsLoudlyWhenTheEnvContractIsIncomplete(t *testing.T) {
	flightDir := t.TempDir()
	env := map[string]string{"AGENT_FLIGHT_DIR": flightDir}

	var stderr bytes.Buffer
	if code := run(env, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no AGENT_OUTPUT_<NAME> destinations") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	results, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("a platform error must still leave a flight recorder: %v", err)
	}
	var decoded schema.Results
	if err := json.Unmarshal(results, &decoded); err != nil {
		t.Fatalf("decode results.json: %v", err)
	}
	if decoded.Status != schema.StatusError {
		t.Fatalf("status = %q, want error", decoded.Status)
	}
}
