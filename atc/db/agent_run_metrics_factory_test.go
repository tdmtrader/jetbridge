package db_test

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/api/metrics"
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
		inserted, prev, err := factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue()) // first insert on (build_id, plan_id) 42/5f2a
		Expect(prev).To(BeNil())      // nothing replaced

		rm.Summary = "second"
		rm.CostUSD = 0.43
		inserted, prev, err = factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse()) // ON CONFLICT fired — resume/retry, not a new row
		// the replaced row's ledger counters come back so the caller can
		// append the spend delta (severed-exec finding, 2026-07-11)
		Expect(prev).ToNot(BeNil())
		Expect(prev.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
		Expect(prev.Usage.InputTokens).To(Equal(int64(100)))
		Expect(prev.Usage.OutputTokens).To(Equal(int64(50)))
		Expect(prev.Usage.CacheReadInputTokens).To(Equal(int64(3)))
		Expect(prev.Usage.CacheCreationInputTokens).To(Equal(int64(2)))
		Expect(prev.Turns).To(Equal(9))

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

	// --- review finding 2026-07-12: ParseSubmission accepts every status the
	// contract defines (ok|failed|error|parked), so the real schema must too —
	// the handler suite runs on MemoryStore, which has no CHECK, so only a
	// real-DB spec can see a parser/CHECK mismatch. Before migration
	// 1773106062 a park-exit partial ingestion (PARK-V2 §1.8) violated the
	// status CHECK here and the row was lost with a 500.
	It("stores every ParseSubmission-accepted status, including parked (PARK-V2)", func() {
		for i, status := range []string{
			schema.RunStatusOK, schema.RunStatusFailed, schema.RunStatusError, schema.RunStatusParked,
		} {
			body := []byte(`{"build_id":50,"plan_id":"plan-` + status + `","step_name":"implement","status":"` + status + `"}`)
			rm, err := metrics.ParseSubmission(body)
			Expect(err).ToNot(HaveOccurred(), "status %d: %s", i, status)

			inserted, prev, err := factory.UpsertReturningInserted(rm)
			Expect(err).ToNot(HaveOccurred(), "status %d: %s", i, status)
			Expect(inserted).To(BeTrue())
			Expect(prev).To(BeNil())
		}

		rows, err := factory.GetByBuild(50)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(4))
		statuses := []string{}
		for _, row := range rows {
			statuses = append(statuses, row.Status)
		}
		Expect(statuses).To(ContainElement(schema.RunStatusParked))
	})

	// --- finding F24: degraded re-ingestion must never clobber a real row ---
	It("InsertIfAbsent inserts when absent and preserves an existing row", func() {
		good := &schema.RunMetrics{
			BuildID: 44, PlanID: "bb", StepName: "implement",
			Status: "ok", Summary: "real ingestion", CostUSD: 0.42, Turns: 9,
		}
		inserted, _, err := factory.UpsertReturningInserted(good)
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
