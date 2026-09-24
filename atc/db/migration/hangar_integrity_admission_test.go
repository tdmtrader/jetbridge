package migration_test

import (
	"database/sql"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const hangarIntegrityAdmissionVersion = 1789793149

var _ = Describe("Hangar integrity admission migration", func() {
	var database *sql.DB
	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(hangarIntegrityAdmissionVersion - 1)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})
	It("preserves historical evidence and replaces the gate reversibly", func() {
		_, err := database.Exec(`INSERT INTO hangar_output_activation_epochs(epoch_id) VALUES(1)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO hangar_policy_snapshots
     (activation_epoch,bucket_fingerprint,metageneration,policy_hash,lifecycle_delete_rules,state)
     VALUES(1,'gs://bucket',1,'legacy',1,'at_risk')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`INSERT INTO hangar_policy_violations(activation_epoch,violation,subject,detail)
     VALUES(1,'out_of_band_absence','object','missing generation')`)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(hangarIntegrityAdmissionVersion)
		var snapshots, findings int
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_policy_snapshots`).Scan(&snapshots)).To(Succeed())
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_policy_violations WHERE resolved_at IS NULL`).Scan(&findings)).To(Succeed())
		Expect(snapshots).To(Equal(1))
		Expect(findings).To(Equal(1))
		var body string
		Expect(database.QueryRow(`SELECT pg_get_functiondef('hangar_check_policy_admission'::regproc)`).Scan(&body)).To(Succeed())
		Expect(body).To(ContainSubstring("unresolved storage integrity"))
		Expect(body).NotTo(ContainSubstring("hangar_policy_snapshots"))
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(hangarIntegrityAdmissionVersion - 1)
		Expect(database.QueryRow(`SELECT pg_get_functiondef('hangar_check_policy_admission'::regproc)`).Scan(&body)).To(Succeed())
		Expect(body).To(ContainSubstring("hangar_policy_snapshots"))
		Expect(database.QueryRow(`SELECT count(*) FROM hangar_policy_violations WHERE resolved_at IS NULL`).Scan(&findings)).To(Succeed())
		Expect(findings).To(Equal(1))
	})
})
