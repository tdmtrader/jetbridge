package migration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Create agent cost ledger", func() {
	const postMigrationVersion = 1773106021

	It("creates the append-only ledger with nullable join keys and source check", func() {
		db := postgresRunner.OpenDBAtVersion(postMigrationVersion)
		defer db.Close()

		By("accepting a minimal row with NULL user/ticket/run join keys")
		_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES('ci_agent', 0.123456)`)
		Expect(err).NotTo(HaveOccurred())

		By("accepting every contract source value")
		for _, source := range []string{"agent_step", "gateway", "harvest_judge", "retrospective", "ci_agent", "probe"} {
			_, err := db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES($1, 0)`, source)
			Expect(err).NotTo(HaveOccurred(), source)
		}

		By("rejecting unknown sources via CHECK")
		_, err = db.Exec(`INSERT INTO agent_cost_ledger(source, cost_usd) VALUES('slack', 0)`)
		Expect(err).To(HaveOccurred())

		By("accepting a ticket-scoped row before agent_tickets exists (plain column, no FK)")
		_, err = db.Exec(`INSERT INTO agent_cost_ledger(source, ticket_id, cost_usd) VALUES('agent_step', 42, 1.5)`)
		Expect(err).NotTo(HaveOccurred())

		By("preserving NUMERIC(12,6) precision")
		var cost string
		Expect(db.QueryRow(`SELECT cost_usd::text FROM agent_cost_ledger WHERE cost_usd = 0.123456`).
			Scan(&cost)).To(Succeed())
		Expect(cost).To(Equal("0.123456"))
	})
})
