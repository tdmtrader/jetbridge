package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Seed agent platform user and cost view", func() {
	const postMigrationVersion = 1773106022

	It("seeds the platform service user and creates the daily rollup view", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		By("seeding the §1.13 service user")
		var username, connector string
		Expect(db.QueryRow(`SELECT username, connector FROM users WHERE sub = 'agent-platform'`).
			Scan(&username, &connector)).To(Succeed())
		Expect(username).To(Equal("platform"))
		Expect(connector).To(Equal("local"))

		By("aggregating ledger rows per day/user/source in the view")
		_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, user_name, cost_usd, turns, occurred_at)
			VALUES ('ci_agent', 'alice', 1.5, 2, '2026-07-08T10:00:00Z'),
			       ('ci_agent', 'alice', 0.5, 1, '2026-07-08T11:00:00Z')`)
		Expect(err).NotTo(HaveOccurred())

		var entries, turns int
		var cost float64
		Expect(db.QueryRow(`SELECT entries, turns, cost_usd::float8 FROM agent_cost_daily_rollup
			WHERE day = '2026-07-08' AND user_name = 'alice' AND source = 'ci_agent'`).
			Scan(&entries, &turns, &cost)).To(Succeed())
		Expect(entries).To(Equal(2))
		Expect(turns).To(Equal(3))
		Expect(cost).To(BeNumerically("~", 2.0, 1e-9))
	})
})
