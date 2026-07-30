package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent child executions migration", func() {
	const beforeVersion, targetVersion = 1773106154, 1773106155

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

	It("creates a team-bound immutable child ledger and monotonic event stream", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var tables int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name IN ('agent_child_executions', 'agent_child_execution_events')
		`).Scan(&tables)).To(Succeed())
		Expect(tables).To(Equal(2))

		var constraints int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.table_constraints
			WHERE table_schema = 'public'
			  AND table_name = 'agent_child_executions'
			  AND constraint_type IN ('FOREIGN KEY', 'UNIQUE', 'CHECK')
		`).Scan(&constraints)).To(Succeed())
		Expect(constraints).To(BeNumerically(">=", 10))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name IN ('agent_child_executions', 'agent_child_execution_events')
		`).Scan(&tables)).To(Succeed())
		Expect(tables).To(BeZero())
	})
})
