package schema_test

import (
	"bytes"
	"encoding/json"
	"io"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Event", func() {
	validEvent := func() schema.Event {
		return schema.Event{
			Timestamp: "2026-02-10T12:00:00Z",
			EventType: schema.EventAgentStart,
			Data:      json.RawMessage(`{"message":"starting"}`),
		}
	}

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			e := validEvent()
			data, err := json.Marshal(e)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Event
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())

			Expect(decoded.Timestamp).To(Equal("2026-02-10T12:00:00Z"))
			Expect(decoded.EventType).To(Equal(schema.EventAgentStart))
		})

		It("uses correct JSON field names", func() {
			e := validEvent()
			data, _ := json.Marshal(e)
			var raw map[string]interface{}
			json.Unmarshal(data, &raw)

			Expect(raw).To(HaveKey("ts"))
			Expect(raw).To(HaveKey("event"))
			Expect(raw).To(HaveKey("data"))
		})
	})

	Describe("Validate", func() {
		It("passes for valid event", func() {
			e := validEvent()
			Expect(e.Validate()).To(Succeed())
		})

		It("requires ts", func() {
			e := validEvent()
			e.Timestamp = ""
			Expect(e.Validate()).To(MatchError(ContainSubstring("ts")))
		})

		It("requires valid timestamp format", func() {
			e := validEvent()
			e.Timestamp = "not-a-timestamp"
			Expect(e.Validate()).To(MatchError(ContainSubstring("invalid timestamp")))
		})

		It("requires event type", func() {
			e := validEvent()
			e.EventType = ""
			Expect(e.Validate()).To(MatchError(ContainSubstring("event")))
		})

		It("requires data", func() {
			e := validEvent()
			e.Data = nil
			Expect(e.Validate()).To(MatchError(ContainSubstring("data")))
		})
	})
})

var _ = Describe("EventWriter", func() {
	It("writes events as NDJSON", func() {
		var buf bytes.Buffer
		w := schema.NewEventWriter(&buf)

		e := schema.Event{
			Timestamp: "2026-02-10T12:00:00Z",
			EventType: schema.EventAgentStart,
			Data:      json.RawMessage(`{"msg":"start"}`),
		}
		Expect(w.Write(e)).To(Succeed())

		var decoded schema.Event
		Expect(json.Unmarshal(buf.Bytes(), &decoded)).To(Succeed())
		Expect(decoded.EventType).To(Equal(schema.EventAgentStart))
	})

	It("auto-sets timestamp if missing", func() {
		var buf bytes.Buffer
		w := schema.NewEventWriter(&buf)

		e := schema.Event{
			EventType: schema.EventAgentEnd,
			Data:      json.RawMessage(`{}`),
		}
		Expect(w.Write(e)).To(Succeed())

		var decoded schema.Event
		json.Unmarshal(buf.Bytes(), &decoded)
		Expect(decoded.Timestamp).NotTo(BeEmpty())
	})

	It("rejects invalid events", func() {
		var buf bytes.Buffer
		w := schema.NewEventWriter(&buf)

		e := schema.Event{Timestamp: "2026-02-10T12:00:00Z"}
		Expect(w.Write(e)).To(MatchError(ContainSubstring("invalid event")))
	})
})

var _ = Describe("EventReader", func() {
	It("reads NDJSON events", func() {
		input := `{"ts":"2026-02-10T12:00:00Z","event":"agent.start","data":{"msg":"start"}}
{"ts":"2026-02-10T12:00:01Z","event":"agent.end","data":{"msg":"end"}}
`
		r := schema.NewEventReader(bytes.NewBufferString(input))

		e1, err := r.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(e1.EventType).To(Equal(schema.EventAgentStart))

		e2, err := r.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(e2.EventType).To(Equal(schema.EventAgentEnd))

		_, err = r.Read()
		Expect(err).To(Equal(io.EOF))
	})

	It("skips empty lines", func() {
		input := `{"ts":"2026-02-10T12:00:00Z","event":"agent.start","data":{}}

{"ts":"2026-02-10T12:00:01Z","event":"agent.end","data":{}}
`
		r := schema.NewEventReader(bytes.NewBufferString(input))

		e1, _ := r.Read()
		Expect(e1.EventType).To(Equal(schema.EventAgentStart))

		e2, _ := r.Read()
		Expect(e2.EventType).To(Equal(schema.EventAgentEnd))
	})

	It("reports line number on parse error", func() {
		input := `{"ts":"2026-02-10T12:00:00Z","event":"agent.start","data":{}}
not valid json
`
		r := schema.NewEventReader(bytes.NewBufferString(input))

		_, _ = r.Read()
		_, err := r.Read()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("line 2"))
	})

	It("returns EOF on empty input", func() {
		r := schema.NewEventReader(bytes.NewBufferString(""))
		_, err := r.Read()
		Expect(err).To(Equal(io.EOF))
	})
})
