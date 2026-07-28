package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("drop agent principals migration", func() {
	const beforeVersion, targetVersion = 1773106139, 1773106140

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

	It("drops every principal token row and restores only an empty historical schema on rollback", func() {
		_, err := database.Exec(`
			INSERT INTO agent_principals (name, token_prefix, token_hash, scopes)
			VALUES ('retired-principal', 'cap1.retired', 'retired-hash', ARRAY['tickets:write'])
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var absent bool
		Expect(database.QueryRow(`SELECT to_regclass('agent_principals') IS NULL`).Scan(&absent)).To(Succeed())
		Expect(absent).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var rows int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_principals`).Scan(&rows)).To(Succeed())
		Expect(rows).To(Equal(0), "rollback must never recreate retired credential authority")
	})
})
