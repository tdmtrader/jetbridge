package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dropSigningKeysAndVarSourcesVersion = 1789793165

var _ = Describe("drop signing keys and var_sources", func() {
	present := func(version int, query string) bool {
		GinkgoHelper()
		database := postgresRunner.OpenDBAtVersion(version)
		defer database.Close()
		var exists bool
		Expect(database.QueryRow(query).Scan(&exists)).To(Succeed())
		return exists
	}

	It("removes the table the idtoken signing keys were kept in", func() {
		const query = "SELECT to_regclass('signing_keys') IS NOT NULL"
		Expect(present(1789793164, query)).To(BeTrue())
		Expect(present(dropSigningKeysAndVarSourcesVersion, query)).To(BeFalse())
	})

	DescribeTable("removes the pipeline columns only var_sources used", func(column string) {
		query := "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'pipelines' AND column_name = '" + column + "')"
		Expect(present(1789793164, query)).To(BeTrue())
		Expect(present(dropSigningKeysAndVarSourcesVersion, query)).To(BeFalse())
	},
		Entry("var_sources", "var_sources"),
		Entry("nonce", "nonce"),
	)
})
