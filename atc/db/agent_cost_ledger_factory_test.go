package db_test

import (
	"fmt"
	"strings"
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

	// insertLedgerWorkflowRun creates a real agent_workflow_runs row so the
	// ledger's workflow_run_id FK (and the workflow rollup's join) has a
	// target. Returns the run id.
	insertLedgerWorkflowRun := func(workflowName string, version int) int64 {
		suffix := time.Now().UnixNano()
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, $2, $3, 'schema_version: 3', 'tdm', 3, 1)
			RETURNING id
		`, workflowName, version, fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID)).To(Succeed())
		var runID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $4, $5, 3, 1, $6, $7, '{}', $8, 'manual', '', 'tdm', 'running')
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, workflowName, version,
			strings.Repeat("a", 64), fmt.Sprintf("key-%d", suffix), strings.Repeat("b", 64),
		).Scan(&runID)).To(Succeed())
		return runID
	}

	It("inserts run-attributed spend and sums it over a time window", func() {
		at := time.Now()
		runID := insertLedgerWorkflowRun("code-review", 1)
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, WorkflowRunID: &runID, FunctionID: "review",
			CostUSD: 1.25, InputTokens: 100, OutputTokens: 50, Turns: 3,
			Model: "claude-sonnet-5", UserName: "alice", OccurredAt: at,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, CostUSD: 9.0, OccurredAt: at, // unbound CI spend
		})).To(Succeed())

		var storedRun *int64
		var storedFunction string
		Expect(dbConn.QueryRow(
			`SELECT workflow_run_id, function_id FROM agent_cost_ledger WHERE cost_usd = 1.25`,
		).Scan(&storedRun, &storedFunction)).To(Succeed())
		Expect(storedRun).ToNot(BeNil())
		Expect(*storedRun).To(Equal(runID))
		Expect(storedFunction).To(Equal("review"))

		By("leaving unbound CI spend with a NULL run and an empty function")
		Expect(dbConn.QueryRow(
			`SELECT workflow_run_id, function_id FROM agent_cost_ledger WHERE cost_usd = 9.0`,
		).Scan(&storedRun, &storedFunction)).To(Succeed())
		Expect(storedRun).To(BeNil())
		Expect(storedFunction).To(BeEmpty())

		windowSpent, err := ledger.SpentSince(at.Add(-time.Minute))
		Expect(err).ToNot(HaveOccurred())
		Expect(windowSpent).To(BeNumerically("~", 10.25, 1e-9))
	})

	It("releases the run identity when the run row is deleted, keeping the spend", func() {
		runID := insertLedgerWorkflowRun("code-review", 2)
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, WorkflowRunID: &runID, CostUSD: 2.0,
		})).To(Succeed())

		_, err := dbConn.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, runID)
		Expect(err).ToNot(HaveOccurred())

		var rows int
		var storedRun *int64
		Expect(dbConn.QueryRow(
			`SELECT count(*), max(workflow_run_id) FROM agent_cost_ledger`,
		).Scan(&rows, &storedRun)).To(Succeed())
		Expect(rows).To(Equal(1))
		Expect(storedRun).To(BeNil())
	})

	It("rejects a source the platform can no longer produce", func() {
		_, err := dbConn.Exec(
			`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES('harvest_judge', 1)`)
		Expect(err).To(HaveOccurred())
	})

	It("defaults occurred_at to now and sums since a cutoff", func() {
		old := time.Now().Add(-48 * time.Hour)
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, CostUSD: 5, OccurredAt: old})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, CostUSD: 2})).To(Succeed()) // zero -> now()

		spent, err := ledger.SpentSince(time.Now().Add(-time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 2.0, 1e-9))

		spent, err = ledger.SpentSince(time.Now().Add(-72 * time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(spent).To(BeNumerically("~", 7.0, 1e-9))
	})

	It("rolls up by day, user, workflow, model, and step", func() {
		day1 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
		day2 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
		runID := insertLedgerWorkflowRun("review", 1)
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 1, Turns: 2,
			InputTokens: 10, OccurredAt: day1,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, OccurredAt: day2,
		})).To(Succeed())
		Expect(ledger.Insert(budget.LedgerEntry{
			Source: budget.SourceAgentStep, UserName: "bob", WorkflowRunID: &runID,
			FunctionID: "review", CostUSD: 4, OccurredAt: day2,
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

		By("keying the workflow rollup on the run's workflow name")
		rows, err = ledger.Rollup(budget.GroupByWorkflow, since, time.Time{})
		Expect(err).ToNot(HaveOccurred())
		// only run-bound spend joins; the two unbound CI rows drop out
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Key).To(Equal("review"))
		Expect(rows[0].CostUSD).To(BeNumerically("~", 4.0, 1e-9))

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
