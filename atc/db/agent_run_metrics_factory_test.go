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

	It("joins the pipeline build status onto each run metric (U3 display truth)", func() {
		// The U3 lie: an agent STEP can exit "ok" while the pipeline BUILD
		// it ran in failed. The read path must expose the build status so
		// display surfaces stop rendering a green "ok" on a failed build.
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())

		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: build.ID(), PlanID: "p1", StepName: "implement",
			Status: "ok", Summary: "agent reported ok",
		})).To(Succeed())

		rows, err := factory.GetByBuild(build.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Status).To(Equal("ok"))          // agent step exit
		Expect(rows[0].BuildStatus).To(Equal("failed")) // the build truth
		// and the read path fuses them, so no surface re-derives the rule
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeFailed))
	})

	It("leaves BuildStatus empty when the metric references no real build", func() {
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: 999123, PlanID: "orphan", StepName: "s", Status: "ok", Summary: "x",
		})).To(Succeed())
		rows, err := factory.GetByBuild(999123)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].BuildStatus).To(Equal("")) // LEFT JOIN, no match
		// no build truth to fuse — the outcome is the step's own word
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeOK))
	})

	It("derives the outcome across the succeeded/open build matrix (U3)", func() {
		greenBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		Expect(greenBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())

		// green build, delivered summary → ok
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: greenBuild.ID(), PlanID: "p1", StepName: "implement",
			Status: "ok", Summary: "delivered",
		})).To(Succeed())
		// green build, nothing delivered → no_output, never a green verdict
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: greenBuild.ID(), PlanID: "p2", StepName: "harvest",
			Status: "ok",
		})).To(Succeed())

		rows, err := factory.GetByBuild(greenBuild.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeOK))
		Expect(rows[1].Outcome).To(Equal(schema.RunOutcomeNoOutput))

		// parked under a still-open build → parked (HITL checkpoint visible)
		openBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: openBuild.ID(), PlanID: "p1", StepName: "implement",
			Status: "parked",
		})).To(Succeed())
		rows, err = factory.GetByBuild(openBuild.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeParked))
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
	// 1773106061 a park-exit partial ingestion (PARK-V2 §1.8) violated the
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

	// --- review finding 2026-07-12 (F#1/F#4): the ON CONFLICT DO UPDATE must
	// never REGRESS a stored row's ledger-relevant counters or downgrade a
	// real status to 'error'. A web-restart resume can re-ingest the same
	// (build_id, plan_id) with a partially-read flight recorder (transient
	// daemon/exec sever between the results.json and events.ndjson reads):
	// flightRead is still true, so the write takes the Upsert path, but cost
	// is partial and status is forced to 'error' (no step.end). An
	// unconditional overwrite (a) corrupts the scorecards/delivery-outcomes
	// row downward, and (b) — because the caller derives the append-only
	// ledger delta from the previous row's counters — lets a later full
	// resume re-charge the whole cost, double-counting into agent_cost_ledger.
	// The upsert must be monotonic on cost/tokens/turns and must not let an
	// incoming 'error' clobber a non-error status.
	It("never regresses ledger counters, status, or results on a degraded re-ingestion", func() {
		good := &schema.RunMetrics{
			BuildID: 46, PlanID: "dd", StepName: "implement",
			Status: "ok", Summary: "complete", Model: "claude-sonnet-4-5",
			Usage: schema.Usage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
			Turns: 12, CostUSD: 5.00,
			Results:     json.RawMessage(`{"schema_version":"1.0","status":"pass","confidence":1,"summary":"x","artifacts":[]}`),
			EventCounts: map[string]int{"tool.call": 20},
		}
		inserted, prev, err := factory.UpsertReturningInserted(good)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue())
		Expect(prev).To(BeNil())

		// Resume: partial events read → lower cost, status forced to error,
		// blank results/event_counts. This must NOT overwrite the good row.
		degraded := &schema.RunMetrics{
			BuildID: 46, PlanID: "dd", StepName: "implement",
			Status: "error", Summary: "event stream ended without step.end",
			Usage: schema.Usage{InputTokens: 400, OutputTokens: 200},
			Turns: 5, CostUSD: 2.00,
		}
		inserted, prev, err = factory.UpsertReturningInserted(degraded)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse())
		Expect(prev).ToNot(BeNil())
		// prev reports the stored (un-regressed) cost, so the caller's ledger
		// delta (degraded.cost − prev.cost) is negative and skipped.
		Expect(prev.CostUSD).To(BeNumerically("~", 5.00, 1e-9))

		rows, err := factory.GetByBuild(46)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].CostUSD).To(BeNumerically("~", 5.00, 1e-9))
		Expect(rows[0].Status).To(Equal("ok"))
		Expect(rows[0].Usage.InputTokens).To(Equal(int64(1000)))
		Expect(rows[0].Usage.OutputTokens).To(Equal(int64(500)))
		Expect(rows[0].Turns).To(Equal(12))
		Expect(rows[0].Results).ToNot(BeEmpty())
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("tool.call", 20))

		// A later FULL resume heals upward and its delta is charged exactly once.
		full := &schema.RunMetrics{
			BuildID: 46, PlanID: "dd", StepName: "implement",
			Status: "ok", Summary: "complete",
			Usage: schema.Usage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
			Turns: 12, CostUSD: 5.00,
		}
		inserted, prev, err = factory.UpsertReturningInserted(full)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse())
		Expect(prev).ToNot(BeNil())
		// prev.cost is still 5.00 (never regressed), so delta = 5.00 − 5.00 = 0:
		// no second ledger charge.
		Expect(prev.CostUSD).To(BeNumerically("~", 5.00, 1e-9))
	})

	It("keeps stored event_counts when a re-ingestion carries an empty (non-nil) map", func() {
		// A severed events read leaves ingestion with EventCounts = {} (the
		// map is created as soon as events.ndjson opens), which marshals to
		// '{}' — NOT SQL NULL — so a bare COALESCE would blank real counts.
		full := &schema.RunMetrics{
			BuildID: 47, PlanID: "ee", StepName: "implement",
			Status: "ok", Summary: "complete", CostUSD: 1.00,
			EventCounts: map[string]int{"tool.call": 20, "subagent.call": 2},
		}
		_, _, err := factory.UpsertReturningInserted(full)
		Expect(err).ToNot(HaveOccurred())

		severed := &schema.RunMetrics{
			BuildID: 47, PlanID: "ee", StepName: "implement",
			Status: "error", Summary: "event stream ended without step.end",
			CostUSD:     0,
			EventCounts: map[string]int{},
		}
		_, _, err = factory.UpsertReturningInserted(severed)
		Expect(err).ToNot(HaveOccurred())

		rows, err := factory.GetByBuild(47)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("tool.call", 20))
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("subagent.call", 2))
	})

	It("ListRecent returns the most-recent rows first, bounded by limit", func() {
		for i, b := range []int{71, 72, 73} {
			Expect(factory.Upsert(&schema.RunMetrics{
				BuildID: b, PlanID: "z", StepName: "implement",
				Status: "ok", Summary: "row", CostUSD: 0.1,
				Model: "m", Turns: i,
			})).To(Succeed())
		}
		rows, err := factory.ListRecent(2)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		// most recent (highest created_at / id) first
		Expect(rows[0].BuildID).To(Equal(73))
		Expect(rows[1].BuildID).To(Equal(72))

		// limit <= 0 falls back to a sane default (returns all seeded rows here)
		all, err := factory.ListRecent(0)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(all)).To(BeNumerically(">=", 3))
	})
})
