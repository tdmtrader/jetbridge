package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const captureKindVersion = 1789793155

var _ = Describe("Run cancellation capture kind migration", func() {
	var database *sql.DB

	kinds := func() []string {
		GinkgoHelper()
		rows, err := database.Query(`SELECT kind FROM pipeline_run_cancellation_operations ORDER BY id`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var found []string
		for rows.Next() {
			var kind string
			Expect(rows.Scan(&kind)).To(Succeed())
			found = append(found, kind)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return found
	}

	progress := func() [2]int {
		GinkgoHelper()
		var indices [2]int
		Expect(database.QueryRow(`SELECT next_discovery_kind, next_claim_kind
			FROM pipeline_run_cancellation_progress WHERE run_id = 1`).Scan(&indices[0], &indices[1])).To(Succeed())
		return indices
	}

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(captureKindVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	It("renames the live capture kind, drops the two dead kinds and remaps the cycle, and goes back", func() {
		setup, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		_, err = setup.Exec(`INSERT INTO pipeline_run_cancellation_progress (run_id, next_discovery_kind, next_claim_kind) VALUES (1, 3, 7)`)
		Expect(err).NotTo(HaveOccurred())
		for _, kind := range []string{"handoff_classify", "capture_cancel_or_settle", "source_hold_release", "build_abort"} {
			_, err = setup.Exec(`INSERT INTO pipeline_run_cancellation_operations (run_id, kind, subject) VALUES (1, $1, 'x')`, kind)
			Expect(err).NotTo(HaveOccurred())
			_, err = setup.Exec(`INSERT INTO pipeline_run_cancellation_cursors (run_id, kind) VALUES (1, $1)`, kind)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(setup.Commit()).To(Succeed())
		Expect(database.Close()).To(Succeed())

		database = postgresRunner.OpenDBAtVersion(captureKindVersion)
		Expect(kinds()).To(Equal([]string{"capture_cancel_or_settle", "build_abort"}))
		Expect(progress()).To(Equal([2]int{2, 5}), "source_hold_release goes on to build_abort; terminalize is the last of six")
		var cursors int
		Expect(database.QueryRow(`SELECT count(*) FROM pipeline_run_cancellation_cursors
			WHERE kind IN ('handoff_classify', 'source_hold_release')`).Scan(&cursors)).To(Succeed())
		Expect(cursors).To(BeZero())
		_, err = database.Exec(`INSERT INTO pipeline_run_cancellation_operations (run_id, kind, subject) VALUES (1, 'handoff_classify', 'y')`)
		Expect(err).To(HaveOccurred(), "the handoff kind is refused")
		_, err = database.Exec(`UPDATE pipeline_run_cancellation_operations SET kind = 'build_abort' WHERE kind = 'capture_cancel_or_settle'`)
		Expect(err).To(MatchError(ContainSubstring("immutable")), "the identity trigger is back on")
		Expect(database.Close()).To(Succeed())

		database = postgresRunner.OpenDBAtVersion(captureKindVersion - 1)
		Expect(kinds()).To(Equal([]string{"handoff_classify", "build_abort"}))
		Expect(progress()).To(Equal([2]int{4, 7}))
	})
})
