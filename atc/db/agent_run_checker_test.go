package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunChecker", func() {
	var checker *db.AgentRunChecker

	// pipeline_runs is pipeline-runs' migration 1773106031 (contracts §1.5).
	// The Task 1 merge-order addendum lands credentials BEFORE pipeline-runs,
	// so create the table with the exact §1.5 DDL when absent; once
	// 1773106031 merges this becomes a no-op.
	createPipelineRuns := func() {
		_, err := dbConn.Exec(`
			CREATE TABLE IF NOT EXISTS pipeline_runs (
				id                   SERIAL PRIMARY KEY,
				template_pipeline_id INTEGER NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
				instance_pipeline_id INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
				number               INTEGER NOT NULL,
				params               JSONB NOT NULL DEFAULT '{}',
				status               TEXT NOT NULL DEFAULT 'running'
				                     CHECK (status IN ('running','awaiting_human','succeeded','failed','errored','aborted')),
				created_by           TEXT NOT NULL DEFAULT '',
				created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
				completed_at         TIMESTAMPTZ,
				archived             BOOLEAN NOT NULL DEFAULT false
			)`)
		Expect(err).ToNot(HaveOccurred())
	}

	BeforeEach(func() {
		checker = db.NewAgentRunChecker(dbConn)
	})

	It("reports running rows active and finished/absent rows inactive", func() {
		createPipelineRuns()

		var runningID, doneID int
		err := dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, status)
			VALUES ($1, 990001, 'running') RETURNING id`, defaultPipeline.ID()).Scan(&runningID)
		Expect(err).ToNot(HaveOccurred())
		err = dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, status, completed_at)
			VALUES ($1, 990002, 'succeeded', now()) RETURNING id`, defaultPipeline.ID()).Scan(&doneID)
		Expect(err).ToNot(HaveOccurred())

		Expect(checker.RunActive(runningID)).To(BeTrue())
		Expect(checker.RunActive(doneID)).To(BeFalse())
		Expect(checker.RunActive(999999999)).To(BeFalse()) // absent row = inactive
	})

	It("counts awaiting_human runs as active (PARK-V2, contracts §11)", func() {
		createPipelineRuns()

		// The agent-run-<run-id> secret and per-run principal row must
		// survive the wait for the continuation to re-attach.
		var parkedID int
		err := dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, status)
			VALUES ($1, 990003, 'awaiting_human') RETURNING id`, defaultPipeline.ID()).Scan(&parkedID)
		Expect(err).ToNot(HaveOccurred())

		Expect(checker.RunActive(parkedID)).To(BeTrue())
	})

	It("treats an absent pipeline_runs table as no-active-runs (undefined_table)", func() {
		// Each spec gets a fresh DB from the template (suite-level
		// BeforeEach: CreateTestDBFromTemplate), so dropping here cannot
		// leak into other specs.
		_, err := dbConn.Exec(`DROP TABLE IF EXISTS pipeline_runs`)
		Expect(err).ToNot(HaveOccurred())

		active, err := checker.RunActive(1)
		Expect(err).ToNot(HaveOccurred())
		Expect(active).To(BeFalse())
	})
})
