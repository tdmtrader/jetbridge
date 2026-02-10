package schema_test

import (
	"bytes"
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
