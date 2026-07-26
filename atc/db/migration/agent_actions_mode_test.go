package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent actions mode migration", func() {
	const beforeVersion, targetVersion = 1773106127, 1773106128
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

	It("defaults existing rows to active and lets the switch create a row without a dispatcher mode", func() {
		_, err := database.Exec(`
			INSERT INTO agent_settings (id, dispatcher_mode, updated_by)
			VALUES (1, 'active', 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var mode string
		var actionsUpdatedAt sql.NullTime
		Expect(database.QueryRow(`
			SELECT actions_mode, actions_updated_at FROM agent_settings WHERE id = 1
		`).Scan(&mode, &actionsUpdatedAt)).To(Succeed())
		Expect(mode).To(Equal("active"))
		Expect(actionsUpdatedAt.Valid).To(BeFalse())

		_, err = database.Exec(`DELETE FROM agent_settings`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_settings (id, actions_mode, actions_updated_at, actions_updated_by)
			VALUES (1, 'suppressed', now(), 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		var dispatcherMode sql.NullString
		Expect(database.QueryRow(`
			SELECT dispatcher_mode FROM agent_settings WHERE id = 1
		`).Scan(&dispatcherMode)).To(Succeed())
		Expect(dispatcherMode.Valid).To(BeFalse())
	})

	It("refuses an unrecognized actions mode", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		_, err := database.Exec(`
			INSERT INTO agent_settings (id, dispatcher_mode, actions_mode) VALUES (1, 'off', 'halt')
		`)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("actions_mode"))
	})

	It("rolls back by pinning switch-created rows to a dormant dispatcher", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_settings (id, actions_mode, actions_updated_at, actions_updated_by)
			VALUES (1, 'suppressed', now(), 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var dispatcherMode, updatedBy string
		Expect(database.QueryRow(`
			SELECT dispatcher_mode, updated_by FROM agent_settings WHERE id = 1
		`).Scan(&dispatcherMode, &updatedBy)).To(Succeed())
		Expect(dispatcherMode).To(Equal("off"))
		// The rollback signs its own backfill: attributing 'off' to the last
		// admin who touched the row would hide that a migration, not a person,
		// stopped the dispatcher.
		Expect(updatedBy).To(Equal("migration-1773106128-down"))

		var columns int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_settings' AND column_name = 'actions_mode'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(0))
	})
})
