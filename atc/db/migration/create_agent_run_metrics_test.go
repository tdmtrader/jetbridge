package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Create agent run metrics", func() {
	const postMigrationVersion = 1773106060

	It("creates the metrics table with status check, build/plan uniqueness, and defaults", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		By("accepting a minimal row with NULL ticket/run/workflow-version join keys")
		_, err := db.Exec(`INSERT INTO agent_run_metrics(build_id, step_name, status) VALUES(1, 'review', 'ok')`)
		Expect(err).NotTo(HaveOccurred())

		By("accepting every contract status value")
		for i, status := range []string{"ok", "failed", "error"} {
			_, err := db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(2, $1, 'review', $2)`, status, status)
			Expect(err).NotTo(HaveOccurred(), "status %d: %s", i, status)
		}

		By("rejecting unknown statuses via CHECK")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(3, 'p', 'review', 'pass')`)
		Expect(err).To(HaveOccurred())

		By("rejecting duplicate (build_id, plan_id) pairs")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(4, 'plan-a', 'review', 'ok')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(4, 'plan-a', 'other', 'failed')`)
		Expect(err).To(HaveOccurred())

		By("allowing the same plan_id across different builds")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(5, 'plan-a', 'review', 'ok')`)
		Expect(err).NotTo(HaveOccurred())

		By("accepting ticket-scoped rows before agent_tickets exists (plain column, no FK)")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(ticket_id, pipeline_run_id, build_id, plan_id, step_name, status) VALUES(42, 7, 6, 'plan-b', 'review', 'ok')`)
		Expect(err).NotTo(HaveOccurred())

		By("preserving NUMERIC(12,6) cost precision")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status, cost_usd) VALUES(7, 'plan-c', 'review', 'ok', 0.123456)`)
		Expect(err).NotTo(HaveOccurred())
		var cost string
		Expect(db.QueryRow(`SELECT cost_usd::text FROM agent_run_metrics WHERE build_id = 7`).
			Scan(&cost)).To(Succeed())
		Expect(cost).To(Equal("0.123456"))

		By("defaulting text columns, token counters, and timestamps")
		var planID, workflowName, workflowHash, summary, model, eventsArtifact string
		var inputTokens, outputTokens, cacheRead, cacheCreation int64
		var turns, wallTime int
		var createdAtNotNull bool
		Expect(db.QueryRow(`
			SELECT plan_id, workflow_name, workflow_hash, summary, model, events_artifact,
			       input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			       turns, wall_time_seconds, created_at IS NOT NULL
			FROM agent_run_metrics WHERE build_id = 1
		`).Scan(&planID, &workflowName, &workflowHash, &summary, &model, &eventsArtifact,
			&inputTokens, &outputTokens, &cacheRead, &cacheCreation,
			&turns, &wallTime, &createdAtNotNull)).To(Succeed())
		Expect(planID).To(BeEmpty())
		Expect(workflowName).To(BeEmpty())
		Expect(workflowHash).To(BeEmpty())
		Expect(summary).To(BeEmpty())
		Expect(model).To(BeEmpty())
		Expect(eventsArtifact).To(BeEmpty())
		Expect(inputTokens).To(BeZero())
		Expect(outputTokens).To(BeZero())
		Expect(cacheRead).To(BeZero())
		Expect(cacheCreation).To(BeZero())
		Expect(turns).To(BeZero())
		Expect(wallTime).To(BeZero())
		Expect(createdAtNotNull).To(BeTrue())

		By("storing JSONB results and event counts")
		_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status, results, event_counts)
			VALUES(8, 'plan-d', 'review', 'ok', '{"status":"pass"}', '{"tool.call": 87}')`)
		Expect(err).NotTo(HaveOccurred())
		var toolCalls int
		Expect(db.QueryRow(`SELECT (event_counts->>'tool.call')::int FROM agent_run_metrics WHERE build_id = 8`).
			Scan(&toolCalls)).To(Succeed())
		Expect(toolCalls).To(Equal(87))

		By("having the ticket and workflow indexes")
		var count int
		Expect(db.QueryRow(`SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'agent_run_metrics' AND indexname IN ('agent_run_metrics_build_plan', 'agent_run_metrics_ticket', 'agent_run_metrics_workflow')`).
			Scan(&count)).To(Succeed())
		Expect(count).To(Equal(3))
	})
})
