package llm

import "encoding/json"

// Usage captures token consumption from an LLM call.
type Usage struct {
	InputTokens             int `json:"input_tokens"`
	OutputTokens            int `json:"output_tokens"`
	CacheReadInputTokens    int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// CallResult is the enriched response from an LLM call, containing both
// the extracted result payload and metadata about the invocation.
type CallResult struct {
	// Result is the extracted JSON content (the actual LLM output).
	Result json.RawMessage

	// Model is the model that was used for this call.
	Model string

	// Usage contains token counts for this call.
	Usage Usage

	// CostUSD is the estimated cost of this call in US dollars.
	CostUSD float64

	// DurationMS is the total wall-clock duration in milliseconds.
	DurationMS int

	// DurationAPIMS is the API-side duration in milliseconds.
	DurationAPIMS int

	// NumTurns is the number of conversation turns in this call.
	NumTurns int
}

// cliEnvelope represents the full JSON response from the Claude CLI
// when invoked with --output-format json.
type cliEnvelope struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	Result        json.RawMessage `json:"result"`
	Model         string          `json:"model"`
	CostUSD       float64         `json:"cost_usd"`
	DurationMS    int             `json:"duration_ms"`
	DurationAPIMS int             `json:"duration_api_ms"`
	NumTurns      int             `json:"num_turns"`
	IsError       bool            `json:"is_error"`
	Usage         Usage           `json:"usage"`
	SessionID     string          `json:"session_id"`
}

// ParseCLIEnvelope parses the full Claude CLI JSON output into a CallResult.
// If the output is not a recognized CLI envelope (no "type" field), it falls
// back to treating the entire output as the result payload.
func ParseCLIEnvelope(data []byte) CallResult {
	var env cliEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
		// Not a CLI envelope — treat entire output as raw result
		return CallResult{Result: ExtractJSON(data)}
	}

	result := env.Result
	// The "result" field from the CLI is a JSON string, not a nested object.
	// Try to unquote it and extract JSON content if it's a string.
	if len(result) > 0 && result[0] == '"' {
		var s string
		if json.Unmarshal(result, &s) == nil {
			result = ExtractJSON([]byte(s))
		}
	}

	return CallResult{
		Result:        result,
		Model:         env.Model,
		Usage:         env.Usage,
		CostUSD:       env.CostUSD,
		DurationMS:    env.DurationMS,
		DurationAPIMS: env.DurationAPIMS,
		NumTurns:      env.NumTurns,
	}
}
