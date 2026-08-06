package db_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
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

	Describe("a restarted execution", func() {
		// A fresh agent after an interruption arrives looking exactly like a
		// partial re-read of the execution already stored: lower counters,
		// because its provider counters started at zero. The non-regressing
		// merge that protects the re-read case is wrong for this one -- it
		// keeps the abandoned agent's numbers and the caller's ledger delta
		// comes out negative and is dropped, so the second agent's spend
		// vanishes from both the row and the ledger. The step declares the
		// restart instead of the store guessing.
		newRun := func(buildID int, cost float64, turns, wall int) *schema.RunMetrics {
			return &schema.RunMetrics{
				BuildID: buildID, PlanID: "1/2", StepName: "implement",
				Status: "ok", CostUSD: cost, Turns: turns, WallTimeSeconds: wall,
				Usage: schema.Usage{OutputTokens: int64(turns) * 10},
			}
		}

		It("adds the new execution's spend instead of keeping the abandoned agent's", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			first := newRun(build.ID(), 3.00, 90, 400)
			_, _, err = factory.UpsertReturningInserted(first)
			Expect(err).ToNot(HaveOccurred())

			Expect(factory.MarkRestartPending(build.ID(), "1/2")).To(Succeed())

			// Cheaper and shorter than the run it replaced -- the shape that
			// is indistinguishable from a partial re-read.
			second := newRun(build.ID(), 2.50, 60, 300)
			inserted, prev, err := factory.UpsertReturningInserted(second)
			Expect(err).ToNot(HaveOccurred())
			Expect(inserted).To(BeFalse())
			Expect(prev).ToNot(BeNil())
			Expect(second.NewExecution).To(BeTrue(), "the store must report the restart so the caller charges in full")

			rows, err := factory.GetByBuild(build.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			stored := rows[0]
			Expect(stored.CostUSD).To(BeNumerically("~", 5.50, 1e-9), "both agents really ran and both cost money")
			Expect(stored.Turns).To(Equal(150))
		})

		It("takes the new execution's window, so the frozen occurrence is not the abandoned run's", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			_, _, err = factory.UpsertReturningInserted(newRun(build.ID(), 3.00, 90, 400))
			Expect(err).ToNot(HaveOccurred())
			var firstCreatedAt time.Time
			Expect(dbConn.QueryRow(`SELECT created_at FROM agent_run_metrics WHERE build_id = $1`, build.ID()).
				Scan(&firstCreatedAt)).To(Succeed())

			Expect(factory.MarkRestartPending(build.ID(), "1/2")).To(Succeed())
			_, _, err = factory.UpsertReturningInserted(newRun(build.ID(), 2.50, 60, 300))
			Expect(err).ToNot(HaveOccurred())

			var createdAt time.Time
			var wall int
			Expect(dbConn.QueryRow(`SELECT created_at, wall_time_seconds FROM agent_run_metrics WHERE build_id = $1`, build.ID()).
				Scan(&createdAt, &wall)).To(Succeed())
			Expect(createdAt).To(BeTemporally(">", firstCreatedAt), "created_at must move to the new execution's completion")
			Expect(wall).To(Equal(300), "wall time must be the new execution's, not the longest seen")
		})

		It("still refuses to regress an ordinary partial re-read", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			_, _, err = factory.UpsertReturningInserted(newRun(build.ID(), 3.00, 90, 400))
			Expect(err).ToNot(HaveOccurred())

			// No restart declared: this is the severed-read case the
			// non-regressing merge exists for.
			partial := newRun(build.ID(), 1.00, 30, 100)
			_, _, err = factory.UpsertReturningInserted(partial)
			Expect(err).ToNot(HaveOccurred())
			Expect(partial.NewExecution).To(BeFalse())

			rows, err := factory.GetByBuild(build.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			stored := rows[0]
			Expect(stored.CostUSD).To(BeNumerically("~", 3.00, 1e-9), "a partial re-read must never sum or shrink")
			Expect(stored.Turns).To(Equal(90))
		})

		It("consumes the flag once, so the ingestion after a restart is ordinary again", func() {
			build, err := defaultTeam.CreateOneOffBuild()
			Expect(err).ToNot(HaveOccurred())

			_, _, err = factory.UpsertReturningInserted(newRun(build.ID(), 3.00, 90, 400))
			Expect(err).ToNot(HaveOccurred())
			Expect(factory.MarkRestartPending(build.ID(), "1/2")).To(Succeed())
			_, _, err = factory.UpsertReturningInserted(newRun(build.ID(), 2.50, 60, 300))
			Expect(err).ToNot(HaveOccurred())

			// A re-read of the restarted execution, not a third agent.
			third := newRun(build.ID(), 2.50, 60, 300)
			_, _, err = factory.UpsertReturningInserted(third)
			Expect(err).ToNot(HaveOccurred())
			Expect(third.NewExecution).To(BeFalse(), "the flag must not survive the ingestion it described")

			rows, err := factory.GetByBuild(build.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			stored := rows[0]
			Expect(stored.CostUSD).To(BeNumerically("~", 5.50, 1e-9), "a re-read after a restart must not sum again")
		})
	})

	// upsert seeds a row the way the exec's in-process ingestion does. The
	// unconditional Upsert shim went with POST /api/v1/agent/metrics; the
	// discriminating write is the only one left.
	upsert := func(rm *schema.RunMetrics) error {
		_, _, err := factory.UpsertReturningInserted(rm)
		return err
	}

	// The row is upserted on (build_id, plan_id), so a step that executes more
	// than once overwrites its own record. The sequence is the only thing that
	// survives to say it happened -- and it is what bounds interruption
	// restarts, which must survive the web process that was counting them.
	It("counts each ingestion of the same step on the row it writes", func() {
		rm := &schema.RunMetrics{
			BuildID: 77, PlanID: "seq-1", StepName: "implement",
			Status: "ok", CostUSD: 0.10, WallTimeSeconds: 30,
		}

		_, _, err := factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(rm.IngestionSeq).To(Equal(1), "a first ingestion is sequence 1, never 0")

		_, _, err = factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(rm.IngestionSeq).To(Equal(2))

		_, _, err = factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(rm.IngestionSeq).To(Equal(3))

		By("not advancing another step's sequence")
		other := &schema.RunMetrics{
			BuildID: 77, PlanID: "seq-2", StepName: "implement",
			Status: "ok", CostUSD: 0.10, WallTimeSeconds: 30,
		}
		_, _, err = factory.UpsertReturningInserted(other)
		Expect(err).ToNot(HaveOccurred())
		Expect(other.IngestionSeq).To(Equal(1))
	})

	// An interrupted agent writes no flight output, so its ingestion takes the
	// degraded InsertIfAbsent path. That path must not overwrite a real row --
	// but it must still record that another execution happened, or the
	// interruption cap has nothing to count and retries are unbounded.
	It("counts a degraded ingestion without overwriting the row it found", func() {
		real := &schema.RunMetrics{
			BuildID: 78, PlanID: "degraded", StepName: "implement",
			Status: "ok", Summary: "real", CostUSD: 1.70, WallTimeSeconds: 392,
		}
		_, _, err := factory.UpsertReturningInserted(real)
		Expect(err).ToNot(HaveOccurred())
		Expect(real.IngestionSeq).To(Equal(1))

		degraded := &schema.RunMetrics{
			BuildID: 78, PlanID: "degraded", StepName: "implement",
			Status: "error", Summary: "", CostUSD: 0, WallTimeSeconds: 0,
		}
		inserted, err := factory.InsertIfAbsent(degraded)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse(), "the real row must survive")
		Expect(degraded.IngestionSeq).To(Equal(2), "but the execution must still be counted")

		By("leaving the real row's data intact")
		stored, err := factory.GetByBuild(78)
		Expect(err).ToNot(HaveOccurred())
		Expect(stored).To(HaveLen(1))
		Expect(stored[0].Status).To(Equal("ok"))
		Expect(stored[0].Summary).To(Equal("real"))
		Expect(stored[0].CostUSD).To(BeNumerically("~", 1.70, 1e-9))
	})

	It("upserts on (build_id, plan_id), returning replaced ledger counters", func() {
		rm := &schema.RunMetrics{
			BuildID: 42, PlanID: "5f2a", StepName: "implement",
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

		rows, err := factory.GetByBuild(42)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Summary).To(Equal("second"))
		Expect(rows[0].CostUSD).To(BeNumerically("~", 0.43, 1e-9))
		Expect(rows[0].Usage.InputTokens).To(Equal(int64(100)))
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("tool.call", 4))
		Expect(rows[0].CreatedAt).To(BeNumerically(">", 0))
	})

	It("lists metrics by durable workflow run, surviving its build's deletion", func() {
		// A real build the workflow run planned, and a metric produced in it.
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())

		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('code-review', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		var runID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, 'code-review', 1, 3, 1, $4, $5, '{}', $6,
			        'manual', '', 'alice', 'running', $7)
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, strings.Repeat("a", 64),
			"metric-run-key", strings.Repeat("b", 64), build.ID()).Scan(&runID)).To(Succeed())

		// The metric field is the schema-local type; the Store query takes
		// snapshot's — both are the same int64 id.
		wfRunID := schema.WorkflowRunID(runID)
		snapRunID := snapshot.WorkflowRunID(runID)
		Expect(upsert(&schema.RunMetrics{
			WorkflowRunID: &wfRunID, FunctionID: "review",
			BuildID: build.ID(), PlanID: "p1", StepName: "review-diff",
			Status: "ok", Summary: "one finding",
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("code-review", snapRunID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].WorkflowRunID).ToNot(BeNil())
		Expect(*rows[0].WorkflowRunID).To(Equal(wfRunID))
		Expect(rows[0].FunctionID).To(Equal("review"))

		By("returning nothing for a run id under the wrong workflow name")
		wrong, err := factory.ListByWorkflowRun("some-other-workflow", snapRunID)
		Expect(err).ToNot(HaveOccurred())
		Expect(wrong).To(BeEmpty())

		By("keeping the metric row after its builds row is deleted (BuildStatus empty)")
		Expect(build.Delete()).To(BeTrue())
		rows, err = factory.ListByWorkflowRun("code-review", snapRunID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].BuildStatus).To(BeEmpty())
	})

	It("joins the pipeline build status onto each run metric (U3 display truth)", func() {
		// The U3 lie: an agent STEP can exit "ok" while the pipeline BUILD
		// it ran in failed. The read path must expose the build status so
		// display surfaces stop rendering a green "ok" on a failed build.
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())

		Expect(upsert(&schema.RunMetrics{
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
		Expect(upsert(&schema.RunMetrics{
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
		Expect(upsert(&schema.RunMetrics{
			BuildID: greenBuild.ID(), PlanID: "p1", StepName: "implement",
			Status: "ok", Summary: "delivered",
		})).To(Succeed())
		// green build, nothing delivered → no_output, never a green verdict
		Expect(upsert(&schema.RunMetrics{
			BuildID: greenBuild.ID(), PlanID: "p2", StepName: "gates",
			Status: "ok",
		})).To(Succeed())

		rows, err := factory.GetByBuild(greenBuild.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeOK))
		Expect(rows[1].Outcome).To(Equal(schema.RunOutcomeNoOutput))

		// a step-reported failure under a still-open build is never masked by
		// the build still being open
		openBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		Expect(upsert(&schema.RunMetrics{
			BuildID: openBuild.ID(), PlanID: "p1", StepName: "implement",
			Status: "failed", Summary: "did not converge",
		})).To(Succeed())
		rows, err = factory.GetByBuild(openBuild.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Outcome).To(Equal(schema.RunOutcomeFailed))
	})

	It("stores NULL workflow identity for pure-CI steps", func() {
		Expect(upsert(&schema.RunMetrics{
			BuildID: 43, PlanID: "aa", StepName: "s", Status: "error", Summary: "crashed",
		})).To(Succeed())
		rows, err := factory.GetByBuild(43)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows[0].WorkflowRunID).To(BeNil())
	})

	// --- review finding 2026-07-12: the schema must accept every status the
	// contract defines, and ONLY those. The handler suite runs on MemoryStore,
	// which has no CHECK, so only a real-DB spec can see a contract/CHECK
	// mismatch.
	It("stores every contract status and rejects the retired parked status", func() {
		for i, status := range []string{
			schema.RunStatusOK, schema.RunStatusFailed, schema.RunStatusError,
		} {
			inserted, prev, err := factory.UpsertReturningInserted(&schema.RunMetrics{
				BuildID: 50, PlanID: "plan-" + status, StepName: "implement", Status: status,
			})
			Expect(err).ToNot(HaveOccurred(), "status %d: %s", i, status)
			Expect(inserted).To(BeTrue())
			Expect(prev).To(BeNil())
		}

		rows, err := factory.GetByBuild(50)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(3))

		// PARK-V2 is gone: the runner has no park exit, so the CHECK must not
		// accept a status no writer can produce.
		_, _, err = factory.UpsertReturningInserted(&schema.RunMetrics{
			BuildID: 50, PlanID: "plan-parked", StepName: "implement", Status: "parked",
		})
		Expect(err).To(HaveOccurred())
	})

	// --- L-1 (#41): the incomplete status (a server-set ingestion degradation,
	// never client-submittable) round-trips through the CHECK constraint added
	// by migration 1773106092. ---
	It("stores and reads back a status=incomplete row (L-1 CHECK constraint)", func() {
		Expect(upsert(&schema.RunMetrics{
			BuildID: 60, PlanID: "no-flight", StepName: "implement",
			Status: schema.RunStatusIncomplete, Summary: "no flight output (runner image predates flight recorder?)",
		})).To(Succeed())

		rows, err := factory.GetByBuild(60)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Status).To(Equal(schema.RunStatusIncomplete))
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
			Expect(upsert(&schema.RunMetrics{
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

// createBuildWithStatus creates a real one-off build under the suite's
// defaultTeam and finishes it with the given terminal status, returning its
// id. WorkflowStats LEFT JOINs builds for success truth, so the fixture needs
// a REAL builds row (a fabricated BuildID like the attribution test's 424242
// would never match the join). Mirrors the CreateOneOffBuild + Finish path the
// existing specs in this file already use.
func createBuildWithStatus(status db.BuildStatus) int {
	build, err := defaultTeam.CreateOneOffBuild()
	Expect(err).ToNot(HaveOccurred())
	Expect(build.Finish(status)).To(Succeed())
	return build.ID()
}

// createWorkflowRun inserts a real agent_workflow_runs row under the suite's
// defaultTeam, bound to buildID as its planned build. WorkflowStats INNER
// JOINs this table for the workflow's identity and version, so the stats
// fixture needs real runs, not tags on the metric row.
func createWorkflowRun(name string, version int, buildID int) schema.WorkflowRunID {
	suffix := time.Now().UnixNano()
	// the definition row only satisfies the run's FK; its own (name, version)
	// is unique per call so several runs can share one workflow name+version
	var definitionID int
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'tdm', 3, 1)
		RETURNING id
	`, fmt.Sprintf("%s-def-%d", name, suffix), fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID)).To(Succeed())
	var runID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
			 schema_version, signature_version, definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
			 created_by, status, planned_build_id)
		VALUES ($1, $2, $3, $4, $5, 3, 1, $6, $7, '{}', $8, 'manual', '', 'tdm', 'running', $9)
		RETURNING id
	`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name, version,
		strings.Repeat("a", 64), fmt.Sprintf("stats-key-%d", suffix), strings.Repeat("b", 64),
		buildID,
	).Scan(&runID)).To(Succeed())
	return schema.WorkflowRunID(runID)
}

var _ = Describe("AgentRunMetricsFactory WorkflowStats", func() {
	var factory db.AgentRunMetricsFactory
	var workflowName string

	BeforeEach(func() {
		factory = db.NewAgentRunMetricsFactory(dbConn)
		// the definitions table is unique on (name, version), so each spec
		// needs its own workflow name
		workflowName = fmt.Sprintf("wf-stats-%d", time.Now().UnixNano())
	})

	upsert := func(rm *schema.RunMetrics) error {
		_, _, err := factory.UpsertReturningInserted(rm)
		return err
	}

	insert := func(runID schema.WorkflowRunID, buildID int, plan, status string, cost float64, turns int) {
		Expect(upsert(&schema.RunMetrics{
			WorkflowRunID: &runID, FunctionID: "work",
			BuildID: buildID, PlanID: plan, StepName: "s",
			Status: status, CostUSD: cost, Turns: turns,
		})).To(Succeed())
	}

	It("aggregates per version by distinct build, joining build status for success", func() {
		// Two v3 runs: one whose planned build is 'succeeded', one 'failed'.
		b1 := createBuildWithStatus(db.BuildStatusSucceeded)
		b2 := createBuildWithStatus(db.BuildStatusFailed)
		r1 := createWorkflowRun(workflowName, 3, b1)
		r2 := createWorkflowRun(workflowName, 3, b2)
		insert(r1, b1, "p1", "ok", 1.50, 5)
		insert(r1, b1, "p2", "ok", 0.50, 3) // 2 steps, same build → 1 run
		insert(r2, b2, "p1", "failed", 2.00, 8)

		// an ad-hoc CI step with no workflow run contributes to nothing:
		// workflow identity is the run, never a tag on the metric row
		Expect(upsert(&schema.RunMetrics{
			BuildID: b1, PlanID: "ci", StepName: "s", Status: "ok", CostUSD: 99, Turns: 99,
		})).To(Succeed())

		stats, err := factory.WorkflowStats(workflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(HaveLen(1))

		s := stats[0]
		Expect(*s.Version).To(Equal(3))
		Expect(s.Runs).To(Equal(2))         // distinct builds
		Expect(s.WorkflowRuns).To(Equal(2)) // distinct durable runs
		Expect(s.SucceededRuns).To(Equal(1))
		Expect(s.TotalCostUSD).To(BeNumerically("~", 4.00, 1e-6))
		Expect(s.TotalTurns).To(Equal(16))
	})

	It("buckets by the run's workflow version, newest first, and ignores other workflows", func() {
		b1 := createBuildWithStatus(db.BuildStatusSucceeded)
		b2 := createBuildWithStatus(db.BuildStatusSucceeded)
		other := createBuildWithStatus(db.BuildStatusSucceeded)
		insert(createWorkflowRun(workflowName, 1, b1), b1, "p1", "ok", 1.00, 1)
		insert(createWorkflowRun(workflowName, 2, b2), b2, "p1", "ok", 2.00, 2)
		insert(createWorkflowRun(workflowName+"-other", 9, other), other, "p1", "ok", 8.00, 8)

		stats, err := factory.WorkflowStats(workflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(HaveLen(2))
		Expect(*stats[0].Version).To(Equal(2))
		Expect(stats[0].TotalCostUSD).To(BeNumerically("~", 2.00, 1e-6))
		Expect(*stats[1].Version).To(Equal(1))
		Expect(stats[1].TotalCostUSD).To(BeNumerically("~", 1.00, 1e-6))
	})

	// Workflow-run template retirement destroys the run instance pipeline, and
	// builds_pipeline_id_fkey is ON DELETE CASCADE — so a retired version's
	// builds are gone while its metrics rows (build_id carries no foreign key)
	// remain. Success must then be read from the durable execution_status,
	// which migration 1773106103 defines as the immutable copy of that very
	// build's terminal status; otherwise a retired version keeps its runs and
	// cost but silently reports a zero success rate.
	It("keeps counting success after the planned build is reclaimed", func() {
		buildID := createBuildWithStatus(db.BuildStatusSucceeded)
		runID := createWorkflowRun(workflowName, 4, buildID)
		Expect(dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET status = 'succeeded', execution_status = 'succeeded'
			WHERE id = $1
		`, int64(runID))).ToNot(BeNil())
		insert(runID, buildID, "p1", "ok", 1.00, 2)

		before, err := factory.WorkflowStats(workflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(before).To(HaveLen(1))
		Expect(before[0].SucceededRuns).To(Equal(1))

		// exactly what the instance pipeline's deletion cascades
		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())

		after, err := factory.WorkflowStats(workflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(HaveLen(1))
		Expect(after[0].Runs).To(Equal(1))
		Expect(after[0].TotalCostUSD).To(BeNumerically("~", 1.00, 1e-6))
		Expect(after[0].SucceededRuns).To(Equal(1))
	})

	// The durable fallback is scoped to the run's own planned build: a metric
	// row from some other build must never inherit the planned build's
	// outcome.
	It("does not lend the planned build's outcome to another build's metrics", func() {
		plannedBuild := createBuildWithStatus(db.BuildStatusSucceeded)
		otherBuild := createBuildWithStatus(db.BuildStatusFailed)
		runID := createWorkflowRun(workflowName, 5, plannedBuild)
		Expect(dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET status = 'succeeded', execution_status = 'succeeded'
			WHERE id = $1
		`, int64(runID))).ToNot(BeNil())
		insert(runID, otherBuild, "p1", "ok", 1.00, 2)

		_, err := dbConn.Exec(`DELETE FROM builds WHERE id IN ($1, $2)`, plannedBuild, otherBuild)
		Expect(err).NotTo(HaveOccurred())

		stats, err := factory.WorkflowStats(workflowName)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(HaveLen(1))
		Expect(stats[0].Runs).To(Equal(1))
		Expect(stats[0].SucceededRuns).To(Equal(0))
	})
})
