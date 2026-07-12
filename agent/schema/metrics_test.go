package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("RunMetrics", func() {
	It("round-trips the ingest payload shape", func() {
		ticket := 7
		rm := schema.RunMetrics{
			TicketID: &ticket, BuildID: 123, PlanID: "5f2a", StepName: "implement",
			Status: schema.RunStatusOK, Summary: "did the thing", Model: "claude-sonnet-4-5",
			Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
			Turns: 9, WallTimeSeconds: 60, CostUSD: 0.42,
			Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass"}`),
			EventsArtifact: "vol-abc123",
			EventCounts:    map[string]int{"tool.call": 4},
		}
		data, err := json.Marshal(rm)
		Expect(err).ToNot(HaveOccurred())
		var back schema.RunMetrics
		Expect(json.Unmarshal(data, &back)).To(Succeed())
		Expect(back).To(Equal(rm))
		Expect(string(data)).To(ContainSubstring(`"cache_read_input_tokens":10`))
	})
})
