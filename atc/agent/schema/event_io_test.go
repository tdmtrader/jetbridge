package schema_test

import (
	"bytes"
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/agent/schema"
)

var _ = Describe("EventWriter", func() {
	var buf *bytes.Buffer

	BeforeEach(func() {
		buf = &bytes.Buffer{}
	})

	It("writes a single event as one JSON line", func() {
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventAgentStart,
			Data:      map[string]interface{}{"step": "review"},
		})
		Expect(err).NotTo(HaveOccurred())

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		Expect(lines).To(HaveLen(1))
	})

	It("writes multiple events as separate lines", func() {
		w := schema.NewEventWriter(buf)

		for i := 0; i < 3; i++ {
			err := w.Write(schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventToolCall,
				Data:      map[string]interface{}{"index": float64(i)},
			})
			Expect(err).NotTo(HaveOccurred())
		}

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		Expect(lines).To(HaveLen(3))
	})

	It("each line is valid JSON", func() {
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventAgentEnd,
			Data:      map[string]interface{}{"status": "pass"},
		})
		Expect(err).NotTo(HaveOccurred())

		line := strings.TrimSpace(buf.String())
		Expect(line).To(MatchJSON(`{"ts":"2026-02-09T21:30:00Z","event":"agent.end","data":{"status":"pass"}}`))
	})

	It("validates events before writing", func() {
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "",
			Type:      schema.EventAgentStart,
			Data:      map[string]interface{}{},
		})
		Expect(err).To(HaveOccurred())
		Expect(buf.Len()).To(Equal(0), "invalid event should not be written")
	})

	It("each line ends with a newline", func() {
		w := schema.NewEventWriter(buf)

		err := w.Write(schema.Event{
			Timestamp: "2026-02-09T21:30:00Z",
			Type:      schema.EventAgentStart,
			Data:      map[string]interface{}{},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(buf.String()).To(HaveSuffix("\n"))
	})
})

var _ = Describe("EventReader", func() {
	It("reads a single event from NDJSON", func() {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{"step":"review"}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		event, err := r.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(event.Timestamp).To(Equal("2026-02-09T21:30:00Z"))
		Expect(event.Type).To(Equal(schema.EventAgentStart))
		Expect(event.Data).To(HaveKeyWithValue("step", "review"))
	})

	It("reads multiple events sequentially", func() {
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
			Expect(err).NotTo(HaveOccurred())
			events = append(events, *event)
		}

		Expect(events).To(HaveLen(3))
		Expect(events[0].Type).To(Equal(schema.EventAgentStart))
		Expect(events[1].Type).To(Equal(schema.EventToolCall))
		Expect(events[2].Type).To(Equal(schema.EventAgentEnd))
	})

	It("returns io.EOF when no more events", func() {
		r := schema.NewEventReader(strings.NewReader(""))

		_, err := r.Read()
		Expect(err).To(Equal(io.EOF))
	})

	It("skips empty lines", func() {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{}}` + "\n\n\n" +
			`{"ts":"2026-02-09T21:30:01Z","event":"agent.end","data":{}}` + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			events = append(events, *event)
		}

		Expect(events).To(HaveLen(2))
	})

	It("returns an error for invalid JSON", func() {
		input := "not valid json\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("line 1"))
	})

	It("validates each event after parsing", func() {
		input := `{"ts":"","event":"agent.start","data":{}}` + "\n"
		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("line 1"))
	})

	It("reports line number on parse error", func() {
		input := `{"ts":"2026-02-09T21:30:00Z","event":"agent.start","data":{}}` + "\n" +
			"bad json line\n"

		r := schema.NewEventReader(strings.NewReader(input))

		_, err := r.Read()
		Expect(err).NotTo(HaveOccurred())

		_, err = r.Read()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("line 2"))
	})

	It("round-trips through EventWriter and EventReader", func() {
		var buf bytes.Buffer
		w := schema.NewEventWriter(&buf)

		original := []schema.Event{
			{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventAgentStart,
				Data:      map[string]interface{}{"step": "review"},
			},
			{
				Timestamp: "2026-02-09T21:30:05Z",
				Type:      schema.EventToolCall,
				Data:      map[string]interface{}{"tool": "grep", "duration_ms": float64(42)},
			},
			{
				Timestamp: "2026-02-09T21:30:10Z",
				Type:      schema.EventAgentEnd,
				Data:      map[string]interface{}{"status": "pass", "confidence": 0.92},
			},
		}

		for _, e := range original {
			Expect(w.Write(e)).To(Succeed())
		}

		r := schema.NewEventReader(&buf)
		decoded := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			decoded = append(decoded, *event)
		}

		Expect(decoded).To(HaveLen(3))
		for i, d := range decoded {
			Expect(d.Timestamp).To(Equal(original[i].Timestamp))
			Expect(d.Type).To(Equal(original[i].Type))
		}
	})
})
