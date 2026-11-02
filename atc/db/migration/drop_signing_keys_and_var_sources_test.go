package migration_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const restoreSigningKeysAndVarSourcesVersion = 1789793166

var _ = Describe("signing keys and var_sources", func() {
	present := func(version int, query string) bool {
		GinkgoHelper()
		database := postgresRunner.OpenDBAtVersion(version)
		defer database.Close()
		var exists bool
		Expect(database.QueryRow(query).Scan(&exists)).To(Succeed())
		return exists
	}

	It("keeps the table the idtoken signing keys are kept in", func() {
		const query = "SELECT to_regclass('signing_keys') IS NOT NULL"
		Expect(present(1789793165, query)).To(BeTrue())
		Expect(present(restoreSigningKeysAndVarSourcesVersion, query)).To(BeTrue())
	})

	DescribeTable("keeps the pipeline columns var_sources uses", func(column string) {
		query := "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'pipelines' AND column_name = '" + column + "')"
		Expect(present(1789793165, query)).To(BeTrue())
		Expect(present(restoreSigningKeysAndVarSourcesVersion, query)).To(BeTrue())
	},
		Entry("var_sources", "var_sources"),
		Entry("nonce", "nonce"),
	)

	It("re-creates what the old 1789793165 dropped", func() {
		database := postgresRunner.OpenDBAtVersion(1789793165)
		defer database.Close()
		_, err := database.Exec(`DROP TABLE signing_keys; ALTER TABLE pipelines DROP COLUMN var_sources, DROP COLUMN nonce`)
		Expect(err).NotTo(HaveOccurred())

		restore, err := os.ReadFile("migrations/1789793166_restore_signing_keys_and_var_sources.up.sql")
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(string(restore))
		Expect(err).NotTo(HaveOccurred())

		var table bool
		Expect(database.QueryRow("SELECT to_regclass('signing_keys') IS NOT NULL").Scan(&table)).To(Succeed())
		Expect(table).To(BeTrue())
		var columns int
		Expect(database.QueryRow("SELECT count(*) FROM information_schema.columns WHERE table_name = 'pipelines' AND column_name IN ('var_sources', 'nonce')").Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(2))
	})
})
