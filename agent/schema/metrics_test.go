package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestRunMetricsRoundTrip(t *testing.T) {
	// round-trips the ingest payload shape
	ticket := 7
	rm := schema.RunMetrics{
		TicketID: &ticket, BuildID: 123, PlanID: "5f2a", StepName: "implement",
		Status: schema.RunStatusOK, Summary: "did the thing", Model: "claude-sonnet-4-5",
		Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
		Turns: 9, WallTimeSeconds: 60, CostUSD: 0.42,
		Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass"}`),
		EventsArtifact: "vol-abc123",
		EventCounts:    map[string]int{"tool.call": 4},
	}
	data, err := json.Marshal(rm)
	requireNoErr(t, err)
	var back schema.RunMetrics
	requireNoErr(t, json.Unmarshal(data, &back))
	requireEqual(t, back, rm)
	requireContains(t, string(data), `"cache_read_input_tokens":10`)
}
