package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent experiment budget reservations migration", func() {
	const beforeVersion, targetVersion = 1773106111, 1773106112
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

	It("persists one nonnegative reservation per cell and rolls back cleanly", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var table string
		Expect(database.QueryRow(`SELECT to_regclass('agent_experiment_budget_reservations')::text`).Scan(&table)).To(Succeed())
		Expect(table).To(Equal("agent_experiment_budget_reservations"))

		_, err := database.Exec(`
			INSERT INTO agent_experiment_budget_reservations
				(cell_id, experiment_id, reserved_usd, max_tokens, state, budget_day)
			VALUES (999999, 999999, 1, 100, 'active', CURRENT_DATE)
		`)
		Expect(err).To(HaveOccurred(), "foreign keys must reject fabricated ownership")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var missing sql.NullString
		Expect(database.QueryRow(`SELECT to_regclass('agent_experiment_budget_reservations')::text`).Scan(&missing)).To(Succeed())
		Expect(missing.Valid).To(BeFalse())
	})
})
