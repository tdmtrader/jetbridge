package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/agent/schema"
)

var _ = Describe("Event", func() {
	var validEvent func() schema.Event

	BeforeEach(func() {
		validEvent = func() schema.Event {
			return schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventAgentStart,
				Data: map[string]interface{}{
					"step":  "review",
					"model": "claude-sonnet-4-5-20250929",
				},
			}
		}
	})

	Describe("Validate", func() {
		It("accepts a valid event with all required fields", func() {
			e := validEvent()
			Expect(e.Validate()).To(Succeed())
		})

		It("rejects missing timestamp", func() {
			e := validEvent()
			e.Timestamp = ""
			Expect(e.Validate()).To(MatchError(ContainSubstring("ts")))
		})

		It("rejects invalid RFC3339 timestamp", func() {
			e := validEvent()
			e.Timestamp = "not-a-timestamp"
			Expect(e.Validate()).To(MatchError(ContainSubstring("ts")))
		})

		It("rejects a date-only timestamp", func() {
			e := validEvent()
			e.Timestamp = "2026-02-09"
			Expect(e.Validate()).To(MatchError(ContainSubstring("ts")))
		})

		It("accepts various valid RFC3339 formats", func() {
			for _, ts := range []string{
				"2026-02-09T21:30:00Z",
				"2026-02-09T21:30:00+00:00",
				"2026-02-09T21:30:00.123456789Z",
				"2026-02-09T14:30:00-07:00",
			} {
				e := validEvent()
				e.Timestamp = ts
				Expect(e.Validate()).To(Succeed(), "expected timestamp %q to be valid", ts)
			}
		})

		It("rejects missing event type", func() {
			e := validEvent()
			e.Type = ""
			Expect(e.Validate()).To(MatchError(ContainSubstring("event")))
		})

		It("rejects nil data", func() {
			e := validEvent()
			e.Data = nil
			Expect(e.Validate()).To(MatchError(ContainSubstring("data")))
		})

		It("accepts empty data map", func() {
			e := validEvent()
			e.Data = map[string]interface{}{}
			Expect(e.Validate()).To(Succeed())
		})

		It("accepts all known event types", func() {
			for _, et := range []schema.EventType{
				schema.EventAgentStart,
				schema.EventAgentEnd,
				schema.EventSkillStart,
				schema.EventSkillEnd,
				schema.EventToolCall,
				schema.EventToolResult,
				schema.EventArtifactWriten,
				schema.EventDecision,
				schema.EventError,
			} {
				e := validEvent()
				e.Type = et
				Expect(e.Validate()).To(Succeed(), "expected event type %q to be valid", et)
			}
		})

		It("accepts custom/extensible event types", func() {
			e := validEvent()
			e.Type = "review.file_analyzed"
			Expect(e.Validate()).To(Succeed())
		})
	})

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals an Event", func() {
			original := schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventToolCall,
				Data: map[string]interface{}{
					"tool":        "grep",
					"duration_ms": float64(42),
				},
			}

			data, err := json.Marshal(original)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Event
			err = json.Unmarshal(data, &decoded)
			Expect(err).NotTo(HaveOccurred())

			Expect(decoded.Timestamp).To(Equal(original.Timestamp))
			Expect(decoded.Type).To(Equal(original.Type))
			Expect(decoded.Data).To(HaveKeyWithValue("tool", "grep"))
			Expect(decoded.Data).To(HaveKeyWithValue("duration_ms", float64(42)))
		})

		It("uses correct JSON field names (ts, event, data)", func() {
			e := schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventAgentStart,
				Data:      map[string]interface{}{"step": "review"},
			}

			data, err := json.Marshal(e)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			Expect(raw).To(HaveKey("ts"))
			Expect(raw).To(HaveKey("event"))
			Expect(raw).To(HaveKey("data"))
			Expect(raw).NotTo(HaveKey("timestamp"))
			Expect(raw).NotTo(HaveKey("type"))
		})

		It("serializes nil data as empty object", func() {
			e := schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventDecision,
			}

			data, err := json.Marshal(e)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			err = json.Unmarshal(data, &raw)
			Expect(err).NotTo(HaveOccurred())

			dataField, ok := raw["data"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "data should be an object")
			Expect(dataField).To(BeEmpty())
		})

		It("produces a single JSON line (no embedded newlines)", func() {
			e := schema.Event{
				Timestamp: "2026-02-09T21:30:00Z",
				Type:      schema.EventAgentEnd,
				Data: map[string]interface{}{
					"status":      "pass",
					"confidence":  0.92,
					"duration_ms": float64(18500),
				},
			}

			data, err := json.Marshal(e)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).NotTo(ContainSubstring("\n"))
		})
	})
})
