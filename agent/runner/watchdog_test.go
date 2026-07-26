package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

// writeRunawayClaude writes a stub CLI that leaks a background descendant,
// records its pid, emits the given stream-json lines, and then NEVER exits —
// the shape of a claude that ignores its own --max-budget-usd/--max-turns.
//
// The trailing wait is `exec sleep`, not a forked `sleep`: the budget and turn
// arms trip the instant the cost/turn line is read, which is exactly when a
// plain `sleep 60` would be forking. killpg only reaches processes that exist
// when it is called, so a child forked in that window escapes the group kill
// and outlives the test. exec replaces the shell in place — no fork, nothing
// to race — leaving only processes that were already in the group. (The same
// escape window exists in production; the pod GC reaper is what bounds it, as
// runner.go's cancel comment notes.)
func writeRunawayClaude(t *testing.T, dir string, lines ...string) (claudePath, grandchildPidPath string) {
	t.Helper()
	claudePath = filepath.Join(dir, "claude")
	grandchildPidPath = filepath.Join(dir, "grandchild-pid")
	script := "#!/bin/sh\nsleep 60 >/dev/null 2>&1 &\necho $! > '" + grandchildPidPath + "'\n"
	for _, line := range lines {
		script += "echo '" + line + "'\n"
	}
	script += "exec sleep 60\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return claudePath, grandchildPidPath
}

func readResults(t *testing.T, flightDir string) schema.Results {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("decode results.json (%s): %v", raw, err)
	}
	return results
}

// expectDeadWithin reuses the descendant-kill test's polling technique: poll
// kill(pid, 0) until it fails, proving the whole process GROUP went down.
func expectDeadWithin(t *testing.T, pidFile string, within time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("stub never recorded its descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing descendant pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL) // don't leak it past the test
	t.Fatalf("claude's leaked descendant (pid %d) survived the watchdog kill", pid)
}

func runWatchdogCase(t *testing.T, cfg runner.Config, claude, flight string) (int, time.Duration, schema.Results) {
	t.Helper()
	cfg.ClaudePath = claude
	cfg.FlightDir = flight
	cfg.Stdout = new(bytes.Buffer)
	cfg.Stderr = new(bytes.Buffer)

	start := time.Now()
	exit, err := runner.Run(context.Background(), cfg)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return exit, elapsed, readResults(t, flight)
}

func TestWatchdogKillsARunawayThatBreachesItsBudget(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir,
		`{"type":"assistant"}`,
		`{"type":"result","subtype":"progress","total_cost_usd":2.5}`)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", BudgetSliceUSD: 1.0,
	}, claude, flight)

	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (platform error)", exit)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("Run took %s — the never-exiting CLI was not killed", elapsed)
	}
	if results.Status != schema.StatusError {
		t.Fatalf("status = %q, want error", results.Status)
	}
	if got := results.Metadata["terminated_reason"]; got != "budget" {
		t.Fatalf("terminated_reason = %v, want \"budget\"", got)
	}
	if !strings.Contains(results.Summary, "budget cap") {
		t.Fatalf("summary = %q, want it to name the breached cap", results.Summary)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogKillsARunawayThatBreachesItsTurnCap(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir,
		`{"type":"assistant"}`, `{"type":"assistant"}`, `{"type":"assistant"}`)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", MaxTurns: 2,
	}, claude, flight)

	if exit != 2 || elapsed > 30*time.Second {
		t.Fatalf("exit = %d after %s, want 2 promptly", exit, elapsed)
	}
	if got := results.Metadata["terminated_reason"]; got != "turns" {
		t.Fatalf("terminated_reason = %v, want \"turns\"", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogKillsARunawayThatOutlivesItsWallClock(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", MaxWallClock: 300 * time.Millisecond,
	}, claude, flight)

	if exit != 2 || elapsed > 30*time.Second {
		t.Fatalf("exit = %d after %s, want 2 promptly", exit, elapsed)
	}
	if got := results.Metadata["terminated_reason"]; got != "wall_clock" {
		t.Fatalf("terminated_reason = %v, want \"wall_clock\"", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogLeavesAnOrdinaryRunAlone(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := writeStubClaude(t, dir, okEnvelope)

	exit, _, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "ordinary",
		MaxTurns: 50, BudgetSliceUSD: 100, MaxWallClock: time.Minute,
	}, claude, flight)

	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if results.Status != schema.StatusPass {
		t.Fatalf("status = %q, want pass", results.Status)
	}
	if _, found := results.Metadata["terminated_reason"]; found {
		t.Fatalf("an unbreached run recorded a termination reason: %+v", results.Metadata)
	}
}

// Reported cost and turn counts are CUMULATIVE-to-date totals. Summing them
// would trip the budget arm at a fraction of the real spend, so the fold must
// be max(), and this test is what says so.
func TestWatchdogFoldsCumulativeTotalsInsteadOfSummingThem(t *testing.T) {
	reason, cost, turns, killed := runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0},
		`{"type":"result","subtype":"progress","total_cost_usd":0.4}`,
		`{"type":"result","subtype":"progress","total_cost_usd":0.9}`,
	)
	if reason != "" || killed {
		t.Fatalf("cumulative 0.9 against a 1.0 cap tripped the watchdog (reason=%q, killed=%t)", reason, killed)
	}
	if cost != 0.9 {
		t.Fatalf("cost = %v, want 0.9", cost)
	}

	reason, cost, _, killed = runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0},
		`{"type":"result","subtype":"progress","total_cost_usd":0.9}`,
		`{"type":"result","subtype":"progress","total_cost_usd":1.5}`,
	)
	if reason != "budget" || !killed {
		t.Fatalf("cumulative 1.5 against a 1.0 cap did not trip (reason=%q, killed=%t)", reason, killed)
	}
	if cost != 1.5 {
		t.Fatalf("cost = %v, want 1.5", cost)
	}

	reason, _, turns, _ = runner.ObserveStreamLinesForTest(
		runner.Config{MaxTurns: 2},
		`{"type":"assistant"}`, `{"type":"assistant"}`, `{"type":"assistant"}`,
	)
	if reason != "turns" || turns != 3 {
		t.Fatalf("turn fold = (%q, %d), want (\"turns\", 3)", reason, turns)
	}

	// Malformed and non-JSON lines must never panic or corrupt the totals.
	if reason, _, _, _ = runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0}, `not json`, ``, `{"type":`,
	); reason != "" {
		t.Fatalf("garbage stream tripped the watchdog: %q", reason)
	}
}

func TestFromEnvReadsMaxWallClock(t *testing.T) {
	t.Setenv("AGENT_MAX_WALL_CLOCK", "90m")
	if got := runner.FromEnv().MaxWallClock; got != 90*time.Minute {
		t.Errorf("MaxWallClock = %s, want 90m", got)
	}
	t.Setenv("AGENT_MAX_WALL_CLOCK", "not-a-duration")
	if got := runner.FromEnv().MaxWallClock; got != 0 {
		t.Errorf("malformed MaxWallClock = %s, want 0 (absent)", got)
	}
}
