package schema

import "encoding/json"

// CLIEnvelope is the claude CLI --output-format json result envelope. It is
// parsed in two places that MUST agree on the same bytes: the in-pod runner
// (agent/runner) turns it into the flight recorder's cost.record, and the
// web-side cost observer (atc/exec) reads it off the live stdout stream as an
// anti-tamper cost floor. Keep it in one place so a CLI field rename can never
// silently zero one side's cost reading.
type CLIEnvelope struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	Result       json.RawMessage `json:"result"`
	Model        string          `json:"model"`
	CostUSD      float64         `json:"cost_usd"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	NumTurns     int             `json:"num_turns"`
	IsError      bool            `json:"is_error"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// ResolvedCostUSD prefers total_cost_usd (reported by newer CLIs) and falls
// back to cost_usd.
func (e CLIEnvelope) ResolvedCostUSD() float64 {
	if e.TotalCostUSD > 0 {
		return e.TotalCostUSD
	}
	return e.CostUSD
}
