package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	hangarCaptureRowsVersion = 1789793152
	captureIsOneRowVersion   = 1789793153
)

func relationExists(database *sql.DB, name string) bool {
	var exists bool
	Expect(database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&exists)).To(Succeed())
	return exists
}

// seedBypassingConstraints writes a row the way history was written, without
// re-deriving the whole Run graph behind it: replica mode skips foreign keys
// and triggers for this session only.
func seedBypassingConstraints(database *sql.DB, statement string, args ...any) {
	database.SetMaxOpenConns(1)
	_, err := database.Exec(`SET session_replication_role = replica`)
	Expect(err).NotTo(HaveOccurred())
	_, err = database.Exec(statement, args...)
	Expect(err).NotTo(HaveOccurred())
	_, err = database.Exec(`SET session_replication_role = DEFAULT`)
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("Capture is one row migrations", func() {
	var database *sql.DB

	closeDB := func() {
		if database != nil {
			Expect(database.Close()).To(Succeed())
			database = nil
		}
	}

	BeforeEach(func() {
		DeferCleanup(closeDB)
	})

	expectRefusalAt152 := func(table string) {
		closeDB()
		_, err := postgresRunner.TryOpenDBAtVersion(captureIsOneRowVersion)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("old output history exists"))
		Expect(err.Error()).To(ContainSubstring(table + " (1 rows)"))
		Expect(err.Error()).To(ContainSubstring("irreversible for output history"))

		database = postgresRunner.OpenDBAtVersion(hangarCaptureRowsVersion)
		Expect(relationExists(database, table)).To(BeTrue(), "the refusal rolled back; nothing was dropped")
	}

	Describe("1789793153 refuses old output history", func() {
		BeforeEach(func() {
			database = postgresRunner.OpenDBAtVersion(hangarCaptureRowsVersion)
		})

		It("refuses while a running Run owns an output start, naming the table", func() {
			seedBypassingConstraints(database, `
				INSERT INTO pipeline_run_output_starts
					(run_id, build_id, task_id, result_name, task_name, node_name, node_uid, handoff_id)
				VALUES (1, 1, gen_random_uuid(), 'out', 'task', 'node', 'node-uid', gen_random_uuid())`)
			expectRefusalAt152("pipeline_run_output_starts")
		})

		It("refuses while a handoff predeclaration exists, naming the table and its handoff-bound claim", func() {
			seedBypassingConstraints(database, `
				INSERT INTO hangar_handoff_predeclarations
					(handoff_id, source_hold_id, execution_id, execution_fence, output_name, activation_epoch, capture_deadline_at)
				VALUES ('11111111-1111-1111-1111-111111111111', gen_random_uuid(), gen_random_uuid(), 1, 'out', 1, now() + interval '2 hours')`)
			seedBypassingConstraints(database, `
				INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
				VALUES (gen_random_uuid(), 1, 1, '11111111-1111-1111-1111-111111111111')`)
			expectRefusalAt152("hangar_handoff_predeclarations")

			closeDB()
			_, err := postgresRunner.TryOpenDBAtVersion(captureIsOneRowVersion)
			Expect(err).To(MatchError(ContainSubstring("hangar_claims bound to a handoff (1 rows)")))
		})

		It("migrates a database with no old output history", func() {
			closeDB()
			database = postgresRunner.OpenDBAtVersion(captureIsOneRowVersion)
			Expect(relationExists(database, "hangar_captures")).To(BeTrue())
			Expect(relationExists(database, "hangar_handoff_predeclarations")).To(BeFalse())
			Expect(relationExists(database, "pipeline_run_output_starts")).To(BeFalse())
		})
	})

	It("admits a pending capture at commit, not at insert, only while no integrity finding is open", func() {
		database = postgresRunner.OpenDBAtVersion(captureIsOneRowVersion)
		_, err := database.Exec(`INSERT INTO hangar_integrity_findings (violation, subject) VALUES ('out_of_band_absence', 'object')`)
		Expect(err).NotTo(HaveOccurred())

		insert := `INSERT INTO hangar_captures (execution_id, execution_fence, output_name, node, node_uid, capture_deadline_at)
			VALUES (gen_random_uuid(), 1, 'out', 'node', 'node-uid', now() + interval '1 hour')`

		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(insert)
		Expect(err).NotTo(HaveOccurred(), "the admission trigger is deferred to commit")
		err = tx.Commit()
		Expect(err).To(MatchError(ContainSubstring("unresolved storage integrity findings")))

		var captures int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_captures`).Scan(&captures)).To(Succeed())
		Expect(captures).To(BeZero())

		_, err = database.Exec(`UPDATE hangar_integrity_findings SET resolved_at = now()`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(insert)
		Expect(err).NotTo(HaveOccurred())
	})

})
