package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("AgentCostRollup", func() {
	BeforeEach(func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/agent/costs", "group_by=user&since=2026-07-01"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, costs.RollupResponse{
					GroupBy: "user",
					Summary: costs.DailySummary{CapUSD: 50, SpentUSD: 5, RemainingUSD: 45},
					Rows:    []budget.RollupRow{{Key: "alice", Entries: 2, CostUSD: 5}},
				}),
			),
		)
	})

	It("fetches the rollup with query params", func() {
		resp, err := client.AgentCostRollup("user", "2026-07-01", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.GroupBy).To(Equal("user"))
		Expect(resp.Rows).To(HaveLen(1))
		Expect(resp.Summary.RemainingUSD).To(Equal(45.0))
	})
})
