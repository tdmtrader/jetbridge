package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dropCertCacheVersion = 1789793163

var _ = Describe("drop cert_cache", func() {
	tableExists := func(version int) bool {
		GinkgoHelper()
		database := postgresRunner.OpenDBAtVersion(version)
		defer database.Close()
		var present bool
		Expect(database.QueryRow("SELECT to_regclass('cert_cache') IS NOT NULL").Scan(&present)).To(Succeed())
		return present
	}

	It("removes the autocert cache the ACME listener used", func() {
		Expect(tableExists(1789793161)).To(BeTrue())
		Expect(tableExists(dropCertCacheVersion)).To(BeFalse())
	})
})
