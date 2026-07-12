package db_test

import (
	"encoding/json"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunMetricsFactory", func() {
	// NOTE(agent-step T8): the plan declares this as metrics.Store
	// (agent/api/metrics, Task 7). That package is on a parallel branch;
	// db.AgentRunMetricsFactory is structurally identical, so the factory
	// satisfies metrics.Store as soon as Task 7 merges.
	var factory db.AgentRunMetricsFactory

	BeforeEach(func() {
		factory = db.NewAgentRunMetricsFactory(dbConn)
	})

	It("upserts on (build_id, plan_id) and lists by ticket", func() {
		ticket := 7
		rm := &schema.RunMetrics{
			TicketID: &ticket, BuildID: 42, PlanID: "5f2a", StepName: "implement",
			Status: "ok", Summary: "first", Model: "claude-sonnet-4-5",
			Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
			Turns: 9, WallTimeSeconds: 61, CostUSD: 0.42,
			Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass","confidence":1,"summary":"x","artifacts":[]}`),
			EventsArtifact: "vol-1",
			EventCounts:    map[string]int{"tool.call": 4},
		}
		inserted, err := factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue()) // first insert on (build_id, plan_id) 42/5f2a

		rm.Summary = "second"
		rm.CostUSD = 0.43
		inserted, err = factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse()) // ON CONFLICT fired — resume/retry, not a new row

		rows, err := factory.ListByTicket(7)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Summary).To(Equal("second"))
		Expect(rows[0].CostUSD).To(BeNumerically("~", 0.43, 1e-9))
		Expect(rows[0].Usage.InputTokens).To(Equal(int64(100)))
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("tool.call", 4))
		Expect(rows[0].CreatedAt).To(BeNumerically(">", 0))

		byBuild, err := factory.GetByBuild(42)
		Expect(err).ToNot(HaveOccurred())
		Expect(byBuild).To(HaveLen(1))
	})

	It("stores NULL ticket/workflow tags for pure-CI steps", func() {
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: 43, PlanID: "aa", StepName: "s", Status: "error", Summary: "crashed",
		})).To(Succeed())
		rows, err := factory.GetByBuild(43)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows[0].TicketID).To(BeNil())
		Expect(rows[0].WorkflowVersion).To(BeNil())
	})

	// --- finding F24: degraded re-ingestion must never clobber a real row ---
	It("InsertIfAbsent inserts when absent and preserves an existing row", func() {
		good := &schema.RunMetrics{
			BuildID: 44, PlanID: "bb", StepName: "implement",
			Status: "ok", Summary: "real ingestion", CostUSD: 0.42, Turns: 9,
		}
		inserted, err := factory.UpsertReturningInserted(good)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue())

		// Degraded re-ingestion (resume with no flight data readable): the
		// zero-cost error row hits ON CONFLICT DO NOTHING and writes nothing.
		degraded := &schema.RunMetrics{
			BuildID: 44, PlanID: "bb", StepName: "implement",
			Status: "error", Summary: "flight recorder output missing",
		}
		inserted, err = factory.InsertIfAbsent(degraded)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse())

		rows, err := factory.GetByBuild(44)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Status).To(Equal("ok")) // web-1's real row survives
		Expect(rows[0].CostUSD).To(BeNumerically("~", 0.42, 1e-9))

		// And it still inserts when no row exists (crashed agent, first run).
		inserted, err = factory.InsertIfAbsent(&schema.RunMetrics{
			BuildID: 45, PlanID: "cc", StepName: "s", Status: "error", Summary: "crashed",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue())
	})
})
