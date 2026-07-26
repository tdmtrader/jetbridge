package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Watchdog termination reasons, stamped into flight/results.json as
// metadata.terminated_reason so ingestion (and a human reading the row) can
// tell a platform-enforced cutoff from an agent failure.
const (
	TerminatedReasonBudget    = "budget"
	TerminatedReasonTurns     = "turns"
	TerminatedReasonWallClock = "wall_clock"
)

// maxStreamLineBytes bounds the partial-line buffer held while waiting for a
// newline. The CLI emits one JSON object per line; a stream that never
// produces a newline is malformed, so drop the partial rather than grow
// without bound.
const maxStreamLineBytes = 1 << 20

// watchdog is the PLATFORM-SIDE backstop for the caps handed to the claude CLI
// as --max-budget-usd / --max-turns, plus a wall-clock bound the CLI has no
// flag for. The CLI's own enforcement stays the first line; this exists
// because a CLI that ignores, mis-parses, or outlives its own flags must not
// be able to spend unbounded money or time.
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

	mu        sync.Mutex
	cost      float64
	turns     int
	assistant int
	reason    string
	detail    string
}

func newWatchdog(cfg Config, kill func()) *watchdog {
	return &watchdog{
		maxCostUSD: cfg.BudgetSliceUSD,
		maxTurns:   cfg.MaxTurns,
		maxWall:    cfg.MaxWallClock,
		kill:       kill,
	}
}

// armed reports whether any cap is configured. An unarmed watchdog is never
// attached to the stream, so an unconfigured step pays nothing.
func (w *watchdog) armed() bool {
	return w.maxCostUSD > 0 || w.maxTurns > 0 || w.maxWall > 0
}

// streamEvent is the minimal projection of a stream-json line the watchdog
// needs. Unknown fields are ignored on purpose: this must keep working when
// the CLI adds fields.
type streamEvent struct {
	Type         string  `json:"type"`
	CostUSD      float64 `json:"cost_usd"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
}

// observe folds one stream-json line into the running totals and trips the
// watchdog on a breach.
//
// cost_usd / total_cost_usd / num_turns reported by the CLI are
// CUMULATIVE-to-date totals, so they are folded with max(), never summed —
// summing a progress stream would trip the budget arm at a fraction of the
// real spend. Turns are additionally floored by the number of assistant
// messages seen, which is the platform's own view of a turn and does not wait
// for the CLI to report num_turns in the final envelope.
func (w *watchdog) observe(line []byte) {
	var event streamEvent
	if json.Unmarshal(line, &event) != nil {
		return
	}

	w.mu.Lock()
	if event.TotalCostUSD > w.cost {
		w.cost = event.TotalCostUSD
	}
	if event.CostUSD > w.cost {
		w.cost = event.CostUSD
	}
	if event.NumTurns > w.turns {
		w.turns = event.NumTurns
	}
	if event.Type == "assistant" {
		w.assistant++
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
	if w.maxWall <= 0 {
		return
	}
	timer := time.NewTimer(w.maxWall - time.Since(start))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		w.trip(TerminatedReasonWallClock, fmt.Sprintf(
			"wall clock exceeded the %s bound", w.maxWall))
	}
}

// trip records the FIRST breach and kills claude's process group. Later
// breaches are ignored so results.json names the reason that actually ended
// the run.
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

// observed returns the cumulative cost and turn count the watchdog saw. A
// killed run has no final envelope, so these are the only cost/turn numbers
// the flight recorder can report — without them a step stopped at $50 ingests
// as free.
func (w *watchdog) observed() (cost float64, turns int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cost, max(w.turns, w.assistant)
}

// streamLineWriter splits the claude stdout byte stream into complete NDJSON
// lines and hands each to observe. It rides the existing stdout MultiWriter,
// so the same bytes still reach the transcript buffer and the step log.
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
		writer.buf.Reset()
	}
	return len(p), nil
}
