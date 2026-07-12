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
			Type:      schema.EventAgentStart,
			Data:      json.RawMessage(`{"step":"review"}`),
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
				Type:      schema.EventToolCall,
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
			Type:      schema.EventAgentEnd,
			Data:      json.RawMessage(`{"status":"pass"}`),
		})
		requireNoErr(t, err)

		line := strings.TrimSpace(buf.String())
		requireJSONEqual(t, []byte(line), `{"ts":"2026-02-09T21:30:00Z","event":"agent.end","data":{"status":"pass"}}`)
	})

	t.Run("sets a missing timestamp before writing", func(t *testing.T) {
		buf := &bytes.Buffer{}
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Type: schema.EventAgentStart,
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
			Type:      schema.EventAgentStart,
			Data:      json.RawMessage(`{}`),
		})
		requireNoErr(t, err)

		requireTrue(t, strings.HasSuffix(buf.String(), "\n"), "expected trailing newline")
	})
}

func TestEventReader(t *testing.T) {
	t.Run("reads a single event from NDJSON", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{"step":"review"}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		event, err := r.Read()
		requireNoErr(t, err)
		requireEqual(t, event.Timestamp, "2026-02-09T21:30:00Z")
		requireEqual(t, event.Type, schema.EventAgentStart)
		requireJSONEqual(t, event.Data, `{"step":"review"}`)
	})

	t.Run("reads multiple events sequentially", func(t *testing.T) {
		input := strings.Join([]string{
			`{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{"step":"review"}}`,
			`{"ts":"2026-02-09T21:30:01Z","event":"tool.call","data":{"tool":"grep"}}`,
			`{"ts":"2026-02-09T21:30:02Z","event":"agent.end","data":{"status":"pass"}}`,
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
		requireEqual(t, events[0].Type, schema.EventAgentStart)
		requireEqual(t, events[1].Type, schema.EventToolCall)
		requireEqual(t, events[2].Type, schema.EventAgentEnd)
	})

	t.Run("returns io.EOF when no more events", func(t *testing.T) {
		r := schema.NewEventReader(strings.NewReader(""))

		_, err := r.Read()
		requireEqual(t, err, io.EOF)
	})

	t.Run("skips empty lines", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{}}` + "\n\n\n" +
			`{"ts":"2026-02-09T21:30:01Z","event":"agent.end","data":{}}` + "\n"

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

	t.Run("returns an error for invalid JSON", func(t *testing.T) {
		input := "not valid json\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		requireErr(t, err)
		requireContains(t, err.Error(), "line 1")
	})

	t.Run("validates each event after parsing", func(t *testing.T) {
		input := `{"ts":"","event":"agent.start","data":{}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		requireErr(t, err)
		requireContains(t, err.Error(), "line 1")
	})

	t.Run("reports line number on parse error", func(t *testing.T) {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{}}` + "\n" +
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
				Type:      schema.EventAgentStart,
				Data:      json.RawMessage(`{"step":"review"}`),
			},
			{
				Timestamp: "2026-02-09T21:30:05Z",
				Type:      schema.EventToolCall,
				Data:      json.RawMessage(`{"tool":"grep","duration_ms":42}`),
			},
			{
				Timestamp: "2026-02-09T21:30:10Z",
				Type:      schema.EventAgentEnd,
				Data:      json.RawMessage(`{"status":"pass","confidence":0.92}`),
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
