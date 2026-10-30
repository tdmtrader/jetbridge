package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dropPrototypesVersion = 1789793164

var _ = Describe("drop prototypes", func() {
	tableExists := func(version int) bool {
		GinkgoHelper()
		database := postgresRunner.OpenDBAtVersion(version)
		defer database.Close()
		var present bool
		Expect(database.QueryRow("SELECT to_regclass('prototypes') IS NOT NULL").Scan(&present)).To(Succeed())
		return present
	}

	It("removes the table the prototypes section was saved into", func() {
		Expect(tableExists(1789793163)).To(BeTrue())
		Expect(tableExists(dropPrototypesVersion)).To(BeFalse())
	})
})
