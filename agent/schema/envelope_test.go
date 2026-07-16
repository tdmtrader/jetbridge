package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/schema"
)

func TestCLIEnvelopeCostUSD(t *testing.T) {
	// Newer CLIs report total_cost_usd; it wins when present.
	e := schema.CLIEnvelope{CostUSD: 0.10, TotalCostUSD: 0.42}
	if got := e.ResolvedCostUSD(); got != 0.42 {
		t.Errorf("ResolvedCostUSD() = %v, want 0.42", got)
	}
	// Older CLIs report only cost_usd.
	e = schema.CLIEnvelope{CostUSD: 0.07}
	if got := e.ResolvedCostUSD(); got != 0.07 {
		t.Errorf("ResolvedCostUSD() = %v, want 0.07", got)
	}
}

func TestCLIEnvelopeUnmarshal(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,"result":"done","model":"m1","num_turns":9,"total_cost_usd":0.42,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1,"cache_creation_input_tokens":2}}`
	var e schema.CLIEnvelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Type != "result" || e.Model != "m1" || e.NumTurns != 9 || e.IsError {
		t.Errorf("scalar fields wrong: %+v", e)
	}
	if e.Usage.InputTokens != 100 || e.Usage.CacheCreationInputTokens != 2 {
		t.Errorf("usage wrong: %+v", e.Usage)
	}
	if string(e.Result) != `"done"` {
		t.Errorf("result = %s", e.Result)
	}
}
