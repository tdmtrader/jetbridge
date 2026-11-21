package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A transaction's after-commit hooks", func() {
	var runs int

	begin := func() db.Tx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).ToNot(HaveOccurred())
		db.AfterCommit(tx, func() { runs++ })
		return tx
	}

	BeforeEach(func() { runs = 0 })

	It("runs nothing when the transaction rolls back", func() {
		tx := begin()
		Expect(tx.Rollback()).To(Succeed())
		Expect(runs).To(BeZero())
	})

	It("runs each hook once on commit, and nothing on a second commit or a rollback after it", func() {
		tx := begin()
		db.AfterCommit(tx, func() { runs += 10 })
		Expect(tx.Commit()).To(Succeed())
		Expect(runs).To(Equal(11))

		Expect(tx.Commit()).ToNot(Succeed())
		db.Rollback(tx)
		Expect(runs).To(Equal(11))
	})
})
