package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const runCredentialHandoffVersion = 1789793143

var _ = Describe("Run credential handoff retention", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(runCredentialHandoffVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	It("refuses a populated down migration and permits an empty down migration", func() {
		// The rollback's contract is about retained evidence being present, not
		// the older tables that normally establish its foreign keys. Disable
		// those constraints only to isolate this migration's refusal boundary.
		_, err := database.Exec(`ALTER TABLE pipeline_run_credential_handoffs DISABLE TRIGGER ALL`)
		Expect(err).ToNot(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO pipeline_run_credential_handoffs (run_id, handoff_id)
			VALUES (1, '11111111-1111-4111-8111-111111111111')`)
		Expect(err).ToNot(HaveOccurred())

		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(runCredentialHandoffVersion - 1)
		Expect(err).To(MatchError(ContainSubstring("cannot discard retained Run credential handoffs")))

		database = postgresRunner.OpenDBAtVersion(runCredentialHandoffVersion)
		_, err = database.Exec(`DELETE FROM pipeline_run_credential_handoffs`)
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Close()).To(Succeed())
		_, err = postgresRunner.TryOpenDBAtVersion(runCredentialHandoffVersion - 1)
		Expect(err).ToNot(HaveOccurred())
	})
})
