package exec

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// maxObservedEnvelopeLineBytes bounds the observer's line buffer. The claude
// CLI envelope embeds the agent's final result text, so lines can be large,
// but anything past this bound is dropped un-parsed (fail open: the floor
// simply isn't raised) rather than held in web-node memory. Matches the 5MB
// LimitReader on the results.json read in ingestFlightRecorder.
const maxObservedEnvelopeLineBytes = 5 << 20

// observedAgentCost is the server-side cost floor extracted from the agent
// process's LIVE stdout stream (see agentCostObserver). Fields mirror the
// ledger-relevant counters of schema.CostRecordData / budget.LedgerEntry.
type observedAgentCost struct {
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Turns               int
	CostUSD             float64
}

// stdoutEnvelope mirrors the ledger-relevant subset of the claude CLI
// --output-format json envelope (parity with agent/runner/envelope.go, which
// parses the SAME bytes in-pod to produce the flight recorder's cost.record).
type stdoutEnvelope struct {
	Type         string  `json:"type"`
	Model        string  `json:"model"`
	CostUSD      float64 `json:"cost_usd"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (e stdoutEnvelope) costUSD() float64 {
	// Same preference the runner applies: newer CLIs report total_cost_usd.
	if e.TotalCostUSD > 0 {
		return e.TotalCostUSD
	}
	return e.CostUSD
}

// agentCostObserver tees the agent process's stdout to dst unchanged while
// scanning it for claude CLI --output-format json envelope lines, keeping a
// per-field MAXIMUM across everything it sees.
//
// Why this exists (review finding, 2026-07-12): all cost that enters
// agent_cost_ledger for source=agent_step comes from flight/events.ndjson,
// which is written INSIDE the agent pod — where claude runs as root with
// --dangerously-skip-permissions and AGENT_FLIGHT_DIR in its own env. A
// prompt-injected agent (or any leaked descendant) can truncate/rewrite that
// file with cost_usd 0 before the pod exits, understating SpentForTicket and
// loosening StepSlice admission for every subsequent step of the run/ticket.
// The stdout stream, by contrast, passes through THIS writer on the web node
// as it is produced: bytes already shipped cannot be rewritten at rest. The
// envelope observed here is the same one agent-runner parses into
// cost.record, so on the honest path the two agree exactly; at ingestion the
// observed values act as a floor under the flight-reported ones.
//
// The floor is a monotonic per-field max, NOT last-line-wins (the runner's
// parseEnvelope semantics): a tamperer that prints a forged zero-cost
// envelope after the real one — e.g. from a leaked descendant inside
// cmd.WaitDelay's drain window — cannot lower it. Raising the floor with a
// forged HIGH-cost line adds no new power: the ledger row is already
// writable upward via events.ndjson itself.
//
// Best-effort by design: an agent killed before the envelope prints streams
// nothing (partial-usage capture is PARK-V2 stream-json teeing work), a
// reattach may miss already-streamed output, and unparseable/oversized lines
// are skipped — in every such case the floor stays at zero and ingestion
// falls back to trusting the flight recorder, exactly the status quo ante.
type agentCostObserver struct {
	dst io.Writer

	mu       sync.Mutex
	line     []byte
	overflow bool
	observed observedAgentCost
}

func newAgentCostObserver(dst io.Writer) *agentCostObserver {
	return &agentCostObserver{dst: dst}
}

func (o *agentCostObserver) Write(p []byte) (int, error) {
	// Scan before forwarding so a short write to dst never hides envelope
	// bytes from the floor.
	o.scan(p)
	return o.dst.Write(p)
}

func (o *agentCostObserver) scan(p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			o.buffer(p)
			return
		}
		o.buffer(p[:i])
		if !o.overflow {
			o.parseLine(o.line)
		}
		o.line = o.line[:0]
		o.overflow = false
		p = p[i+1:]
	}
}

// buffer appends chunk to the pending line, discarding the whole line once
// it exceeds the bound (fail open — see maxObservedEnvelopeLineBytes).
func (o *agentCostObserver) buffer(chunk []byte) {
	if o.overflow {
		return
	}
	if len(o.line)+len(chunk) > maxObservedEnvelopeLineBytes {
		o.line = o.line[:0]
		o.overflow = true
		return
	}
	o.line = append(o.line, chunk...)
}

func (o *agentCostObserver) parseLine(line []byte) {
	// The agent process runs under a TTY, so lines may end \r\n; TrimSpace
	// covers the \r along with any padding.
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var env stdoutEnvelope
	if json.Unmarshal(line, &env) != nil || env.Type != "result" {
		return
	}
	if cost := env.costUSD(); cost > o.observed.CostUSD {
		o.observed.CostUSD = cost
		if env.Model != "" {
			o.observed.Model = env.Model
		}
	}
	if o.observed.Model == "" && env.Model != "" {
		o.observed.Model = env.Model
	}
	o.observed.InputTokens = max(o.observed.InputTokens, env.Usage.InputTokens)
	o.observed.OutputTokens = max(o.observed.OutputTokens, env.Usage.OutputTokens)
	o.observed.CacheReadTokens = max(o.observed.CacheReadTokens, env.Usage.CacheReadInputTokens)
	o.observed.CacheCreationTokens = max(o.observed.CacheCreationTokens, env.Usage.CacheCreationInputTokens)
	o.observed.Turns = max(o.observed.Turns, env.NumTurns)
}

// Observed returns the per-field maximum seen so far. A pending final line
// with no trailing newline (claude can exit without one) is parsed in place
// but NOT consumed: if its remainder arrives later the completed line is
// parsed again, which is harmless under max semantics.
func (o *agentCostObserver) Observed() observedAgentCost {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.line) > 0 && !o.overflow {
		o.parseLine(o.line)
	}
	return o.observed
}
