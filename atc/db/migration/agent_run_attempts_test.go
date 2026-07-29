package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent run attempts migration", func() {
	const beforeVersion, targetVersion = 1773106144, 1773106145

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

	It("adds bounded current-attempt and durable fence authority without rewriting existing heads", func() {
		var headID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads (build_id, plan_id, function_id)
			VALUES (9001, 'attempt-plan', 'implement')
			RETURNING id
		`).Scan(&headID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var retainedHeadID int64
		Expect(database.QueryRow(`
			SELECT id FROM agent_run_checkpoint_heads
			WHERE build_id = 9001 AND plan_id = 'attempt-plan' AND function_id = 'implement'
		`).Scan(&retainedHeadID)).To(Succeed())
		Expect(retainedHeadID).To(Equal(headID))

		var firstID int64
		var maxTotal int
		Expect(database.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id)
			VALUES ($1, 1, 'scheduling', TRUE, 'materialization-1')
			RETURNING id, max_total_attempts
		`, headID).Scan(&firstID, &maxTotal)).To(Succeed())
		Expect(maxTotal).To(Equal(3))

		var boundedHeadID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads (build_id, plan_id, function_id)
			VALUES (9002, 'bounded-attempt-plan', 'implement')
			RETURNING id
		`).Scan(&boundedHeadID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current,
				 materialization_id, interruption_reason, interrupted_at)
			VALUES ($1, 1, 3, 'interrupted', FALSE,
				'bounded-materialization-1', 'preempted', clock_timestamp())
		`, boundedHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current,
				 materialization_id, source_attempt_number, source_checkpoint_generation,
				 recovery_mode, source_interruption_reason)
			VALUES ($1, 2, 4, 'scheduling', TRUE,
				'bounded-materialization-2', 1, 0, 'checkpoint_zero', 'preempted')
		`, boundedHeadID)
		Expect(err).To(HaveOccurred(),
			"a recovery row cannot enlarge the identity's durable attempt maximum")

		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_fence_tokens (attempt_id, token)
			VALUES ($1, '11111111-1111-1111-1111-111111111111')
		`, firstID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_run_attempt_fence_tokens (attempt_id, token)
			VALUES ($1, '11111111-1111-1111-1111-111111111111')
		`, firstID)
		Expect(err).To(HaveOccurred(), "a retired fence token can never be issued again")

		_, err = database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id)
			VALUES ($1, 2, 'scheduling', TRUE, 'materialization-2')
		`, headID)
		Expect(err).To(HaveOccurred(), "an identity cannot have two current attempts")

		_, err = database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current, materialization_id)
			VALUES ($1, 4, 3, 'scheduling', FALSE, 'materialization-4')
		`, headID)
		Expect(err).To(HaveOccurred(), "attempt_number must remain inside the durable maximum")

		_, err = database.Exec(`
			UPDATE agent_run_attempts
			SET fence_token = '11111111-1111-1111-1111-111111111111',
				fence_expires_at = clock_timestamp() + INTERVAL '1 minute'
			WHERE id = $1
		`, firstID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_run_attempts
			SET state = 'manual_review_required', terminal_at = clock_timestamp()
			WHERE id = $1
		`, firstID)
		Expect(err).To(HaveOccurred(), "a terminal attempt cannot retain mutation authority")
		_, err = database.Exec(`
			UPDATE agent_run_attempts
			SET state = 'interrupted', interruption_reason = 'preempted',
				interrupted_at = clock_timestamp(), is_current = FALSE,
				fence_expires_at = NULL
			WHERE id = $1
		`, firstID)
		Expect(err).NotTo(HaveOccurred())

		var sourceCheckpointID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoints
				(head_id, generation, expected_previous_generation, execution_attempt, fence_token, status, manifest, stage_expires_at, committed_at)
			VALUES ($1, 7, 0, 1, '22222222-2222-2222-2222-222222222222', 'committed', '{}', now() + INTERVAL '1 hour', now())
			RETURNING id
		`, headID).Scan(&sourceCheckpointID)).To(Succeed())

		var secondID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current,
				 materialization_id, source_attempt_number, source_checkpoint_id,
				 source_checkpoint_generation, recovery_mode, source_interruption_reason)
			VALUES ($1, 2, 3, 'scheduling', TRUE,
				'materialization-2', 1, $2, 7, 'workspace_only', 'preempted')
			RETURNING id
		`, headID, sourceCheckpointID).Scan(&secondID)).To(Succeed())
		Expect(secondID).To(BeNumerically(">", firstID))

		_, err = database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current,
				 materialization_id, source_attempt_number, source_checkpoint_id,
				 source_checkpoint_generation, recovery_mode, source_interruption_reason)
			VALUES ($1, 3, 3, 'scheduling', FALSE,
				'materialization-duplicate', 1, $2, 7, 'workspace_only', 'preempted')
		`, headID, sourceCheckpointID)
		Expect(err).To(HaveOccurred(), "one interrupted source cannot allocate two replacements")

		_, err = database.Exec(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, max_total_attempts, state, is_current,
				 materialization_id, source_attempt_number, source_checkpoint_generation,
				 recovery_mode, source_interruption_reason)
			VALUES ($1, 3, 3, 'scheduling', FALSE,
				'materialization-invalid', 2, 0, 'workspace_only', 'preempted')
		`, headID)
		Expect(err).To(HaveOccurred(), "nonzero recovery requires checkpoint provenance")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var attemptTables int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'agent_run_attempts'
		`).Scan(&attemptTables)).To(Succeed())
		Expect(attemptTables).To(Equal(0))
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'agent_run_attempt_fence_tokens'
		`).Scan(&attemptTables)).To(Succeed())
		Expect(attemptTables).To(Equal(0))
		Expect(database.QueryRow(`
			SELECT id FROM agent_run_checkpoint_heads
			WHERE build_id = 9001 AND plan_id = 'attempt-plan' AND function_id = 'implement'
		`).Scan(&retainedHeadID)).To(Succeed())
	})
})
