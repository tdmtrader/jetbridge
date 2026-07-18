package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestStepEndDataMarshalsSnakeCase(t *testing.T) {
	// marshals StepEndData with snake_case keys
	data, err := json.Marshal(schema.StepEndData{
		StepName: "implement", Status: schema.RunStatusOK,
		Summary: "done", WallTimeSeconds: 42, CostUSD: 0.5, Turns: 7,
	})
	requireNoErr(t, err)
	requireJSONEqual(t, data, `{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.5,"turns":7}`)
	e := schema.Event{Timestamp: "2026-07-08T12:00:00Z", Type: schema.EventStepEnd, Data: data}
	requireNoErr(t, e.Validate())
}

func TestGateResultDataAttemptFlakyRoundTrip(t *testing.T) {
	in := schema.GateResultData{Gate: "test", Scope: "full", Status: "ok",
		DurationSeconds: 2.5, Summary: "passed on retry", Attempt: 2, Flaky: true}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"attempt":2`) || !strings.Contains(string(raw), `"flaky":true`) {
		t.Fatalf("additive keys missing: %s", raw)
	}
	var out schema.GateResultData
	if err := json.Unmarshal(raw, &out); err != nil || out.Attempt != 2 || !out.Flaky {
		t.Fatalf("round-trip failed: %+v, %v", out, err)
	}
	// first-attempt passes omit both keys (omitempty) so old consumers
	// see byte-identical events
	clean, _ := json.Marshal(schema.GateResultData{Gate: "test", Scope: "full", Status: "ok"})
	if strings.Contains(string(clean), "attempt") || strings.Contains(string(clean), "flaky") {
		t.Fatalf("omitempty violated: %s", clean)
	}
}
