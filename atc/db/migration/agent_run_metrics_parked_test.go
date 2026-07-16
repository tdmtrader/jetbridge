package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// PARK-V2 (shared-contracts §1.8, 2026-07-10): the status CHECK gains
// 'parked' and the table gains session_id — the ingest API (ParseSubmission)
// accepts 'parked', so the real schema must too, or park-exit partial
// ingestion 500s and the row is lost.
var _ = Describe("Agent run metrics parked status", func() {
	const preMigrationVersion = 1773106060
	const postMigrationVersion = 1773106061

	Context("Up", func() {
		It("widens the status CHECK to 'parked' and backfills session_id on existing rows", func() {
			db := postgresRunner.OpenDBAtVersion(preMigrationVersion)
			By("pinning that the pre-migration CHECK rejects 'parked'")
			_, err := db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(1, 'pre', 'review', 'parked')`)
			Expect(err).To(HaveOccurred())
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(1, 'pre', 'review', 'ok')`)
			Expect(err).NotTo(HaveOccurred())
			db.Close()

			db = postgresRunner.OpenDBAtVersion(postMigrationVersion)
			defer db.Close()

			By("accepting every contract status value, including 'parked'")
			for i, status := range []string{"ok", "failed", "error", "parked"} {
				_, err := db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(2, $1, 'review', $2)`, status, status)
				Expect(err).NotTo(HaveOccurred(), "status %d: %s", i, status)
			}

			By("still rejecting unknown statuses via CHECK")
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(3, 'p', 'review', 'pass')`)
			Expect(err).To(HaveOccurred())

			By("defaulting session_id to '' on pre-existing and new rows")
			var preSessionID, newSessionID string
			Expect(db.QueryRow(`SELECT session_id FROM agent_run_metrics WHERE build_id = 1`).
				Scan(&preSessionID)).To(Succeed())
			Expect(preSessionID).To(BeEmpty())
			Expect(db.QueryRow(`SELECT session_id FROM agent_run_metrics WHERE build_id = 2 AND status = 'parked'`).
				Scan(&newSessionID)).To(Succeed())
			Expect(newSessionID).To(BeEmpty())

			By("storing a non-empty session_id")
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status, session_id) VALUES(4, 'q', 'review', 'parked', 'sess-42')`)
			Expect(err).NotTo(HaveOccurred())

			By("rejecting NULL session_id")
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status, session_id) VALUES(5, 'r', 'review', 'ok', NULL)`)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Down", func() {
		It("drops session_id, deletes parked rows, and restores the narrow CHECK", func() {
			db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
			_, err := db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status, session_id) VALUES(1, 'a', 'review', 'parked', 'sess-42')`)
			Expect(err).NotTo(HaveOccurred())
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(2, 'b', 'review', 'ok')`)
			Expect(err).NotTo(HaveOccurred())
			db.Close()

			db = postgresRunner.OpenDBAtVersion(preMigrationVersion)
			defer db.Close()

			By("deleting parked rows so the narrow CHECK validates")
			var count int
			Expect(db.QueryRow(`SELECT COUNT(*) FROM agent_run_metrics`).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1))
			var status string
			Expect(db.QueryRow(`SELECT status FROM agent_run_metrics WHERE build_id = 2`).Scan(&status)).To(Succeed())
			Expect(status).To(Equal("ok"))

			By("rejecting 'parked' again")
			_, err = db.Exec(`INSERT INTO agent_run_metrics(build_id, plan_id, step_name, status) VALUES(3, 'c', 'review', 'parked')`)
			Expect(err).To(HaveOccurred())

			By("dropping the session_id column")
			Expect(db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'agent_run_metrics' AND column_name = 'session_id'`).
				Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})
	})
})
