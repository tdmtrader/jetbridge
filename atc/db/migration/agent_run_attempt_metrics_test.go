package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent run attempt metrics migration", func() {
	const beforeVersion, targetVersion = 1773106145, 1773106146

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

	It("preserves legacy aggregates and binds new metrics to their exact durable attempts", func() {
		_, err := database.Exec(`INSERT INTO agent_run_metrics
			(build_id, plan_id, step_name, status, summary, cost_usd)
			VALUES (971, 'legacy', 'implement', 'ok', 'legacy summary', 0.42)`)
		Expect(err).NotTo(HaveOccurred())
		var headID, attemptID int64
		Expect(database.QueryRow(`INSERT INTO agent_run_checkpoint_heads
			(build_id, plan_id, function_id) VALUES (972, 'recover', 'implement') RETURNING id`).Scan(&headID)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO agent_run_attempts
			(head_id, attempt_number, state, is_current, materialization_id)
			VALUES ($1, 1, 'scheduling', TRUE, 'migration-metric-attempt') RETURNING id`, headID).Scan(&attemptID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var summary string
		var legacyCost float64
		Expect(database.QueryRow(`SELECT summary, cost_usd::float8 FROM agent_run_metrics
			WHERE build_id = 971 AND plan_id = 'legacy'`).Scan(&summary, &legacyCost)).To(Succeed())
		Expect(summary).To(Equal("legacy summary"))
		Expect(legacyCost).To(BeNumerically("~", 0.42, 1e-9))

		_, err = database.Exec(`INSERT INTO agent_run_attempt_metrics
			(attempt_id, build_id, plan_id, execution_attempt, function_id, step_name,
			 source, provider, model, status)
			VALUES ($1, 972, 'recover', 1, 'implement', 'implement',
			 'agent_step', 'anthropic', 'claude-sonnet-4-5', 'error')`, attemptID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_run_attempt_metrics SET model = 'other'
			WHERE attempt_id = $1`, attemptID)
		Expect(err).To(HaveOccurred(), "attempt attribution is immutable")
		_, err = database.Exec(`INSERT INTO agent_run_attempt_metrics
			(attempt_id, build_id, plan_id, execution_attempt, function_id, step_name,
			 source, provider, model, status)
			VALUES ($1, 999, 'recover', 1, 'implement', 'implement',
			 'agent_step', 'anthropic', 'claude-sonnet-4-5', 'error')`, attemptID)
		Expect(err).To(HaveOccurred(), "the copied identity must match the durable attempt")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'agent_run_attempt_metrics'
		)`).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
		Expect(database.QueryRow(`SELECT summary FROM agent_run_metrics
			WHERE build_id = 971 AND plan_id = 'legacy'`).Scan(&summary)).To(Succeed())
	})
})
