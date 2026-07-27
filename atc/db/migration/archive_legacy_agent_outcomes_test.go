package migration_test

import (
	"database/sql"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The legacy agent_outcomes compatibility table is retired: the migration
// renames it to an inert archive and creates the unresolved report beside it.
// Durable dispositions live in agent_workflow_outcomes.
var _ = Describe("legacy agent outcomes migration", func() {
	const beforeVersion, targetVersion = 1773106124, 1773106125

	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator
	var seq int

	next := func() int {
		seq++
		return seq
	}

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		migrator = migration.NewMigrator(database, lock.NewLockFactory(lockDB, noop, noop))
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		seq = 0
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	insertLegacyOutcome := func() int {
		ticketID := next()
		_, err := database.Exec(`
			INSERT INTO agent_outcomes (ticket_id, repo, branch, merge_state, disposition, disposed_by)
			VALUES ($1, 'acme/widgets', $2, 'merged', '', 'reviewer')
		`, ticketID, fmt.Sprintf("agent/ticket-%d", ticketID))
		Expect(err).NotTo(HaveOccurred())
		return ticketID
	}

	archiveHas := func(ticketID int) bool {
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_legacy_outcomes_archive WHERE ticket_id = $1)`, ticketID).Scan(&exists)).To(Succeed())
		return exists
	}

	tableExists := func(name string) bool {
		var exists bool
		Expect(database.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)
		`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	It("archives the compatibility table and creates the unresolved report", func() {
		ticketID := insertLegacyOutcome()

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(tableExists("agent_outcomes")).To(BeFalse())
		Expect(tableExists("agent_legacy_outcomes_archive")).To(BeTrue())
		Expect(tableExists("agent_legacy_outcomes_unresolved")).To(BeTrue())
		Expect(archiveHas(ticketID)).To(BeTrue())

		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_legacy_outcomes_unresolved`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})

	It("round-trips down and up, preserving the archived rows", func() {
		ticketID := insertLegacyOutcome()

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(archiveHas(ticketID)).To(BeTrue())

		By("down restoring agent_outcomes and dropping the report")
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(tableExists("agent_outcomes")).To(BeTrue())
		Expect(tableExists("agent_legacy_outcomes_archive")).To(BeFalse())
		Expect(tableExists("agent_legacy_outcomes_unresolved")).To(BeFalse())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_outcomes WHERE ticket_id = $1`, ticketID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))

		By("re-applying the migration cleanly")
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(archiveHas(ticketID)).To(BeTrue())
	})
})
