package metric_test

import (
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Counting Database Queries", func() {
	var countingConn db.DbConn

	BeforeEach(func() {
		useEmptyTestDB()

		countingConn = metric.CountQueries(openTestConn("underlying"))

		metric.Metrics.DatabaseQueries.Delta()
	})

	It("passes through calls that are not queries, without counting them", func() {
		Expect(countingConn.Ping()).To(Succeed())
		Expect(countingConn.Name()).To(Equal("underlying"))

		Expect(metric.Metrics.DatabaseQueries.Delta()).To(Equal(float64(0)))
	})

	It("returns the errors from the underlying connection", func() {
		brokenConn := metric.CountQueries(closedTestConn())

		Expect(brokenConn.Ping()).To(MatchError(ContainSubstring("database is closed")))

		_, err := brokenConn.Query("SELECT $1::int", 1)
		Expect(err).To(MatchError(ContainSubstring("database is closed")))
	})

	Describe("query counting", func() {
		It("increments the global (;_;) counter", func() {
			rows, err := countingConn.Query("SELECT $1::int", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(rows.Close()).To(Succeed())

			Expect(metric.Metrics.DatabaseQueries.Delta()).To(Equal(float64(1)))

			_, err = countingConn.Exec("SELECT $1::int", 1)
			Expect(err).NotTo(HaveOccurred())

			var value int
			Expect(countingConn.QueryRow("SELECT $1::int", 1).Scan(&value)).To(Succeed())
			Expect(value).To(Equal(1))

			Expect(metric.Metrics.DatabaseQueries.Delta()).To(Equal(float64(2)))

			By("working in transactions")
			tx, err := countingConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			rows, err = tx.Query("SELECT $1::int", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(rows.Close()).To(Succeed())

			Expect(metric.Metrics.DatabaseQueries.Delta()).To(Equal(float64(1)))
		})
	})
})
