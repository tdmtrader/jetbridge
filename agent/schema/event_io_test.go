package schema_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestEventWriter(t *testing.T) {
	t.Run("writes a single event as one JSON line", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventStepStart,
			Data:      json.RawMessage(`{"step_name":"review"}`),
		})
		requireNoErr(t, err)

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		requireLen(t, lines, 1)
	})

	t.Run("writes multiple events as separate lines", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		for i := 0; i < 3; i++ {
			err := w.Write(schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventCostRecord,
				Data:      json.RawMessage(`{"index":1}`),
			})
			requireNoErr(t, err)
		}

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		requireLen(t, lines, 3)
	})

	t.Run("each line is valid JSON", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventStepEnd,
			Data:      json.RawMessage(`{"status":"ok"}`),
		})
		requireNoErr(t, err)

		line := strings.TrimSpace(buf.String())
		requireJSONEqual(t, []byte(line), `{"ts":"2026-02-09T21:30:00Z","event":"step.end","data":{"status":"ok"}}`)
	})

	t.Run("sets a missing timestamp before writing", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Type: schema.EventStepStart,
			Data: json.RawMessage(`{}`),
		})
		requireNoErr(t, err)

		var written schema.Event
		requireNoErr(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &written))
		requireTrue(t, written.Timestamp != "", "timestamp should be set")
		_, err = time.Parse(time.RFC3339, written.Timestamp)
		requireNoErr(t, err)
	})

	t.Run("validates events before writing", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      "",
			Data:      json.RawMessage(`{}`),
		})
		requireErr(t, err)
		requireEqual(t, buf.Len(), 0, "invalid event should not be written")
	})

	t.Run("each line ends with a newline", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventStepStart,
			Data:      json.RawMessage(`{}`),
		})
		requireNoErr(t, err)

		requireTrue(t, strings.HasSuffix(buf.String(), "\n"), "expected trailing newline")
	})
}

func TestEventReader(t *testing.T) {
	t.Run("reads a single event from NDJSON", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{"step_name":"review"}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		event, err := r.Read()
		requireNoErr(t, err)
		requireEqual(t, event.Timestamp, "2026-02-09T21:30:00Z")
		requireEqual(t, event.Type, schema.EventStepStart)
		requireJSONEqual(t, event.Data, `{"step_name":"review"}`)
	})

	t.Run("reads multiple events sequentially", func(t *testing.T) {
		input := strings.Join([]string{
			`{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{"step_name":"review"}}`,
			`{"ts":"2026-02-09T21:30:01Z","event":"cost.record","data":{"cost_usd":0.5}}`,
			`{"ts":"2026-02-09T21:30:02Z","event":"step.end","data":{"status":"ok"}}`,
		}, "\n") + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			requireNoErr(t, err)
			events = append(events, *event)
		}

		requireLen(t, events, 3)
		requireEqual(t, events[0].Type, schema.EventStepStart)
		requireEqual(t, events[1].Type, schema.EventCostRecord)
		requireEqual(t, events[2].Type, schema.EventStepEnd)
	})

	t.Run("returns io.EOF when no more events", func(t *testing.T) {
		r := schema.NewEventReader(strings.NewReader(""))

		_, err := r.Read()
		requireEqual(t, err, io.EOF)
	})

	t.Run("skips an oversized line and keeps reading later events", func(t *testing.T) {
		// One >5MiB line (e.g. a step.start carrying an oversized budget
		// note, or a foreign producer per contract §5) must not kill the
		// stream: the cost.record and step.end that follow are ledger- and
		// status-relevant (review finding, 2026-07-16).
		oversized := `{"ts":"2026-02-09T21:30:01Z","event":"step.start","data":{"blob":"` +
			strings.Repeat("x", 5<<20) + `"}}`
		input := strings.Join([]string{
			`{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{"step_name":"review"}}`,
			oversized,
			`{"ts":"2026-02-09T21:30:02Z","event":"cost.record","data":{"cost_usd":1.5}}`,
			`{"ts":"2026-02-09T21:30:03Z","event":"step.end","data":{"status":"ok"}}`,
		}, "\n") + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			requireNoErr(t, err)
			events = append(events, *event)
		}

		requireLen(t, events, 3)
		requireEqual(t, events[0].Type, schema.EventStepStart)
		requireEqual(t, events[1].Type, schema.EventCostRecord)
		requireEqual(t, events[2].Type, schema.EventStepEnd)
		requireEqual(t, r.Skipped(), 1)
	})

	t.Run("skips an oversized unterminated final line", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{}}` + "\n" +
			strings.Repeat("y", (5<<20)+1) // torn giant tail, no newline

		r := schema.NewEventReader(strings.NewReader(input))

		event, err := r.Read()
		requireNoErr(t, err)
		requireEqual(t, event.Type, schema.EventStepStart)

		_, err = r.Read()
		requireEqual(t, err, io.EOF)
		requireEqual(t, r.Skipped(), 1)
	})

	t.Run("skips empty lines", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{}}` + "\n\n\n" +
			`{"ts":"2026-02-09T21:30:01Z","event":"step.end","data":{}}` + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			requireNoErr(t, err)
			events = append(events, *event)
		}

		requireLen(t, events, 2)
	})

	t.Run("reads a line larger than the default 64KiB scanner limit", func(t *testing.T) {
		// A single NDJSON event whose payload exceeds bufio.Scanner's default
		// 64KiB token limit must not abort the stream. Agent-step ingestion
		// breaks its read loop on any reader error, so an oversized line
		// mid-stream would otherwise discard every later cost.record and
		// step.end event — leaving the step status=error even when a valid
		// step.end followed (review finding, 2026-07-12).
		big := strings.Repeat("x", 200*1024) // 200 KiB, well past the 64 KiB default
		input := `{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{"step_name":"review","blob":"` + big + `"}}` + "\n" +
			`{"ts":"2026-02-09T21:30:01Z","event":"step.end","data":{"status":"ok"}}` + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			requireNoErr(t, err)
			events = append(events, *event)
		}

		requireLen(t, events, 2)
		requireEqual(t, events[0].Type, schema.EventStepStart)
		requireEqual(t, events[1].Type, schema.EventStepEnd)
	})

	t.Run("returns an error for invalid JSON", func(t *testing.T) {
		input := "not valid json\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		requireErr(t, err)
		requireContains(t, err.Error(), "line 1")
	})

	t.Run("validates each event after parsing", func(t *testing.T) {
		input := `{"ts":"","event":"step.start","data":{}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		requireErr(t, err)
		requireContains(t, err.Error(), "line 1")
	})

	t.Run("reports line number on parse error", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{}}` + "\n" +
			"bad json line\n"

		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		requireNoErr(t, err)

		_, err = r.Read()
		requireErr(t, err)
		requireContains(t, err.Error(), "line 2")
	})

	t.Run("round-trips through EventWriter and EventReader", func(t *testing.T) {
		var buf bytes.Buffer
		w := schema.NewEventWriter(&buf)

		original := []schema.Event{
			{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventStepStart,
				Data:      json.RawMessage(`{"step_name":"review"}`),
			},
			{
				Timestamp: "2026-02-09T21:30:05Z",
				Type:      schema.EventCostRecord,
				Data:      json.RawMessage(`{"cost_usd":0.5,"turns":3}`),
			},
			{
				Timestamp: "2026-02-09T21:30:10Z",
				Type:      schema.EventStepEnd,
				Data:      json.RawMessage(`{"status":"ok"}`),
			},
		}

		for _, e := range original {
			requireNoErr(t, w.Write(e))
		}

		r := schema.NewEventReader(&buf)
		decoded := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			requireNoErr(t, err)
			decoded = append(decoded, *event)
		}

		requireLen(t, decoded, 3)
		for i, d := range decoded {
			requireEqual(t, d.Timestamp, original[i].Timestamp)
			requireEqual(t, d.Type, original[i].Type)
		}
	})
}
