// Package runner is the deterministic agent-step pod entrypoint: it waits
// for the declared MCP sidecars to become healthy, invokes the claude CLI,
// and writes the flight recorder (flight/events.ndjson + flight/results.json).
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

// Sidecar health-check cadence (§8.5: every MCP sidecar exposes GET /healthz).
var (
	sidecarHealthInterval = 2 * time.Second
	sidecarHealthTimeout  = 60 * time.Second
)

// claudeWaitDelay bounds how long cmd.Wait may block on claude's inherited
// stdout/stderr pipes after claude itself is dead (ctx cancellation) or has
// exited. Claude routinely leaks descendants (tool-call subprocesses) that
// inherit the pipe; without this bound cmd.Run blocks until the last one
// exits. On the terminal-end kill path (step timeout / build abort — the
// jetbridge exec SIGTERMs the pod's supervised process group, waits a 10s
// grace, then group-SIGKILLs) a blocked runner is SIGKILLed before it writes
// step.end/results.json, and the ingested row degrades to the zero-cost,
// no-step.end error row the kill exists to prevent (review finding,
// 2026-07-12). Must stay comfortably below that 10s grace and below the §3.2
// park exit grace (AGENT_PARK_EXIT_GRACE_SECONDS, default 30).
var claudeWaitDelay = 5 * time.Second

const maxSummaryChars = 500

// Config drives one agent-step execution.
type Config struct {
	Prompt       string
	PromptFile   string
	Model        string
	MaxTurns     int
	OutputSchema string
	FlightDir    string
	WorkDir      string
	StepName     string
	ClaudePath   string
	MCPServers   map[string]string
	Stdout       io.Writer
	Stderr       io.Writer

	// Step identity for the step.start payload (shared-contracts §5):
	// build_id and plan_id are the correlation key consumers use to join
	// the event stream back to its agent_run_metrics row, so they are NOT
	// optional. The remaining fields are the optional ticket/workflow/budget
	// tags; zero values mean absent (pure-CI step).
	BuildID         int
	PlanID          string
	TicketID        int
	WorkflowName    string
	WorkflowVersion int
	WorkflowHash    string
	BudgetSliceUSD  float64
}

var mcpURLPattern = regexp.MustCompile(`^([A-Z]+)_MCP_URL$`)

// FromEnv builds a Config from the §8.1 environment contract set by the
// agent-step exec. MCP servers are discovered by scanning the environment
// for variables matching ^([A-Z]+)_MCP_URL$ (DEV_MCP_URL -> key "dev").
func FromEnv() Config {
	wd, _ := os.Getwd()

	cfg := Config{
		Prompt:       os.Getenv("AGENT_PROMPT"),
		PromptFile:   os.Getenv("AGENT_PROMPT_FILE"),
		Model:        os.Getenv("AGENT_MODEL"),
		OutputSchema: os.Getenv("AGENT_OUTPUT_SCHEMA"),
		FlightDir:    os.Getenv("AGENT_FLIGHT_DIR"),
		StepName:     os.Getenv("AGENT_STEP_NAME"),
		WorkDir:      wd,
		MCPServers:   map[string]string{},

		// §5 step.start identity: BUILD_ID is jetbridge/exec-injected;
		// AGENT_PLAN_ID is set by the agent-step exec (never public YAML);
		// AGENT_TICKET_ID and AGENT_WORKFLOW_* arrive via renderer-emitted
		// plan env, empty for pure-CI steps.
		PlanID:          os.Getenv("AGENT_PLAN_ID"),
		BuildID:         envInt("BUILD_ID"),
		TicketID:        envInt("AGENT_TICKET_ID"),
		WorkflowName:    os.Getenv("AGENT_WORKFLOW_NAME"),
		WorkflowVersion: envInt("AGENT_WORKFLOW_VERSION"),
		WorkflowHash:    os.Getenv("AGENT_WORKFLOW_HASH"),
	}

	if v := os.Getenv("AGENT_BUDGET_SLICE_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.BudgetSliceUSD = f
		}
	}

	if v := os.Getenv("AGENT_MAX_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxTurns = n
		}
	}

	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if m := mcpURLPattern.FindStringSubmatch(name); m != nil {
			cfg.MCPServers[strings.ToLower(m[1])] = value
		}
	}

	return cfg
}

// envInt parses name as a positive integer; anything else (unset, empty,
// malformed, non-positive) means absent and returns 0.
func envInt(name string) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Run executes one agent step: resolve the prompt, wait for sidecars,
// invoke claude, and write the flight recorder. The returned exit code
// follows the step contract: 0 = pass, 2 = platform error (including
// claude CLI errors). A non-nil error means the runner itself broke
// before the outcome could be recorded.
func Run(ctx context.Context, cfg Config) (int, error) {
	start := time.Now()

	stdout := cfg.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	claudePath := cfg.ClaudePath
	if claudePath == "" {
		claudePath = "claude"
	}

	// output_schema is plumbed end-to-end (config -> AgentPlan ->
	// AGENT_OUTPUT_SCHEMA -> Config.OutputSchema) but the runner does not yet
	// validate the claude result against it. Warn loudly rather than silently
	// ignore the field, so a user declaring output_schema is not misled into
	// believing the result is being enforced (review finding, 2026-07-12).
	if cfg.OutputSchema != "" {
		fmt.Fprintf(stderr, "agent-runner: warning: output_schema %q is declared but not yet enforced; the claude result is not validated against it\n", cfg.OutputSchema)
	}

	// 1. Resolve the prompt: inline wins, else artifact-relative file.
	prompt := cfg.Prompt
	if prompt == "" && cfg.PromptFile != "" {
		raw, err := os.ReadFile(filepath.Join(cfg.WorkDir, cfg.PromptFile))
		if err != nil {
			return 2, fmt.Errorf("read prompt file: %w", err)
		}
		prompt = string(raw)
	}
	if prompt == "" {
		return 2, errors.New("no prompt configured")
	}

	// Open the flight recorder up-front so every exit path can honor the
	// contract that the event stream starts with step.start and ends with
	// step.end.
	if err := os.MkdirAll(cfg.FlightDir, 0o755); err != nil {
		return 2, fmt.Errorf("create flight dir: %w", err)
	}
	eventsFile, err := os.Create(filepath.Join(cfg.FlightDir, "events.ndjson"))
	if err != nil {
		return 2, fmt.Errorf("open events.ndjson: %w", err)
	}
	defer eventsFile.Close()
	events := schema.NewEventWriter(eventsFile)

	// step.start carries the full §5 identity payload: build_id and plan_id
	// are the correlation key consumers (scorecards, process-intel drill-down)
	// use to join this event stream back to its agent_run_metrics row, so
	// they must never be left zero (review finding, 2026-07-12).
	startData := schema.StepStartData{
		StepName:       cfg.StepName,
		BuildID:        cfg.BuildID,
		PlanID:         cfg.PlanID,
		WorkflowName:   cfg.WorkflowName,
		WorkflowHash:   cfg.WorkflowHash,
		BudgetSliceUSD: cfg.BudgetSliceUSD,
	}
	if cfg.TicketID > 0 {
		ticketID := cfg.TicketID
		startData.TicketID = &ticketID
	}
	if cfg.WorkflowVersion > 0 {
		workflowVersion := cfg.WorkflowVersion
		startData.WorkflowVersion = &workflowVersion
	}
	writeEvent(events, schema.EventStepStart, startData)

	// 2. Wait for every declared MCP sidecar to become healthy. A sidecar
	// that never comes up is a platform error, not an agent failure.
	if err := waitForSidecars(ctx, cfg.MCPServers); err != nil {
		summary := truncate(err.Error(), maxSummaryChars)
		writeEvent(events, schema.EventError, map[string]string{"message": err.Error()})
		// Persist results.json too. Server-side ingestion reads step.end only
		// for WallTimeSeconds — never its Status/Summary — and falls back to
		// results.json for the run's status/summary; without this write the
		// metrics row degrades to the generic "flight recorder output missing"
		// summary and the real sidecar-failure reason never surfaces (review
		// finding, 2026-07-12).
		writeResults(cfg.FlightDir, schema.StatusError, summary)
		writeEvent(events, schema.EventStepEnd, schema.StepEndData{
			StepName:        cfg.StepName,
			Status:          schema.RunStatusError,
			Summary:         summary,
			WallTimeSeconds: int(time.Since(start).Seconds()),
		})
		return 2, nil
	}

	// 4. Invoke the claude CLI.
	args := []string{"-p", prompt, "--output-format", "json"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxTurns))
	}
	args = append(args, "--dangerously-skip-permissions")

	if len(cfg.MCPServers) > 0 {
		mcpConfigPath, err := writeMCPConfig(cfg.MCPServers)
		if err != nil {
			return 2, fmt.Errorf("write mcp config: %w", err)
		}
		defer os.Remove(mcpConfigPath)
		args = append(args, "--mcp-config", mcpConfigPath)
	}

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, claudePath, args...)
	// Own process group: a severed exec session tears down the pod's pty and
	// the kernel HUPs the pty's FOREGROUND group. The supervisor's
	// `trap '' HUP` shield only protects processes that keep the inherited
	// ignore — claude (Node) installs its own SIGHUP handling and dies with
	// the pty, killing the run a web restart should have resumed. Outside
	// the foreground group the HUP never reaches it. Terminal-end teardown
	// still works: the group TERM reaches agent-runner, whose cancelled
	// context kills claude directly by pid (and the pod GC reaper bounds the
	// escape window if agent-runner itself is SIGKILLed first).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancellation tears down claude's WHOLE detached group, not just its
	// pid: claude routinely leaks tool subprocesses, and with Setpgid the
	// supervisor's terminal-end group kill can no longer reach them — the
	// shared pgid this replaced used to be the safety net (native review
	// finding, agent-review-native #3).
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Dir = cfg.WorkDir
	cmd.Stdout = io.MultiWriter(&buf, stdout)
	cmd.Stderr = stderr
	cmd.WaitDelay = claudeWaitDelay
	runErr := cmd.Run()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		// ErrWaitDelay is only returned when Wait would otherwise return
		// nil: claude exited 0 and its envelope (if any) is already in buf —
		// the error just says a leaked descendant held the stdout pipe past
		// the drain bound. The envelope is the authoritative outcome; a
		// missing/garbled one still degrades to status error via parseErr.
		runErr = nil
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "agent-runner: claude: %v\n", runErr)
	}

	// 5. Parse the last non-empty stdout line as the CLI envelope,
	// tolerating leading non-JSON output.
	env, parseErr := parseEnvelope(buf.Bytes())

	writeEvent(events, schema.EventCostRecord, schema.CostRecordData{
		Source:              "agent_step",
		Provider:            "anthropic",
		Model:               env.Model,
		InputTokens:         env.Usage.InputTokens,
		OutputTokens:        env.Usage.OutputTokens,
		CacheReadTokens:     env.Usage.CacheReadInputTokens,
		CacheCreationTokens: env.Usage.CacheCreationInputTokens,
		Turns:               env.NumTurns,
		CostUSD:             env.ResolvedCostUSD(),
	})

	// 6. Map the outcome onto the results.json wire status.
	status := schema.StatusPass
	if runErr != nil || parseErr != nil || env.IsError {
		status = schema.StatusError
	}
	exitCode := 0
	if status == schema.StatusError {
		exitCode = 2
	}

	summary := summaryFromResult(env.Result)
	if summary == "" {
		switch {
		case runErr != nil:
			summary = runErr.Error()
		case parseErr != nil:
			summary = fmt.Sprintf("unparseable claude output: %v", parseErr)
		default:
			summary = "(no result)"
		}
	}
	summary = truncate(summary, maxSummaryChars)

	results := schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return 2, fmt.Errorf("marshal results: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.FlightDir, "results.json"), resultsJSON, 0o644); err != nil {
		return 2, fmt.Errorf("write results.json: %w", err)
	}

	// 7. Close out the event stream.
	threeWay, _ := schema.ThreeWayStatus(status)
	writeEvent(events, schema.EventStepEnd, schema.StepEndData{
		StepName:        cfg.StepName,
		Status:          threeWay,
		Summary:         summary,
		WallTimeSeconds: int(time.Since(start).Seconds()),
		CostUSD:         env.ResolvedCostUSD(),
		Turns:           env.NumTurns,
	})

	return exitCode, nil
}

// waitForSidecars polls GET <url with /mcp replaced by /healthz> for every
// declared MCP server until each responds 200, checking in deterministic
// (sorted-name) order.
func waitForSidecars(ctx context.Context, servers map[string]string) error {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	client := &http.Client{Timeout: sidecarHealthInterval}
	for _, name := range names {
		if err := waitHealthy(ctx, client, name, healthzURL(servers[name])); err != nil {
			return err
		}
	}
	return nil
}

func healthzURL(mcpURL string) string {
	return strings.TrimSuffix(mcpURL, "/mcp") + "/healthz"
}

func waitHealthy(ctx context.Context, client *http.Client, name, url string) error {
	deadline := time.Now().Add(sidecarHealthTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar %s never became healthy", name)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("sidecar %s never became healthy", name)
		case <-time.After(sidecarHealthInterval):
		}
	}
}

// writeMCPConfig writes the claude CLI --mcp-config file mapping each
// declared sidecar to its HTTP MCP endpoint.
func writeMCPConfig(servers map[string]string) (string, error) {
	type serverEntry struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	entries := make(map[string]serverEntry, len(servers))
	for name, url := range servers {
		entries[name] = serverEntry{Type: "http", URL: url}
	}
	payload, err := json.Marshal(map[string]any{"mcpServers": entries})
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp("", "agent-runner-mcp-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// parseEnvelope finds the last non-empty line of the CLI's stdout and
// parses it as the --output-format json envelope.
func parseEnvelope(out []byte) (schema.CLIEnvelope, error) {
	var env schema.CLIEnvelope

	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return schema.CLIEnvelope{}, fmt.Errorf("parse CLI envelope: %w", err)
		}
		return env, nil
	}
	return schema.CLIEnvelope{}, errors.New("no CLI output to parse")
}

// summaryFromResult extracts a human-readable summary from the envelope's
// result field. The CLI encodes the agent's textual output as a JSON string;
// unquote it once when possible.
func summaryFromResult(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var unquoted string
		if json.Unmarshal(raw, &unquoted) == nil {
			return unquoted
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// writeResults persists a minimal, valid results.json for an early-exit path
// (e.g. sidecar-health failure) so ingestion can read the real Status/Summary
// instead of degrading to "flight recorder output missing". Best-effort: the
// step.end event is the primary contract, so a failed write must not change
// the runner's exit code.
func writeResults(flightDir string, status schema.Status, summary string) {
	results := schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(flightDir, "results.json"), raw, 0o644)
}

// writeEvent marshals data and appends it to the flight recorder,
// best-effort: an unwritable event must not abort the run itself.
func writeEvent(w *schema.EventWriter, t schema.EventType, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = w.Write(schema.Event{Type: t, Data: raw})
}
