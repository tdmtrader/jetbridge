package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent user lookup", func() {
	var lookup db.AgentUserLookup

	BeforeEach(func() {
		lookup = db.NewAgentUserLookup(dbConn)
	})

	It("resolves users.id by username and reports absence without error", func() {
		Expect(db.NewUserFactory(dbConn).CreateOrUpdateUser("tdm", "local", "sub-1")).To(Succeed())

		var want int
		err := dbConn.QueryRow(`SELECT id FROM users WHERE username = 'tdm' ORDER BY id DESC LIMIT 1`).Scan(&want)
		Expect(err).ToNot(HaveOccurred())

		id, found, err := lookup.FindByUsername("tdm")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(id).To(Equal(want))

		_, found, err = lookup.FindByUsername("ghost")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		// Empty username short-circuits (dispatch calls with UserName "" for
		// system-origin tickets).
		_, found, err = lookup.FindByUsername("")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
