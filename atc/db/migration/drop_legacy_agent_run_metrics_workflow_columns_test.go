package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Batch 1: the v2 workflow tags on agent_run_metrics are gone; workflow
// identity is read through agent_workflow_runs.
var _ = Describe("drop legacy agent run metrics workflow columns migration", func() {
	const beforeVersion, targetVersion = 1773106129, 1773106130

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

	metricColumns := func() []string {
		rows, err := database.Query(`
			SELECT column_name FROM information_schema.columns
			WHERE table_name = 'agent_run_metrics' ORDER BY ordinal_position
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			cols = append(cols, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return cols
	}

	hasWorkflowIndex := func() bool {
		var exists bool
		Expect(database.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM pg_indexes
			WHERE tablename = 'agent_run_metrics' AND indexname = 'agent_run_metrics_workflow')
		`).Scan(&exists)).To(Succeed())
		return exists
	}

	It("drops the tag columns and their index, keeping the telemetry rows", func() {
		// agent_run_metrics.build_id has no FK, so a durable row needs no build.
		_, err := database.Exec(`
			INSERT INTO agent_run_metrics
				(build_id, plan_id, step_name, status, workflow_name, workflow_version, workflow_hash)
			VALUES (918273, 'p', 'implement', 'ok', 'code-review', 4, 'deadbeef')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(metricColumns()).NotTo(ContainElements("workflow_name", "workflow_version", "workflow_hash"))
		Expect(hasWorkflowIndex()).To(BeFalse())

		var count int
		Expect(database.QueryRow(
			`SELECT count(*) FROM agent_run_metrics WHERE build_id = 918273 AND plan_id = 'p'`,
		).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))

		By("restoring the column shape and index on the way down")
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(metricColumns()).To(ContainElements("workflow_name", "workflow_version", "workflow_hash"))
		Expect(hasWorkflowIndex()).To(BeTrue())
	})
})
