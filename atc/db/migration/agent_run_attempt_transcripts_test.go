package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent run attempt transcripts migration", func() {
	const beforeVersion, targetVersion = 1773106146, 1773106147

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

	It("binds each transcript to its exact durable attempt while preserving legacy projections", func() {
		var headID, firstAttemptID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads (build_id, plan_id, function_id)
			VALUES (9101, 'transcript-plan', 'implement')
			RETURNING id
		`).Scan(&headID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id)
			VALUES ($1, 1, 'scheduling', TRUE, 'materialization-1')
			RETURNING id
		`, headID).Scan(&firstAttemptID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_run_transcripts
				(build_id, plan_id, function_id, ndjson, byte_len)
			VALUES (9101, 'legacy-plan', 'implement', 'legacy', 6)
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_transcripts
				(attempt_id, build_id, plan_id, execution_attempt, function_id, ndjson, byte_len)
			VALUES ($1, 9101, 'transcript-plan', 1, 'implement', 'interrupted', 11)
		`, firstAttemptID)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_transcripts
				(attempt_id, build_id, plan_id, execution_attempt, function_id, ndjson, byte_len)
			VALUES ($1, 9101, 'transcript-plan', 2, 'implement', 'wrong-attempt', 13)
		`, firstAttemptID)
		Expect(err).To(HaveOccurred(), "the denormalized attempt number must match the durable attempt")

		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_transcripts
				(attempt_id, build_id, plan_id, execution_attempt, function_id, ndjson, byte_len)
			VALUES ($1, 9102, 'transcript-plan', 1, 'implement', 'wrong-build', 11)
		`, firstAttemptID)
		Expect(err).To(HaveOccurred(), "the denormalized build identity must match the durable head")

		_, err = database.Exec(`
			UPDATE agent_run_attempts
			SET state = 'interrupted', is_current = FALSE,
				interruption_reason = 'preempted', interrupted_at = clock_timestamp()
			WHERE id = $1
		`, firstAttemptID)
		Expect(err).NotTo(HaveOccurred())
		var secondAttemptID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id,
				 source_attempt_number, source_checkpoint_generation, recovery_mode,
				 source_interruption_reason)
			VALUES ($1, 2, 'scheduling', TRUE, 'materialization-2',
				1, 0, 'checkpoint_zero', 'preempted')
			RETURNING id
		`, headID).Scan(&secondAttemptID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_transcripts
				(attempt_id, build_id, plan_id, execution_attempt, function_id, ndjson, byte_len, display_finalized)
			VALUES ($1, 9101, 'transcript-plan', 2, 'implement', 'final', 5, TRUE)
		`, secondAttemptID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_run_attempt_transcripts
			SET display_finalized = TRUE
			WHERE attempt_id = $1
		`, firstAttemptID)
		Expect(err).To(HaveOccurred(), "only one attempt can become the final presentation")

		var legacy string
		Expect(database.QueryRow(`
			SELECT ndjson FROM agent_run_transcripts
			WHERE build_id = 9101 AND plan_id = 'legacy-plan'
		`).Scan(&legacy)).To(Succeed())
		Expect(legacy).To(Equal("legacy"))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var transcriptTables int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'agent_run_attempt_transcripts'
		`).Scan(&transcriptTables)).To(Succeed())
		Expect(transcriptTables).To(BeZero())
		Expect(database.QueryRow(`
			SELECT ndjson FROM agent_run_transcripts
			WHERE build_id = 9101 AND plan_id = 'legacy-plan'
		`).Scan(&legacy)).To(Succeed())
		Expect(legacy).To(Equal("legacy"))
	})
})
