package db_test

import (
	"encoding/json"
	"time"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentCostLedgerFactory", func() {
	var ledger db.AgentCostLedgerFactory

	BeforeEach(func() {
		ledger = db.NewAgentCostLedgerFactory(dbConn)
		_, err := dbConn.Exec(`DELETE FROM agent_cost_ledger`)
		Expect(err).ToNot(HaveOccurred())
	})

	intPtr := func(i int) *int { return &i }

	It("inserts and sums per ticket, excluding harvest_judge spend (§1.13)", func() {
		at := time.Now()
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, TicketID: intPtr(7), CostUSD: 1.25,
			InputTokens: 100, OutputTokens: 50, Turns: 3, Model: "claude-sonnet-5",
			UserName: "alice", OccurredAt: at,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceGateway, TicketID: intPtr(7), CostUSD: 0.75, OccurredAt: at,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceHarvestJudge, TicketID: intPtr(7), CostUSD: 0.5, OccurredAt: at, // excluded from ticket sums
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, CostUSD: 9.0, OccurredAt: at, // no ticket
		})).To(Succeed())

		spent, err := ledger.SpentForTicket(7)
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 2.0, 1e-9))

		By("still counting judge spend toward time-window sums (daily cap)")
		windowSpent, err := ledger.SpentSince(at.Add(-time.Minute))
		Expect(err).ToNot(HaveOccurred())
		Expect(windowSpent).To(BeNumerically("~", 11.5, 1e-9))

		spent, err = ledger.SpentForTicket(999)
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeZero())
	})

	It("defaults occurred_at to now and sums since a cutoff", func() {
		old := time.Now().Add(-48 * time.Hour)
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 5, OccurredAt: old})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceProbe, CostUSD: 2})).To(Succeed()) // zero -> now()

		spent, err := ledger.SpentSince(time.Now().Add(-time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 2.0, 1e-9))

		spent, err = ledger.SpentSince(time.Now().Add(-72 * time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 7.0, 1e-9))
	})

	It("rolls up by day, user, ticket, and workflow metadata", func() {
		day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
		day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2,
			InputTokens: 10, OccurredAt: day1,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, OccurredAt: day2,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, UserName: "bob", TicketID: intPtr(42), CostUSD: 4, OccurredAt: day2,
			Metadata: json.RawMessage(`{"workflow":"review@1"}`),
		})).To(Succeed())

		since := day1.Add(-time.Hour)

		rows, err := ledger.Rollup(budget.GroupByDay, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Key).To(Equal("2026-07-07"))
		Expect(rows[0].Entries).To(Equal(1))
		Expect(rows[0].InputTokens).To(Equal(int64(10)))
		Expect(rows[1].CostUSD).To(BeNumerically("~", 6.0, 1e-9))

		rows, err = ledger.Rollup(budget.GroupByUser, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2)) // alice, bob

		rows, err = ledger.Rollup(budget.GroupByTicket, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		keys := []string{}
		for _, r := range rows {
			keys = append(keys, r.Key)
		}
		Expect(keys).To(ContainElement("42"))

		rows, err = ledger.Rollup(budget.GroupByWorkflow, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		found := false
		for _, r := range rows {
			if r.Key == "review@1" {
				found = true
				Expect(r.CostUSD).To(BeNumerically("~", 4.0, 1e-9))
			}
		}
		Expect(found).To(BeTrue())

		By("seeding known model/step fixture rows")
		Expect(ledger.Insert(budget.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "opus", StepName: "implement", CostUSD: 1.0})).NotTo(HaveOccurred())
		Expect(ledger.Insert(budget.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "opus", StepName: "harvest", CostUSD: 2.0})).NotTo(HaveOccurred())
		Expect(ledger.Insert(budget.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "sonnet", StepName: "implement", CostUSD: 4.0})).NotTo(HaveOccurred())

		By("rolling up by model")
		rows, err = ledger.Rollup(budget.GroupByModel, since, time.Time{})
		Expect(err).NotTo(HaveOccurred())
		modelCost := map[string]float64{}
		for _, r := range rows {
			modelCost[r.Key] = r.CostUSD
		}
		Expect(modelCost["opus"]).To(BeNumerically("~", 3.0, 0.0001))
		Expect(modelCost["sonnet"]).To(BeNumerically("~", 4.0, 0.0001))

		By("rolling up by step")
		rows, err = ledger.Rollup(budget.GroupByStep, since, time.Time{})
		Expect(err).NotTo(HaveOccurred())
		stepCost := map[string]float64{}
		for _, r := range rows {
			stepCost[r.Key] = r.CostUSD
		}
		Expect(stepCost["implement"]).To(BeNumerically("~", 5.0, 0.0001))
		Expect(stepCost["harvest"]).To(BeNumerically("~", 2.0, 0.0001))

		By("bounding with until")
		rows, err = ledger.Rollup(budget.GroupByDay, since, day2.Add(-time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))

		By("rejecting unknown group_by")
		_, err = ledger.Rollup("nonsense", since, time.Time{})
		Expect(err).To(HaveOccurred())
	})
})
