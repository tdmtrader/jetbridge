package schema_test

import (
	"encoding/json"
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

func TestProviderVisibleMCPReadyDataRoundTripsSafeFields(t *testing.T) {
	data, err := json.Marshal(schema.MCPReadyData{Server: "output-builder", ProtocolVersion: "2024-11-05", Tools: []string{"describe_output", "validate_output", "write_output"}})
	requireNoErr(t, err)
	requireJSONEqual(t, data, `{"server":"output-builder","protocol_version":"2024-11-05","tools":["describe_output","validate_output","write_output"]}`)
	var got schema.MCPReadyData
	requireNoErr(t, json.Unmarshal(data, &got))
	if got.Server != "output-builder" || got.ProtocolVersion != "2024-11-05" || len(got.Tools) != 3 {
		t.Fatalf("round trip = %#v", got)
	}
}
