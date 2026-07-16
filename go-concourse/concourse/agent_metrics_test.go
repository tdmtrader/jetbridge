package concourse_test

import (
	"net/http"

	agentschema "github.com/concourse/concourse/agent/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("AgentRunMetrics", func() {
	BeforeEach(func() {
		ticket := 101
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/agent/metrics", "limit=5"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, []agentschema.RunMetrics{
					{
						TicketID: &ticket, BuildID: 9002, PlanID: "p2", StepName: "implement",
						WorkflowName: "standard-dev", Status: "ok", Turns: 31, CostUSD: 2.87,
						Usage: agentschema.Usage{InputTokens: 180000, OutputTokens: 41000},
					},
				}),
			),
		)
	})

	It("fetches recent run metrics with the limit query param", func() {
		runs, err := client.AgentRunMetrics(5)
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].StepName).To(Equal("implement"))
		Expect(runs[0].CostUSD).To(Equal(2.87))
		Expect(*runs[0].TicketID).To(Equal(101))
	})
})
