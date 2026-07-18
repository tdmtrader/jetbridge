package harvest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
	schema "github.com/concourse/concourse/agent/schema"
)

func readResults(t *testing.T, flight string) schema.Results {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatalf("results.json: %v", err)
	}
	var res schema.Results
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("results.json unmarshal: %v", err)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("results.json invalid: %v", err)
	}
	return res
}

func eventTypes(t *testing.T, flight string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatalf("events.ndjson: %v", err)
	}
	defer f.Close()
	var types []string
	r := schema.NewEventReader(f)
	for {
		e, err := r.Read()
		if err != nil {
			break
		}
		types = append(types, string(e.Type))
	}
	return types
}

// containsType reports whether s is among ss (gates_test.go already
// owns the string-search `contains` in this package).
func containsType(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestRunFlightOutputsOnPass(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	cfg := harvest.Config{StepName: "harvest", Workspace: "workspace",
		Repo: "tdmtrader/scratch", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("exit = %d: %s", exit, out.String())
	}

	res := readResults(t, flight)
	if res.Status != schema.StatusPass || res.SchemaVersion != "1.0" {
		t.Fatalf("results: %+v", res)
	}
	if res.Metadata["pushed_branch"] != "agent/ticket-42" {
		t.Fatalf("metadata: %+v", res.Metadata)
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if res.Metadata["head_sha"] != head {
		t.Fatalf("head_sha: %+v", res.Metadata)
	}
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42"); got != head {
		t.Fatalf("pushed %s, want %s", got, head)
	}

	// stdout mirrors the identical document
	var mirror schema.Results
	if err := json.Unmarshal(out.Bytes(), &mirror); err != nil || mirror.Status != schema.StatusPass {
		t.Fatalf("stdout mirror: %v / %+v", err, mirror)
	}

	types := eventTypes(t, flight)
	for _, want := range []string{"step.start", "push.done", "step.end"} {
		if !containsType(types, want) {
			t.Fatalf("missing event %s in %v", want, types)
		}
	}

	// manifest: base resolvable (clone has origin/main) -> written
	var m harvest.Manifest
	raw, err := os.ReadFile(filepath.Join(flight, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.HeadSHA != head || len(m.Commits) != 1 {
		t.Fatalf("manifest: %+v, %v", m, err)
	}

	// evidence: schema_version harvest/1, pass, commit = head
	var ev harvest.Evidence
	raw, err = os.ReadFile(filepath.Join(flight, "review.json"))
	if err != nil {
		t.Fatalf("review.json: %v", err)
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("evidence unmarshal: %v", err)
	}
	if ev.SchemaVersion != "harvest/1" || !ev.Score.Pass || ev.Metadata.Commit != head {
		t.Fatalf("evidence: %+v", ev)
	}
}

func TestRunFlightGateFailureEvidence(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	seedGoModule(t, ws, map[string]string{
		"go.mod":    gateFixtureGoMod,
		"main.go":   gateFixtureMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tt.Fatal(\"always fails\")\n}\n",
	})
	flight := t.TempDir()
	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
		GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full"}}}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	if res := readResults(t, flight); res.Status != schema.StatusFail {
		t.Fatalf("results: %+v", res)
	}
	if got := git(t, remote, "for-each-ref", "refs/heads/agent"); got != "" {
		t.Fatalf("nothing may be pushed on gate failure: %q", got)
	}
	types := eventTypes(t, flight)
	if !containsType(types, "gate.start") || !containsType(types, "gate.result") {
		t.Fatalf("gate events missing: %v", types)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"gate-test"`) || !strings.Contains(string(raw), `"category":"gate"`) {
		t.Fatalf("gate proven issue missing: %s", raw)
	}
}

func TestRunFlightDirtyEvidence(t *testing.T) {
	ws, _ := workspaceWithRemote(t)
	os.WriteFile(filepath.Join(ws, "wip.txt"), []byte("uncommitted"), 0644)
	flight := t.TempDir()

	var out bytes.Buffer
	exit := harvest.Run(harvest.Config{StepName: "harvest", Repo: "r", Branch: "b", Push: true}, ws, "", flight, &out)
	if exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"workspace-dirty"`) {
		t.Fatalf("workspace-dirty evidence missing: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(ws, "wip.txt")); err != nil {
		t.Fatal("no auto-discard: uncommitted work must survive (F33)")
	}
}

func TestRunJudgePassRecordedAndPushed(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":9,\"rationale\":\"good\",\"issues\":[{\"title\":\"nit\",\"description\":\"d\",\"file\":\"report.md\",\"line\":1}]}]}","model":"m1","total_cost_usd":0.2,"num_turns":1,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5}}`
	t.Setenv("HARVEST_JUDGE_CLI", stubClaude(t, envelope))

	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
		Judge: &harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
			PassThreshold: 6.5,
		}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("exit = %d: %s", exit, out.String())
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42"); got != head {
		t.Fatal("judge pass must still push")
	}
	types := eventTypes(t, flight)
	if !containsType(types, "judge.score") || !containsType(types, "cost.record") {
		t.Fatalf("judge events missing: %v", types)
	}
	raw, _ := os.ReadFile(filepath.Join(flight, "review.json"))
	if !strings.Contains(string(raw), `"judge-correctness-1"`) || !strings.Contains(string(raw), `"category":"judge"`) {
		t.Fatalf("judge observation missing: %s", raw)
	}
}

func TestRunJudgeErrorIsAdvisory(t *testing.T) {
	ws, remote := workspaceWithRemote(t)
	flight := t.TempDir()
	// stub CLI that exits 1: judge errors, push must proceed (§6.4)
	dir := t.TempDir()
	bad := filepath.Join(dir, "claude")
	os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("HARVEST_JUDGE_CLI", bad)

	cfg := harvest.Config{StepName: "harvest", Repo: "r", TargetBranch: "main",
		Branch: "agent/ticket-9", Push: true,
		Judge: &harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "c", Weight: 1}},
			PassThreshold: 6.5,
		}}

	var out bytes.Buffer
	exit := harvest.Run(cfg, ws, "", flight, &out)
	if exit != 0 {
		t.Fatalf("judge error must not block: exit = %d: %s", exit, out.String())
	}
	head := git(t, ws, "rev-parse", "HEAD")
	if got := git(t, remote, "rev-parse", "refs/heads/agent/ticket-9"); got != head {
		t.Fatal("push must have happened")
	}
	res := readResults(t, flight)
	if res.Metadata["judge_error"] == nil || res.Metadata["judge_error"] == "" {
		t.Fatalf("judge_error must be recorded: %+v", res.Metadata)
	}
}

func TestRunNoFlightDirStillWorks(t *testing.T) {
	ws, _ := workspaceWithRemote(t)
	var out bytes.Buffer
	exit := harvest.Run(harvest.Config{StepName: "h", Repo: "r", Branch: "b", Push: true}, ws, "", "", &out)
	if exit != 0 {
		t.Fatalf("flightless run (deployed v0.5 exec) must keep working: %d", exit)
	}
	var res schema.Results
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Status != schema.StatusPass {
		t.Fatalf("stdout results: %v / %+v", err, res)
	}
}
