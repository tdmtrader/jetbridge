package runner_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

func writeStubClaude(t *testing.T, dir, envelope string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWritesFlightRecorder(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	healthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthz.Close()

	claude := writeStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"done\"","model":"m1","cost_usd":0.42,"num_turns":9,"is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}`)

	cfg := runner.Config{
		Prompt:          "do it",
		Model:           "m1",
		MaxTurns:        9,
		FlightDir:       flight,
		WorkDir:         dir,
		StepName:        "write-spec",
		ClaudePath:      claude,
		MCPServers:      map[string]string{"platform": healthz.URL + "/mcp"},
		BuildID:         1234,
		PlanID:          "42",
		TicketID:        7,
		WorkflowName:    "feature-dev",
		WorkflowVersion: 3,
		WorkflowHash:    "abc123",
		BudgetSliceUSD:  2.5,
	}
	exit, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}

	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusPass {
		t.Fatalf("expected pass, got %s", results.Status)
	}

	events, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	reader := schema.NewEventReader(events)
	var recorded []*schema.Event
	for {
		e, err := reader.Read()
		if err != nil {
			break
		}
		recorded = append(recorded, e)
	}
	var types []schema.EventType
	for _, e := range recorded {
		types = append(types, e.Type)
	}
	want := []schema.EventType{schema.EventStepStart, schema.EventCostRecord, schema.EventStepEnd}
	if len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Fatalf("expected %v, got %v", want, types)
	}

	// step.start must carry the full §5 identity payload — build_id and
	// plan_id are the correlation key back to agent_run_metrics and are not
	// optional (review finding, 2026-07-12).
	var start schema.StepStartData
	if err := json.Unmarshal(recorded[0].Data, &start); err != nil {
		t.Fatal(err)
	}
	if start.StepName != "write-spec" {
		t.Errorf("step.start step_name = %q, want %q", start.StepName, "write-spec")
	}
	if start.BuildID != 1234 {
		t.Errorf("step.start build_id = %d, want 1234", start.BuildID)
	}
	if start.PlanID != "42" {
		t.Errorf("step.start plan_id = %q, want %q", start.PlanID, "42")
	}
	if start.TicketID == nil || *start.TicketID != 7 {
		t.Errorf("step.start ticket_id = %v, want 7", start.TicketID)
	}
	if start.WorkflowName != "feature-dev" {
		t.Errorf("step.start workflow_name = %q, want %q", start.WorkflowName, "feature-dev")
	}
	if start.WorkflowVersion == nil || *start.WorkflowVersion != 3 {
		t.Errorf("step.start workflow_version = %v, want 3", start.WorkflowVersion)
	}
	if start.WorkflowHash != "abc123" {
		t.Errorf("step.start workflow_hash = %q, want %q", start.WorkflowHash, "abc123")
	}
	if start.BudgetSliceUSD != 2.5 {
		t.Errorf("step.start budget_slice_usd = %v, want 2.5", start.BudgetSliceUSD)
	}
}

func TestFromEnvReadsStepIdentity(t *testing.T) {
	// The §8.1 rows the agent-step exec sets on the main container; without
	// these reads every step.start opens with build_id 0 / plan_id "".
	t.Setenv("BUILD_ID", "1234")
	t.Setenv("AGENT_PLAN_ID", "5f2a")
	t.Setenv("AGENT_STEP_NAME", "write-spec")
	t.Setenv("AGENT_TICKET_ID", "7")
	t.Setenv("AGENT_WORKFLOW_NAME", "feature-dev")
	t.Setenv("AGENT_WORKFLOW_VERSION", "3")
	t.Setenv("AGENT_WORKFLOW_HASH", "abc123")
	t.Setenv("AGENT_BUDGET_SLICE_USD", "2.50")

	cfg := runner.FromEnv()

	if cfg.BuildID != 1234 {
		t.Errorf("BuildID = %d, want 1234", cfg.BuildID)
	}
	if cfg.PlanID != "5f2a" {
		t.Errorf("PlanID = %q, want %q", cfg.PlanID, "5f2a")
	}
	if cfg.TicketID != 7 {
		t.Errorf("TicketID = %d, want 7", cfg.TicketID)
	}
	if cfg.WorkflowName != "feature-dev" {
		t.Errorf("WorkflowName = %q, want %q", cfg.WorkflowName, "feature-dev")
	}
	if cfg.WorkflowVersion != 3 {
		t.Errorf("WorkflowVersion = %d, want 3", cfg.WorkflowVersion)
	}
	if cfg.WorkflowHash != "abc123" {
		t.Errorf("WorkflowHash = %q, want %q", cfg.WorkflowHash, "abc123")
	}
	if cfg.BudgetSliceUSD != 2.5 {
		t.Errorf("BudgetSliceUSD = %v, want 2.5", cfg.BudgetSliceUSD)
	}
}

func TestFromEnvTreatsMalformedIdentityAsAbsent(t *testing.T) {
	t.Setenv("BUILD_ID", "not-a-number")
	t.Setenv("AGENT_TICKET_ID", "")
	t.Setenv("AGENT_WORKFLOW_VERSION", "-1")
	t.Setenv("AGENT_BUDGET_SLICE_USD", "free")

	cfg := runner.FromEnv()

	if cfg.BuildID != 0 || cfg.TicketID != 0 || cfg.WorkflowVersion != 0 || cfg.BudgetSliceUSD != 0 {
		t.Errorf("malformed identity env should read as absent, got BuildID=%d TicketID=%d WorkflowVersion=%d BudgetSliceUSD=%v",
			cfg.BuildID, cfg.TicketID, cfg.WorkflowVersion, cfg.BudgetSliceUSD)
	}
}

func TestRunMapsCLIErrorToErrorStatus(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude := writeStubClaude(t, dir, `{"type":"result","is_error":true,"result":"\"boom\""}`)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exit != 2 {
		t.Fatalf("expected exit 2 (error), got %d", exit)
	}
}
