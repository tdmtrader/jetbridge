package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("workflow run node occurrences migration", func() {
	const beforeVersion, targetVersion = 1773106156, 1773106157

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

	tableExists := func(name string) bool {
		var exists bool
		Expect(database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	It("creates the projection with both attempt axes in its key", func() {
		Expect(tableExists("agent_workflow_run_node_occurrences")).To(BeFalse())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(tableExists("agent_workflow_run_node_occurrences")).To(BeTrue())

		// A retried step records evidence for several plan copies of one
		// identity, all at recovery attempt 1. A key without retry_attempt
		// would collide on exactly that case.
		var key string
		Expect(database.QueryRow(`
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_run_node_occurrences'::regclass
			  AND contype = 'p'
		`).Scan(&key)).To(Succeed())
		Expect(key).To(ContainSubstring("workflow_run_id"))
		Expect(key).To(ContainSubstring("node_id"))
		Expect(key).To(ContainSubstring("retry_attempt"))
		Expect(key).To(ContainSubstring("attempt"))
	})

	It("adds the plan identity that joins a publication to its node", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var nullable string
		Expect(database.QueryRow(`
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_publication_occurrences'
			  AND column_name = 'plan_id'
		`).Scan(&nullable)).To(Succeed())
		// Rows written before the projection existed have no plan identity to
		// backfill, so the column must admit them.
		Expect(nullable).To(Equal("YES"))
	})

	It("rolls back cleanly, leaving no orphan trigger function", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(tableExists("agent_workflow_run_node_occurrences")).To(BeFalse())

		var functions int
		Expect(database.QueryRow(`
			SELECT count(*) FROM pg_proc
			WHERE proname = 'enforce_agent_workflow_run_node_occurrence_immutability'
		`).Scan(&functions)).To(Succeed())
		Expect(functions).To(BeZero())

		var columns int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_publication_occurrences'
			  AND column_name = 'plan_id'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(BeZero())

		// The down migration must be re-appliable: a rollback that cannot be
		// migrated forward again strands an operator mid-upgrade.
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(tableExists("agent_workflow_run_node_occurrences")).To(BeTrue())
	})
})
