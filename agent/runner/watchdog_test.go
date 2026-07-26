package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// The fixtures below are the shapes the PINNED CLI actually emits under
// --output-format stream-json --verbose (claude-code 2.0.1, see
// deploy/agent-runner/Dockerfile; live capture in ci/dogfood/FINDINGS.md):
// one type:"assistant" line per model message, each carrying its own
// message.usage and a parent_tool_use_id that is non-null when the message is
// re-emitted from a Task subagent; interleaved type:"user" tool_result lines;
// and exactly ONE terminal type:"result" line, which is the only place cost
// ever appears. There is no mid-run cost line — an earlier revision of this
// file invented `subtype:"progress"` lines carrying total_cost_usd, and the
// budget arm's whole design rested on that fiction.

func assistantLine(id string, outputTokens int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_%s","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"working %s"}],"stop_reason":null,"usage":{"input_tokens":4,"cache_creation_input_tokens":120,"cache_read_input_tokens":900,"output_tokens":%d}},"parent_tool_use_id":null,"session_id":"s1"}`,
		id, id, outputTokens)
}

// subagentLine is the same message as it arrives when a Task subagent produced
// it: the CLI re-emits the sub-session's assistant messages into the parent
// stream, tagged with the tool_use id that spawned them (finding I2).
func subagentLine(id string, outputTokens int) string {
	return strings.Replace(assistantLine(id, outputTokens),
		`"parent_tool_use_id":null`, `"parent_tool_use_id":"toolu_01ABCDEF"`, 1)
}

func toolResultLine(id string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_%s","type":"tool_result","content":"ok"}]},"parent_tool_use_id":null,"session_id":"s1"}`, id)
}

// resultLine is the terminal envelope. The graceful budget and turn stops
// report is_error:false — that is why they cannot be told apart from a clean
// finish by status alone (live capture, ci/dogfood/FINDINGS.md).
func resultLine(subtype string, costUSD float64, numTurns int) string {
	return fmt.Sprintf(`{"type":"result","subtype":%q,"is_error":false,"duration_ms":1200,"num_turns":%d,"result":"done","session_id":"s1","total_cost_usd":%g,"usage":{"input_tokens":10,"cache_creation_input_tokens":120,"cache_read_input_tokens":900,"output_tokens":50}}`,
		subtype, numTurns, costUSD)
}

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

// writeExitingClaude writes a stub that emits the given lines and then exits 0
// on its own — a CLI that ended the run itself.
func writeExitingClaude(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n"
	for _, line := range lines {
		script += "echo '" + line + "'\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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

// partialUsage pulls the metadata block a watchdog-killed run records.
func partialUsage(t *testing.T, results schema.Results) map[string]any {
	t.Helper()
	block, found := results.Metadata["partial_usage"]
	if !found {
		t.Fatalf("no partial_usage in metadata: %+v", results.Metadata)
	}
	usage, ok := block.(map[string]any)
	if !ok {
		t.Fatalf("partial_usage is %T, want an object", block)
	}
	return usage
}

func usageCount(t *testing.T, usage map[string]any, key string) float64 {
	t.Helper()
	n, ok := usage[key].(float64)
	if !ok {
		t.Fatalf("partial_usage[%q] = %v (%T), want a number", key, usage[key], usage[key])
	}
	return n
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

// runWatchdogCase runs one step. Stdout/Stderr are left alone when the caller
// set them, so a test can inspect the step log or break it on purpose.
func runWatchdogCase(t *testing.T, cfg runner.Config, claude, flight string) (int, time.Duration, schema.Results) {
	t.Helper()
	cfg.ClaudePath = claude
	cfg.FlightDir = flight
	if cfg.Stdout == nil {
		cfg.Stdout = new(bytes.Buffer)
	}
	if cfg.Stderr == nil {
		cfg.Stderr = new(bytes.Buffer)
	}

	start := time.Now()
	exit, err := runner.Run(context.Background(), cfg)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return exit, elapsed, readResults(t, flight)
}

func flightDir(t *testing.T) (dir, flight string) {
	t.Helper()
	dir = t.TempDir()
	flight = filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, flight
}

// The turn arm is real mid-flight containment: assistant messages are visible
// while the run is still spending. It is also the arm that proves the partial
// accounting — the run is killed long before the cost-bearing terminal line
// exists, so the tokens accumulated from the stream are the ONLY spend signal
// the step can report.
func TestWatchdogKillsARunawayThatBreachesItsTurnCap(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir, flight := flightDir(t)
	claude, grandchild := writeRunawayClaude(t, dir,
		assistantLine("1", 30), toolResultLine("1"),
		assistantLine("2", 40), toolResultLine("2"),
		assistantLine("3", 50))

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", MaxTurns: 2,
	}, claude, flight)

	if exit != 2 || elapsed > 30*time.Second {
		t.Fatalf("exit = %d after %s, want 2 promptly", exit, elapsed)
	}
	if got := results.Metadata["terminated_reason"]; got != "turns" {
		t.Fatalf("terminated_reason = %v, want \"turns\"", got)
	}
	if results.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0 — a terminated run asserts nothing", results.Confidence)
	}

	// Real tokens, accumulated from the live stream, instead of the flat zero
	// a killed run used to ingest.
	usage := partialUsage(t, results)
	if got := usageCount(t, usage, "output_tokens"); got != 120 {
		t.Errorf("partial output_tokens = %v, want 120 (30+40+50)", got)
	}
	if got := usageCount(t, usage, "cache_read_tokens"); got != 2700 {
		t.Errorf("partial cache_read_tokens = %v, want 2700 (3 x 900)", got)
	}
	if got := usageCount(t, usage, "usage_messages"); got != 3 {
		t.Errorf("partial usage_messages = %v, want 3", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogKillsARunawayThatOutlivesItsWallClock(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir, flight := flightDir(t)
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

// The budget arm is a TERMINAL-ENVELOPE CROSS-CHECK, not containment: cost
// only ever reaches the stream on the final result line. Its one remaining job
// is this shape — a CLI that reported a cost over the cap and then did not go
// away.
func TestWatchdogCrossChecksACostOverTheCapWhenTheCLIWillNotExit(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir, flight := flightDir(t)
	claude, grandchild := writeRunawayClaude(t, dir,
		assistantLine("1", 30),
		resultLine("success", 2.5, 6))

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

// NEGATIVE CONTROL for the attribution rule. Same terminal envelope, same
// breached cap — but the CLI exits on its own, which is what the CLI's OWN
// graceful --max-budget-usd stop does (it prints exactly this line, with
// is_error:false, and leaves). The watchdog's arm still trips, because the arm
// cannot see the difference; the runner must, or every graceful budget stop is
// laundered into a platform kill: status error, exit 2, the agent's summary
// replaced.
func TestWatchdogDoesNotAttributeAGracefulCLIStopToThePlatform(t *testing.T) {
	dir, flight := flightDir(t)
	claude := writeExitingClaude(t, dir,
		assistantLine("1", 30),
		resultLine("error_max_budget_usd", 2.5, 6))

	stderr := new(bytes.Buffer)
	exit, _, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "graceful", BudgetSliceUSD: 1.0,
		Stderr: stderr,
	}, claude, flight)

	if reason, found := results.Metadata["terminated_reason"]; found {
		t.Fatalf("a self-exiting CLI was attributed to the platform: terminated_reason = %v", reason)
	}
	if results.Summary != "done" {
		t.Fatalf("summary = %q, want the CLI's own result — the platform must not overwrite it", results.Summary)
	}
	if results.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1 — the run completed on its own terms", results.Confidence)
	}
	if results.Status != schema.StatusPass || exit != 0 {
		t.Fatalf("status/exit = %q/%d, want pass/0 — the CLI reported is_error:false", results.Status, exit)
	}
	// The arm DID trip: this is a suppressed attribution, not an arm that
	// never fired, and the step log has to say so.
	if !strings.Contains(stderr.String(), "not a platform kill") {
		t.Fatalf("stderr does not record the suppressed trip:\n%s", stderr.String())
	}
}

// The watchdog rides the stdout MultiWriter, which stops at the first writer
// that errors. Last in that slice, a broken step log silently blinded the only
// bound on the run's spend while claude kept going (finding I5).
func TestWatchdogStillSeesTheStreamWhenTheStepLogWriterFails(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir, flight := flightDir(t)
	claude, grandchild := writeRunawayClaude(t, dir, resultLine("success", 2.5, 6))

	// MaxWallClock is only the test's own backstop: a blinded watchdog would
	// otherwise hang on the never-exiting stub instead of failing.
	_, _, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "blinded",
		BudgetSliceUSD: 1.0, MaxWallClock: 5 * time.Second,
		Stdout: brokenWriter{},
	}, claude, flight)

	if got := results.Metadata["terminated_reason"]; got != "budget" {
		t.Fatalf("terminated_reason = %v, want \"budget\" — the broken step log blinded the watchdog", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

// brokenWriter is a step log that is already gone: a severed exec's closed
// stream, or an EPIPE on os.Stdout.
type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, errors.New("step log is gone") }

func TestWatchdogLeavesAnOrdinaryRunAlone(t *testing.T) {
	dir, flight := flightDir(t)
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
	if results.Confidence != 1 {
		t.Fatalf("confidence = %v, want 1", results.Confidence)
	}
}

// A Task-dispatching run re-emits every subagent assistant message into the
// parent stream. Counting those as parent turns overcounts without bound — a
// single Task can emit dozens — and kills healthy runs (finding I2). Their
// USAGE is still real spend and is still accumulated.
func TestWatchdogTurnFloorIgnoresSubagentMessages(t *testing.T) {
	lines := make([]string, 0, 31)
	lines = append(lines, assistantLine("0", 10)) // the parent's own turn
	for i := 0; i < 30; i++ {
		lines = append(lines, subagentLine(strconv.Itoa(i+1), 10))
	}

	run := runner.ObserveStreamLinesForTest(runner.Config{MaxTurns: 5}, lines...)
	if run.Reason != "" || run.Kills != 0 {
		t.Fatalf("30 subagent messages against a 5-turn cap killed a healthy run (reason=%q, kills=%d)", run.Reason, run.Kills)
	}
	if run.Turns != 1 {
		t.Fatalf("turns = %d, want 1 — only the parent's own message is a turn", run.Turns)
	}
	if run.UsageMessages != 31 || run.OutputTokens != 310 {
		t.Fatalf("usage = %d messages / %d output tokens, want 31/310 — subagent spend is real spend",
			run.UsageMessages, run.OutputTokens)
	}

	// The floor still counts the parent's own messages.
	run = runner.ObserveStreamLinesForTest(runner.Config{MaxTurns: 2},
		assistantLine("1", 10), assistantLine("2", 10), assistantLine("3", 10))
	if run.Reason != "turns" || run.Turns != 3 {
		t.Fatalf("turn fold = (%q, %d), want (\"turns\", 3)", run.Reason, run.Turns)
	}
}

// Cost is folded with max(), never summed and never last-line-wins. An honest
// run emits one terminal envelope, but claude's stdout pipe is inherited by
// every tool subprocess it leaks, so extra "result" lines are reachable:
// summing would trip the arm on a forged line, and last-wins would let a
// forged zero mask a real spend. Same anti-tamper semantics as atc/exec's
// agentCostObserver.
func TestWatchdogFoldsCostWithMaxNeverSumOrLastWins(t *testing.T) {
	run := runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 1.0},
		resultLine("success", 0.4, 2), resultLine("success", 0.9, 4))
	if run.Reason != "" || run.Kills != 0 {
		t.Fatalf("0.4 + 0.9 summed against a 1.0 cap tripped the watchdog (reason=%q)", run.Reason)
	}
	if run.CostUSD != 0.9 {
		t.Fatalf("cost = %v, want 0.9", run.CostUSD)
	}

	run = runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 1.0},
		resultLine("success", 0.9, 4), resultLine("success", 1.5, 6))
	if run.Reason != "budget" || run.Kills != 1 {
		t.Fatalf("cumulative 1.5 against a 1.0 cap did not trip (reason=%q, kills=%d)", run.Reason, run.Kills)
	}
	if run.CostUSD != 1.5 {
		t.Fatalf("cost = %v, want 1.5", run.CostUSD)
	}

	// A later, cheaper line cannot lower what was already observed.
	run = runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 10.0},
		resultLine("success", 3.0, 6), resultLine("success", 0, 1))
	if run.CostUSD != 3.0 {
		t.Fatalf("cost = %v, want 3.0 — a forged zero must not lower the observation", run.CostUSD)
	}

	// Malformed and non-JSON lines must never panic or corrupt the totals.
	run = runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 1.0},
		`not json`, ``, `{"type":`, `{"type":"assistant"`)
	if run.Reason != "" || run.CostUSD != 0 {
		t.Fatalf("garbage stream tripped the watchdog: reason=%q cost=%v", run.Reason, run.CostUSD)
	}
}

// The CLI's own num_turns rides the terminal envelope, and on the cap-stop
// shape it is the ONLY turn signal — a run whose assistant messages the
// watchdog never saw (a reattach, a blinded window) still has to be caught.
func TestWatchdogFoldsNumTurnsFromTheTerminalEnvelope(t *testing.T) {
	// The live capture from FINDINGS.md, verbatim in shape: 100 turns, $5.98,
	// is_error:false.
	run := runner.ObserveStreamLinesForTest(runner.Config{MaxTurns: 50},
		resultLine("error_max_turns", 5.98, 100))
	if run.Reason != "turns" || run.Kills != 1 {
		t.Fatalf("num_turns=100 against a 50-turn cap did not trip (reason=%q, kills=%d)", run.Reason, run.Kills)
	}
	if run.Turns != 100 {
		t.Fatalf("turns = %d, want 100 (the envelope's own count, not the assistant floor)", run.Turns)
	}

	// Unset cap: the fold still happens, the arm stays silent.
	run = runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 100},
		resultLine("error_max_turns", 5.98, 100))
	if run.Reason != "" || run.Turns != 100 {
		t.Fatalf("unconfigured turn cap = (%q, %d), want (\"\", 100)", run.Reason, run.Turns)
	}
}

// The legacy bare cost_usd fold is KEPT, deliberately. The pinned CLI (2.0.1)
// reports total_cost_usd in the only live capture we have
// (ci/dogfood/FINDINGS.md), so this shape may well be unreachable there — but
// one capture cannot prove a negative, and both other readers of these bytes
// still honor cost_usd: schema.CLIEnvelope.ResolvedCostUSD (which this
// watchdog now calls, instead of keeping a third copy of the rule) and, via
// it, the runner's own envelope parse and atc/exec's web-side cost floor.
// Deleting it here alone would make the watchdog blind to a cost the platform
// still charges for — the exact one-sided divergence schema/envelope.go exists
// to prevent.
func TestWatchdogFoldsLegacyCostUSDWhenTotalCostIsAbsent(t *testing.T) {
	legacy := `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"done","cost_usd":2.5,"usage":{"input_tokens":10,"output_tokens":50}}`

	run := runner.ObserveStreamLinesForTest(runner.Config{BudgetSliceUSD: 1.0}, legacy)
	if run.Reason != "budget" || run.Kills != 1 {
		t.Fatalf("bare cost_usd 2.5 against a 1.0 cap did not trip (reason=%q, kills=%d)", run.Reason, run.Kills)
	}
	if run.CostUSD != 2.5 {
		t.Fatalf("cost = %v, want 2.5", run.CostUSD)
	}
}

// First breach wins, and it kills once. results.json must name the reason that
// actually ended the run, not the last arm to notice.
func TestWatchdogKeepsTheFirstBreachAndKillsOnce(t *testing.T) {
	// Two arms breaching in sequence: the turn floor stops the run mid-flight,
	// and the CLI's terminal envelope — still in flight when the kill lands —
	// then breaches the budget cap too.
	run := runner.ObserveStreamLinesForTest(runner.Config{MaxTurns: 2, BudgetSliceUSD: 1.0},
		assistantLine("1", 10), assistantLine("2", 10), assistantLine("3", 10),
		resultLine("success", 9.99, 3))
	if run.Reason != "turns" {
		t.Fatalf("reason = %q, want \"turns\" — the breach that actually ended the run", run.Reason)
	}
	if run.Kills != 1 {
		t.Fatalf("kills = %d, want 1 — a second breach must not re-kill", run.Kills)
	}

	// Both caps breached by the SAME line: still exactly one reason, and the
	// arm order is deterministic.
	run = runner.ObserveStreamLinesForTest(runner.Config{MaxTurns: 5, BudgetSliceUSD: 1.0},
		resultLine("error_max_budget_usd", 2.5, 40))
	if run.Reason != "budget" || run.Kills != 1 {
		t.Fatalf("simultaneous breach = (%q, %d kills), want (\"budget\", 1)", run.Reason, run.Kills)
	}
}

// A line that never terminates is dropped at the bound, and the stream resyncs
// at the next newline instead of wedging the watchdog for the rest of the run.
func TestWatchdogResyncsAfterAnOversizedLine(t *testing.T) {
	oversized := strings.Repeat("x", runner.MaxStreamLineBytes+1)

	run := runner.ObserveStreamForTest(runner.Config{BudgetSliceUSD: 1.0}, nil,
		oversized, "\n", resultLine("success", 2.5, 6)+"\n")

	if run.Reason != "budget" || run.CostUSD != 2.5 {
		t.Fatalf("the line after an oversized one was not folded: reason=%q cost=%v", run.Reason, run.CostUSD)
	}
}

// A watchdog failure must never take down the run's accounting: the observe
// path runs on the goroutine copying claude's stdout, so an unrecovered panic
// there would kill agent-runner before it writes the flight recorder — losing
// the whole run's cost, tokens and outcome over a cap check (finding I4).
func TestWatchdogDisarmsInsteadOfCrashingTheRun(t *testing.T) {
	run := runner.ObserveStreamForTest(
		runner.Config{MaxTurns: 1, BudgetSliceUSD: 1.0},
		func() { panic("watchdog kill path is broken") },
		assistantLine("1", 10)+"\n",
		assistantLine("2", 10)+"\n", // trips the turn floor; the kill panics
		resultLine("success", 99, 8)+"\n",
	)

	if run.Reason != "turns" || run.Kills != 1 {
		t.Fatalf("the trip itself did not record: reason=%q kills=%d", run.Reason, run.Kills)
	}
	if !strings.Contains(run.Warnings, "DISARMED") {
		t.Fatalf("the contained panic was not reported:\n%s", run.Warnings)
	}
	if run.CostUSD != 0 {
		t.Fatalf("cost = %v, want 0 — a disarmed watchdog stops observing", run.CostUSD)
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
