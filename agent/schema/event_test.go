package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func validEvent() schema.Event {
	return schema.Event{
		Timestamp: "2026-02-09T21:30:00Z",
		Type:      schema.EventStepStart,
		Data:      json.RawMessage(`{"step_name":"review","plan_id":"abc"}`),
	}
}

func TestEventValidate(t *testing.T) {
	t.Run("exposes the event-type constants and raw-message data", func(t *testing.T) {
		e := schema.Event{
			Timestamp: "2026-07-08T12:00:00Z",
			Type:      schema.EventCostRecord,
			Data:      json.RawMessage(`{"k":"v"}`),
		}
		requireNoErr(t, e.Validate())
		requireEqual(t, string(schema.EventStepEnd), "step.end")
	})

	t.Run("accepts a valid event with all required fields", func(t *testing.T) {
		e := validEvent()
		requireNoErr(t, e.Validate())
	})

	t.Run("rejects missing timestamp", func(t *testing.T) {
		e := validEvent()
		e.Timestamp = ""
		requireErrContains(t, e.Validate(), "ts")
	})

	t.Run("rejects invalid RFC3339 timestamp", func(t *testing.T) {
		e := validEvent()
		e.Timestamp = "not-a-timestamp"
		requireErrContains(t, e.Validate(), "ts")
	})

	t.Run("rejects a date-only timestamp", func(t *testing.T) {
		e := validEvent()
		e.Timestamp = "2026-02-09"
		requireErrContains(t, e.Validate(), "ts")
	})

	t.Run("accepts various valid RFC3339 formats", func(t *testing.T) {
		for _, ts := range []string{
			"2026-02-09T21:30:00Z",
			"2026-02-09T21:30:00+00:00",
			"2026-02-09T21:30:00.123456789Z",
			"2026-02-09T14:30:00-07:00",
		} {
			e := validEvent()
			e.Timestamp = ts
			requireNoErr(t, e.Validate(), "expected timestamp %q to be valid", ts)
		}
	})

	t.Run("rejects missing event type", func(t *testing.T) {
		e := validEvent()
		e.Type = ""
		requireErrContains(t, e.Validate(), "event")
	})

	t.Run("rejects nil data", func(t *testing.T) {
		e := validEvent()
		e.Data = nil
		requireErrContains(t, e.Validate(), "data")
	})

	t.Run("rejects empty (zero-length) data", func(t *testing.T) {
		e := validEvent()
		e.Data = json.RawMessage{}
		requireErrContains(t, e.Validate(), "data")
	})

	t.Run("accepts an empty data object", func(t *testing.T) {
		e := validEvent()
		e.Data = json.RawMessage(`{}`)
		requireNoErr(t, e.Validate())
	})

	t.Run("accepts all known event types", func(t *testing.T) {
		for _, et := range []schema.EventType{
			schema.EventStepStart,
			schema.EventStepEnd,
			schema.EventCostRecord,
			schema.EventError,
		} {
			e := validEvent()
			e.Type = et
			requireNoErr(t, e.Validate(), "expected event type %q to be valid", et)
		}
	})

	t.Run("accepts custom/extensible event types", func(t *testing.T) {
		e := validEvent()
		e.Type = "review.file_analyzed"
		requireNoErr(t, e.Validate())
	})
}

func TestEventJSONRoundTrip(t *testing.T) {
	t.Run("marshals and unmarshals an Event", func(t *testing.T) {
		original := schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventCostRecord,
			Data:      json.RawMessage(`{"cost_usd":0.42}`),
		}

		data, err := json.Marshal(original)
		requireNoErr(t, err)

		var decoded schema.Event
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.Timestamp, original.Timestamp)
		requireEqual(t, decoded.Type, original.Type)
		requireJSONEqual(t, decoded.Data, `{"cost_usd":0.42}`)
	})

	t.Run("uses correct JSON field names (ts, event, data)", func(t *testing.T) {
		e := schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventStepStart,
			Data:      json.RawMessage(`{"step_name":"review"}`),
		}

		data, err := json.Marshal(e)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		requireHasKey(t, raw, "ts")
		requireHasKey(t, raw, "event")
		requireHasKey(t, raw, "data")
		requireNoKey(t, raw, "timestamp")
		requireNoKey(t, raw, "type")
	})

	t.Run("serializes nil data as empty object", func(t *testing.T) {
		e := schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventError,
		}

		data, err := json.Marshal(e)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		dataField, ok := raw["data"].(map[string]interface{})
		requireTrue(t, ok, "data should be an object")
		requireLen(t, dataField, 0)
	})

	t.Run("produces a single JSON line (no embedded newlines)", func(t *testing.T) {
		e := schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventStepEnd,
			Data:      json.RawMessage(`{"status":"ok","cost_usd":0.5,"turns":7}`),
		}

		data, err := json.Marshal(e)
		requireNoErr(t, err)
		requireNotContains(t, string(data), "\n")
	})
}
