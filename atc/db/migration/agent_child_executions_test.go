package migration_test

import (
	"database/sql"
	"strings"

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

		var resultColumns int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_child_executions'
			  AND column_name IN ('result_snapshot_type', 'result_snapshot_digest', 'result_body')
		`).Scan(&resultColumns)).To(Succeed())
		Expect(resultColumns).To(Equal(3))

		var workspaceColumns int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'agent_child_executions'
			  AND column_name IN ('workspace_snapshot_id', 'workspace_snapshot_type', 'workspace_snapshot_digest')
		`).Scan(&workspaceColumns)).To(Succeed())
		Expect(workspaceColumns).To(Equal(3))

		rows, err := database.Query(`
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = 'agent_child_executions'::regclass AND contype = 'c'
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var definitions []string
		for rows.Next() {
			var definition string
			Expect(rows.Scan(&definition)).To(Succeed())
			definitions = append(definitions, definition)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		terminalConstraint := strings.Join(definitions, "\n")
		Expect(terminalConstraint).To(ContainSubstring("result_snapshot_type"))
		Expect(terminalConstraint).To(ContainSubstring("result_snapshot_digest"))
		Expect(terminalConstraint).To(ContainSubstring("result_body"))
		Expect(terminalConstraint).To(ContainSubstring("state = 'succeeded'"))
		Expect(terminalConstraint).To(ContainSubstring("workspace_snapshot_type"))
		Expect(terminalConstraint).To(ContainSubstring("repository-change/v1"))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name IN ('agent_child_executions', 'agent_child_execution_events')
		`).Scan(&tables)).To(Succeed())
		Expect(tables).To(BeZero())
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM information_schema.table_constraints
			WHERE table_schema = 'public' AND table_name = 'agent_snapshots'
			  AND constraint_name = 'agent_snapshots_id_team_key'
		`).Scan(&constraints)).To(Succeed())
		Expect(constraints).To(BeZero())
	})
})
