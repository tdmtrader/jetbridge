package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

// Watchdog termination reasons, stamped into flight/results.json as
// metadata.terminated_reason so ingestion (and a human reading the row) can
// tell a platform-enforced cutoff from an agent failure. Run stamps one ONLY
// when the watchdog's kill actually preempted the CLI — see the attribution
// rule there.
const (
	TerminatedReasonBudget    = "budget"
	TerminatedReasonTurns     = "turns"
	TerminatedReasonWallClock = "wall_clock"
)

// maxStreamLineBytes bounds the partial-line buffer held while waiting for a
// newline. It matches atc/exec's maxObservedEnvelopeLineBytes (5 MiB) and for
// the same stated reason: the CLI's terminal envelope EMBEDS the agent's final
// result text, so a legitimate line is routinely far larger than a log line.
// The previous 1 MiB bound diverged from that observer and would drop exactly
// the terminal line the budget cross-check exists to read. A stream that
// produces no newline within the bound is malformed, so drop the pending bytes
// rather than grow without bound (the stream resyncs at the next newline).
const maxStreamLineBytes = 5 << 20

// watchdog is the PLATFORM-SIDE backstop for the caps handed to the claude CLI
// as --max-budget-usd / --max-turns, plus a wall-clock bound the CLI has no
// flag for. The CLI's own enforcement stays the first line; this exists
// because a CLI that ignores, mis-parses, or outlives its own flags must not
// be able to spend unbounded money or time.
//
// The three arms are NOT equivalent, because of what the stream actually
// carries (pinned CLI 2.0.1, deploy/agent-runner/Dockerfile; live capture in
// ci/dogfood/FINDINGS.md):
//
//   - turns and wall-clock are MID-FLIGHT CONTAINMENT. Assistant messages and
//     elapsed time are observable while the run is still spending, so these
//     arms can stop a runaway before it finishes.
//   - budget is a TERMINAL-ENVELOPE CROSS-CHECK, not containment. Cost appears
//     ONLY on the terminal type:"result" line (subtypes success,
//     error_max_turns, error_max_budget_usd, error_during_execution,
//     error_max_structured_output_retries); there is no mid-run cost line to
//     react to. By the time this arm can fire the money is already spent and
//     the CLI is normally exiting on its own, so its remaining job is the
//     narrow one of killing a CLI that REPORTED a cost over the cap and then
//     did not exit. Whether that kill is reported as a platform termination is
//     decided by Run's attribution rule, not here: the CLI's own graceful
//     --max-budget-usd stop prints this very envelope (with is_error:false)
//     and then exits, and stamping that as a platform kill would replace a
//     healthy run's status, exit code and summary with a lie.
//
// On the FIRST breach it kills claude through the same cancel path the step
// timeout and build abort already use (cmd.Cancel -> group SIGKILL), so leaked
// tool subprocesses die with it. The runner's own context is untouched, so the
// flight recorder is still written after a kill.
type watchdog struct {
	maxCostUSD float64
	maxTurns   int
	maxWall    time.Duration
	kill       func()
	warn       io.Writer

	// disarmed is atomic, not mu-guarded, precisely because it is set from a
	// recover(): a panic could have unwound while mu was held, and taking mu
	// there would deadlock the very accounting this fail-safe protects.
	disarmed atomic.Bool

	mu        sync.Mutex
	cost      float64
	turns     int
	assistant int
	usage     partialUsage
	reason    string
	detail    string
}

func newWatchdog(cfg Config, warn io.Writer, kill func()) *watchdog {
	if warn == nil {
		warn = io.Discard
	}
	return &watchdog{
		maxCostUSD: cfg.BudgetSliceUSD,
		maxTurns:   cfg.MaxTurns,
		maxWall:    cfg.MaxWallClock,
		kill:       kill,
		warn:       warn,
	}
}

// armed reports whether any cap is configured. An unarmed watchdog is never
// attached to the stream, so an unconfigured step pays nothing.
func (w *watchdog) armed() bool {
	return w.maxCostUSD > 0 || w.maxTurns > 0 || w.maxWall > 0
}

// tokenUsage is a CLI usage block, in either of the two places the stream
// carries one: `usage` on the terminal envelope (the run TOTAL) and
// `message.usage` on each type:"assistant" line (that message's increment).
type tokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func (u tokenUsage) empty() bool {
	return u == tokenUsage{}
}

// partialUsage is token usage summed from the live stream's per-message
// blocks. It exists for the shape the cost fields cannot cover: a run the
// watchdog kills has no terminal envelope, so cost_usd/total_cost_usd never
// arrive and the turn and wall-clock arms — the two that can actually stop a
// runaway mid-flight — would otherwise ingest a flat zero for a run that spent
// real money. Tokens are the only spend signal the live stream carries; they
// are recorded as tokens and NOT converted to dollars here (the runner has no
// price table, and a fabricated USD figure would enter agent_cost_ledger as if
// it were measured).
type partialUsage struct {
	tokenUsage
	Messages int
}

func (u *partialUsage) add(m tokenUsage) {
	u.InputTokens += m.InputTokens
	u.OutputTokens += m.OutputTokens
	u.CacheReadInputTokens += m.CacheReadInputTokens
	u.CacheCreationInputTokens += m.CacheCreationInputTokens
	u.Messages++
}

// metadata renders the accumulated counts as the results.json metadata block
// Run writes under the key "partial_usage" on a watchdog kill.
//
// NOTHING CONSUMES THESE KEYS TODAY — stated outright rather than implied.
// atc/exec's ingestFlightRecorder (agent_step.go) stores results.json verbatim
// into agent_run_metrics.results and takes every token counter from the
// events.ndjson cost.record event instead, which is why Run writes the same
// accumulated numbers THERE too, where they are actually ingested. This block
// is the part that says they are PARTIAL: cost.record has no field to carry
// that qualifier, so a reader of the metrics row alone cannot tell a killed
// run's accumulated tokens from a completed run's final ones.
func (u partialUsage) metadata() map[string]any {
	return map[string]any{
		"input_tokens":          u.InputTokens,
		"output_tokens":         u.OutputTokens,
		"cache_read_tokens":     u.CacheReadInputTokens,
		"cache_creation_tokens": u.CacheCreationInputTokens,
		"usage_messages":        u.Messages,
		"source":                "accumulated from live stream-json assistant message usage; no cost was ever streamed",
	}
}

// streamEvent is the projection of one stream-json line the watchdog folds.
// The cost/turn fields ARE the CLI envelope (schema.CLIEnvelope), so the
// watchdog resolves cost by exactly the rule the runner's own parseEnvelope
// and atc/exec's web-side observer use — three readers of the same bytes that
// must never disagree about what a dollar is. Unknown fields are ignored on
// purpose: this must keep working when the CLI adds fields.
type streamEvent struct {
	schema.CLIEnvelope

	// ParentToolUseID is non-null on lines the CLI re-emits from a Task
	// SUBAGENT: the sub-session's assistant messages are echoed into the
	// parent stream. Their usage is real spend and is accumulated, but they
	// are not parent turns — counting them toward the turn floor overcounts
	// without bound on any Task-dispatching run and kills healthy runs
	// (finding I2).
	ParentToolUseID *string `json:"parent_tool_use_id"`

	// Message.Usage is the per-message usage block on type:"assistant" lines.
	Message struct {
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
}

// observe folds one stream-json line into the running totals and trips the
// watchdog on a breach.
//
// The reported cost and num_turns are CUMULATIVE-to-date totals, so they are
// folded with max(), never summed. Only one terminal envelope is emitted per
// honest run, but claude's stdout pipe is inherited by every tool subprocess
// it leaks, so extra "result" lines are reachable: summing would trip the
// budget arm on a forged line, and last-line-wins would let a forged zero mask
// a real spend. Max is the same anti-tamper semantics atc/exec's
// agentCostObserver uses on the web side.
//
// Turns are additionally floored by the number of NON-subagent assistant
// messages, which is the platform's own view of a turn and does not wait for
// the CLI to report num_turns in the final envelope.
func (w *watchdog) observe(line []byte) {
	// Fail-safe (finding I4): this runs on the goroutine copying claude's
	// stdout. An unrecovered panic here would take down agent-runner itself,
	// and with it the flight recorder — the run's ENTIRE accounting — over a
	// cap check. Contain it and disarm instead: a watchdog that has already
	// misbehaved must not go on to kill a healthy run.
	defer w.containPanic("observe")

	if w.disarmed.Load() {
		return
	}

	var event streamEvent
	if json.Unmarshal(line, &event) != nil {
		return
	}

	w.mu.Lock()
	// Everything under this lock is straight-line arithmetic. The panic-prone
	// work — decoding above, formatting and the kill callback below — happens
	// OUTSIDE it, so a contained panic can never leave w.mu held and deadlock
	// the accessors Run calls once the CLI is gone.
	if cost := event.ResolvedCostUSD(); cost > w.cost {
		w.cost = cost
	}
	if event.NumTurns > w.turns {
		w.turns = event.NumTurns
	}
	if event.Type == "assistant" {
		if !event.Message.Usage.empty() {
			w.usage.add(event.Message.Usage)
		}
		if event.ParentToolUseID == nil {
			w.assistant++
		}
	}
	cost := w.cost
	turns := max(w.turns, w.assistant)
	w.mu.Unlock()

	if w.maxCostUSD > 0 && cost > w.maxCostUSD {
		w.trip(TerminatedReasonBudget, fmt.Sprintf(
			"cumulative cost $%.4f exceeded the $%.4f budget cap", cost, w.maxCostUSD))
		return
	}
	if w.maxTurns > 0 && turns > w.maxTurns {
		w.trip(TerminatedReasonTurns, fmt.Sprintf(
			"%d turns exceeded the %d-turn cap", turns, w.maxTurns))
	}
}

// watchWallClock trips the wall-clock arm when the run outlives maxWall. It
// returns as soon as done is closed (claude exited) so it never outlives the
// step.
func (w *watchdog) watchWallClock(start time.Time, done <-chan struct{}) {
	defer w.containPanic("wall-clock watcher")

	if w.maxWall <= 0 {
		return
	}
	timer := time.NewTimer(w.maxWall - time.Since(start))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Both cases can become ready in the same instant — a run that
		// finishes exactly at the bound — and select then picks uniformly at
		// random, so a healthy run would be stamped wall_clock on a coin flip
		// (finding I3). Re-check without blocking and yield to the exit.
		//
		// This does NOT cover the whole false-positive window: `done` closes
		// only after cmd.Run returns, so throughout cmd.WaitDelay's drain
		// (~5s in production) claude is already dead while a leaked descendant
		// holds the pipe open and this timer can still fire. Run's attribution
		// rule is what covers that: a CLI that ended itself and left a clean
		// terminal envelope is never reported as a platform kill.
		select {
		case <-done:
			return
		default:
		}
		w.trip(TerminatedReasonWallClock, fmt.Sprintf(
			"wall clock exceeded the %s bound", w.maxWall))
	}
}

// containPanic is the fail-safe recover for both watchdog goroutines. A
// watchdog failure must never take down the run's accounting, so it is logged
// and the watchdog is DISARMED — every later observe is a no-op and the run
// continues unwatched, which is strictly better than losing the flight
// recorder or killing a healthy run on a broken cap check.
func (w *watchdog) containPanic(where string) {
	r := recover()
	if r == nil {
		return
	}
	w.disarmed.Store(true)
	fmt.Fprintf(w.warn,
		"agent-runner: warning: watchdog %s panicked (%v) and is now DISARMED; the run continues unwatched\n",
		where, r)
}

// trip records the FIRST breach and kills claude's process group. Later
// breaches are ignored so results.json names the reason that actually ended
// the run — and so a single terminal envelope that breaches two caps at once
// produces one kill, not two.
func (w *watchdog) trip(reason, detail string) {
	w.mu.Lock()
	if w.reason != "" {
		w.mu.Unlock()
		return
	}
	w.reason, w.detail = reason, detail
	w.mu.Unlock()
	w.kill()
}

// terminated returns the winning breach, or ("", "") when the watchdog never
// fired.
func (w *watchdog) terminated() (reason string, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason, w.detail
}

// observed returns what the watchdog saw: the cumulative cost and turn count,
// plus the token usage accumulated from the live stream. A killed run has no
// final envelope, so on that path these are the only cost/turn/token numbers
// the flight recorder can report — without them a step stopped mid-runaway
// ingests as free.
func (w *watchdog) observed() (cost float64, turns int, usage partialUsage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cost, max(w.turns, w.assistant), w.usage
}

// streamLineWriter splits the claude stdout byte stream into complete NDJSON
// lines and hands each to observe. It rides the existing stdout MultiWriter,
// so the same bytes still reach the transcript buffer and the step log.
//
// It never reports an error and never short-writes: io.MultiWriter aborts at
// the first failing writer, and this one runs FIRST (see Run), so an error
// here would blind the transcript and the step log instead of just the
// watchdog.
type streamLineWriter struct {
	buf     bytes.Buffer
	observe func([]byte)
}

func (writer *streamLineWriter) Write(p []byte) (int, error) {
	writer.buf.Write(p)
	for {
		pending := writer.buf.Bytes()
		index := bytes.IndexByte(pending, '\n')
		if index < 0 {
			break
		}
		line := make([]byte, index)
		copy(line, pending[:index])
		writer.buf.Next(index + 1)
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			writer.observe(trimmed)
		}
	}
	if writer.buf.Len() > maxStreamLineBytes {
		// No newline within the bound: these bytes are not a line the watchdog
		// will ever parse. Drop them. The stream RESYNCS at the next newline —
		// the tail of the dropped line arrives as a truncated fragment that
		// fails to decode and is ignored, and the line after it folds normally.
		writer.buf.Reset()
	}
	return len(p), nil
}
