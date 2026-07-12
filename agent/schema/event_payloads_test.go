package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("event payloads", func() {
	It("marshals StepEndData with snake_case keys", func() {
		data, err := json.Marshal(schema.StepEndData{
			StepName: "implement", Status: schema.RunStatusOK,
			Summary: "done", WallTimeSeconds: 42, CostUSD: 0.5, Turns: 7,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(MatchJSON(`{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.5,"turns":7}`))
		e := schema.Event{Timestamp: "2026-07-08T12:00:00Z", Type: schema.EventStepEnd, Data: data}
		Expect(e.Validate()).To(Succeed())
	})
})
