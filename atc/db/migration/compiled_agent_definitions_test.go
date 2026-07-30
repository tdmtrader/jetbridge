package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("compiled agent definitions migration", func() {
	const beforeVersion, targetVersion = 1773106155, 1773106156

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
	)

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

	It("adds one nullable durable compiled representation for workflow and node rows", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var nullable string
		Expect(database.QueryRow(`
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_workflow_definitions'
			  AND column_name = 'compiled_definition'
		`).Scan(&nullable)).To(Succeed())
		Expect(nullable).To(Equal("YES"))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var columns int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_workflow_definitions'
			  AND column_name = 'compiled_definition'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(BeZero())
	})
})
