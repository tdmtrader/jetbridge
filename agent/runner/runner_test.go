package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// readEventTypes decodes flight/events.ndjson and returns the event types in
// order.
func readEventTypes(t *testing.T, flight string) []schema.EventType {
	t.Helper()
	events, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	reader := schema.NewEventReader(events)
	var types []schema.EventType
	for {
		e, err := reader.Read()
		if err != nil {
			break
		}
		types = append(types, e.Type)
	}
	return types
}

func TestRunFlushesFlightRecorderWhenCanceledMidRun(t *testing.T) {
	// The terminal-end kill path (step timeout / build abort): the jetbridge
	// exec SIGTERMs the supervised process group, agent-runner's
	// NotifyContext cancels ctx, claude is killed — and the runner MUST
	// still write results.json and close the event stream with step.end
	// before the 10s kill grace expires in a group SIGKILL, or ingestion
	// records the zero-cost, no-step.end error row this finding is about
	// (review finding, 2026-07-12). Claude routinely leaks descendants that
	// inherit its stdout pipe; without cmd.WaitDelay the runner blocks on
	// pipe drain until the leaked sleeps below exit (~60s) and never gets
	// to flush.
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	started := filepath.Join(dir, "claude-started")
	claude := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n: > '" + started + "'\nsleep 60 &\nsleep 60\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Cancel only once claude is actually running, so the test
		// exercises the killed-mid-run drain, not a never-started command.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	begin := time.Now()
	// Buffered IO: the leaked sleeps inherit claude's stdout/stderr, and
	// routing them at the test binary's own std streams would keep the
	// `go test` pipe open for their full 60s after the tests finish.
	exit, err := runner.Run(ctx, runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	elapsed := time.Since(begin)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 2 {
		t.Fatalf("expected exit 2 (error), got %d", exit)
	}
	// Without WaitDelay the leaked sleeps hold the stdout pipe for ~60s and
	// the runner would be group-SIGKILLed long before returning.
	if elapsed > 20*time.Second {
		t.Fatalf("run blocked %v on pipe drain after cancellation; must return well within the terminal-kill grace", elapsed)
	}

	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatalf("results.json must be written after a canceled run: %v", err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusError {
		t.Fatalf("expected error status, got %s", results.Status)
	}

	types := readEventTypes(t, flight)
	if len(types) == 0 || types[0] != schema.EventStepStart {
		t.Fatalf("event stream must start with step.start, got %v", types)
	}
	if types[len(types)-1] != schema.EventStepEnd {
		t.Fatalf("event stream must end with step.end even when canceled mid-run, got %v", types)
	}
}

func TestRunTreatsWaitDelayExpiryAsClaudeOutcome(t *testing.T) {
	// claude exits 0 with a valid envelope but leaks a descendant holding
	// the stdout pipe. ErrWaitDelay is returned only when Wait would
	// otherwise return nil — the envelope is the authoritative outcome, so
	// the run must still map to pass, not error.
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	claude := filepath.Join(dir, "claude")
	envelope := `{"type":"result","subtype":"success","result":"\"done\"","model":"m1","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5}}`
	script := "#!/bin/sh\nsleep 60 &\necho '" + envelope + "'\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0 (pass) when only the pipe drain timed out, got %d", exit)
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

// readEvents decodes flight/events.ndjson into the recorded events in order.
func readEvents(t *testing.T, flight string) []*schema.Event {
	t.Helper()
	f, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := schema.NewEventReader(f)
	var out []*schema.Event
	for {
		e, err := reader.Read()
		if err != nil {
			break
		}
		out = append(out, e)
	}
	return out
}

func TestRunSidecarFailureIsPlatformError(t *testing.T) {
	// A declared MCP sidecar that never becomes healthy is a PLATFORM error,
	// not an agent failure: the runner must exit 2, write an error event plus a
	// step.end with status=error, AND persist results.json carrying the real
	// failure reason so ingestion does not degrade the metrics row to the
	// generic "flight recorder output missing" summary (review finding,
	// 2026-07-12). The distinction between "sidecar never came up" and "agent
	// ran and failed" only exists in this path.
	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	// claude must never run when a sidecar is unhealthy; point at a stub that
	// fails the test if invoked.
	claude := filepath.Join(dir, "claude")
	tripwire := filepath.Join(dir, "claude-ran")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n: > '"+tripwire+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// waitHealthy polls until sidecarHealthTimeout (60s) or ctx.Done; a short
	// ctx deadline exits the poll via ctx.Done in ~50ms without waiting out the
	// health timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	exit, err := runner.Run(ctx, runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "wire-check", ClaudePath: claude,
		MCPServers: map[string]string{"platform": unhealthy.URL + "/mcp"},
		Stdout:     new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 2 {
		t.Fatalf("expected exit 2 (platform error), got %d", exit)
	}
	if _, err := os.Stat(tripwire); err == nil {
		t.Fatal("claude was invoked despite an unhealthy sidecar")
	}

	// results.json must exist with status=error and the real reason, or the
	// ingested metrics row loses the failure cause (finding 2).
	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatalf("results.json must be written on the sidecar-failure path: %v", err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusError {
		t.Fatalf("results.json status = %s, want error", results.Status)
	}
	if results.Validate() != nil {
		t.Fatalf("results.json must satisfy schema.Validate (ingestion requires it): %v", results.Validate())
	}
	if !strings.Contains(results.Summary, "never became healthy") {
		t.Fatalf("results.json summary = %q, want the sidecar-failure reason", results.Summary)
	}

	// Event stream: step.start, error, step.end(status=error).
	events := readEvents(t, flight)
	var types []schema.EventType
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := []schema.EventType{schema.EventStepStart, schema.EventError, schema.EventStepEnd}
	if len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Fatalf("expected %v, got %v", want, types)
	}
	var end schema.StepEndData
	if err := json.Unmarshal(events[2].Data, &end); err != nil {
		t.Fatal(err)
	}
	if end.Status != schema.RunStatusError {
		t.Fatalf("step.end status = %q, want %q", end.Status, schema.RunStatusError)
	}
	if !strings.Contains(end.Summary, "never became healthy") {
		t.Fatalf("step.end summary = %q, want the sidecar-failure reason", end.Summary)
	}
}

func TestFromEnvDiscoversMCPServersAndPromptConfig(t *testing.T) {
	// The §8.1 rows the agent-step exec feeds straight into Run: the
	// ^([A-Z]+)_MCP_URL$ discovery scan, AGENT_MAX_TURNS parsing, and the
	// prompt/model/schema/flight-dir keys. None of these were covered.
	t.Setenv("AGENT_PROMPT", "do the thing")
	t.Setenv("AGENT_PROMPT_FILE", "repo/prompt.txt")
	t.Setenv("AGENT_MODEL", "claude-opus-4-8")
	t.Setenv("AGENT_MAX_TURNS", "12")
	t.Setenv("AGENT_OUTPUT_SCHEMA", "repo/schemas/spec.json")
	t.Setenv("AGENT_FLIGHT_DIR", "/work/flight")
	t.Setenv("DEV_MCP_URL", "http://127.0.0.1:7780/mcp")
	t.Setenv("PLATFORM_MCP_URL", "http://127.0.0.1:7781/mcp")

	cfg := runner.FromEnv()

	if cfg.Prompt != "do the thing" {
		t.Errorf("Prompt = %q", cfg.Prompt)
	}
	if cfg.PromptFile != "repo/prompt.txt" {
		t.Errorf("PromptFile = %q", cfg.PromptFile)
	}
	if cfg.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.MaxTurns != 12 {
		t.Errorf("MaxTurns = %d, want 12", cfg.MaxTurns)
	}
	if cfg.OutputSchema != "repo/schemas/spec.json" {
		t.Errorf("OutputSchema = %q", cfg.OutputSchema)
	}
	if cfg.FlightDir != "/work/flight" {
		t.Errorf("FlightDir = %q", cfg.FlightDir)
	}
	// Discovery lower-cases the ^([A-Z]+)_MCP_URL$ prefix into the server key.
	if got := cfg.MCPServers["dev"]; got != "http://127.0.0.1:7780/mcp" {
		t.Errorf("MCPServers[dev] = %q, want the DEV_MCP_URL value", got)
	}
	if got := cfg.MCPServers["platform"]; got != "http://127.0.0.1:7781/mcp" {
		t.Errorf("MCPServers[platform] = %q, want the PLATFORM_MCP_URL value", got)
	}
}

func TestFromEnvMalformedMaxTurnsIsAbsent(t *testing.T) {
	t.Setenv("AGENT_MAX_TURNS", "lots")
	if got := runner.FromEnv().MaxTurns; got != 0 {
		t.Errorf("malformed AGENT_MAX_TURNS should read as 0, got %d", got)
	}
}

func TestRunResolvesPromptFromFile(t *testing.T) {
	// When no inline prompt is set, the runner reads AGENT_PROMPT_FILE relative
	// to the workdir and passes its contents to claude.
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	promptText := "hello from the prompt file"
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(promptText), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stub claude records its argv so the test can prove the file contents were
	// passed through as the -p prompt.
	argsFile := filepath.Join(dir, "claude-args")
	envelope := `{"type":"result","subtype":"success","result":"\"ok\"","model":"m1","is_error":false}`
	claude := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > '" + argsFile + "'\necho '" + envelope + "'\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	exit, err := runner.Run(context.Background(), runner.Config{
		PromptFile: "prompt.txt", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), promptText) {
		t.Fatalf("claude argv %q did not include the prompt file contents %q", string(args), promptText)
	}
}

func TestRunPromptFileMissingIsPlatformError(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	exit, err := runner.Run(context.Background(), runner.Config{
		PromptFile: "no-such-file.txt", FlightDir: flight, WorkDir: dir, StepName: "s",
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
	if err == nil || !strings.Contains(err.Error(), "read prompt file") {
		t.Fatalf("expected a read prompt file error, got %v", err)
	}
}

func TestRunNoPromptConfiguredIsPlatformError(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	exit, err := runner.Run(context.Background(), runner.Config{
		FlightDir: flight, WorkDir: dir, StepName: "s",
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
	if err == nil || !strings.Contains(err.Error(), "no prompt configured") {
		t.Fatalf("expected a no-prompt error, got %v", err)
	}
}

func TestRunWarnsWhenOutputSchemaUnenforced(t *testing.T) {
	// output_schema is plumbed end-to-end but not yet enforced; declaring it
	// must produce a visible warning rather than silently no-op (review
	// finding, 2026-07-12).
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude := writeStubClaude(t, dir, `{"type":"result","subtype":"success","result":"\"ok\"","model":"m1","is_error":false}`)

	var stderr bytes.Buffer
	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", OutputSchema: "repo/schemas/spec.json",
		FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Stdout: new(bytes.Buffer), Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}
	if !strings.Contains(stderr.String(), "output_schema") || !strings.Contains(stderr.String(), "not yet enforced") {
		t.Fatalf("expected an output_schema-not-enforced warning on stderr, got %q", stderr.String())
	}
}
