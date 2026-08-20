package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run retention cutoff bound", func() {
	It("executes the production cutoff expression at the declared TTL bound", func() {
		var cutoff time.Time
		err := dbConn.QueryRow(
			`SELECT now() - $1 * interval '1 day'`,
			atc.MaxRunRetentionTTLDays,
		).Scan(&cutoff)
		Expect(err).NotTo(HaveOccurred())
		Expect(cutoff).NotTo(BeZero())
	})
})
