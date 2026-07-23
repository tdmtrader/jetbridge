package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent workflow budget reservations migration", func() {
	const beforeVersion, targetVersion = 1773106113, 1773106114
	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator

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
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("ties one positive reservation to a durable workflow run and rolls back cleanly", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var table string
		Expect(database.QueryRow(`SELECT to_regclass('agent_workflow_budget_reservations')::text`).Scan(&table)).To(Succeed())
		Expect(table).To(Equal("agent_workflow_budget_reservations"))

		_, err := database.Exec(`
			INSERT INTO agent_workflow_budget_reservations
				(workflow_run_id, reserved_usd, budget_day)
			VALUES (999999, 1, CURRENT_DATE)
		`)
		Expect(err).To(HaveOccurred(), "foreign keys must reject fabricated workflow runs")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var missing sql.NullString
		Expect(database.QueryRow(`SELECT to_regclass('agent_workflow_budget_reservations')::text`).Scan(&missing)).To(Succeed())
		Expect(missing.Valid).To(BeFalse())
	})
})
