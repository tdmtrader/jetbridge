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

	// seedOccurrenceWithWait builds the minimum real graph the wait_id foreign
	// key needs: a team, a workflow definition, a run, a question snapshot, an
	// open wait, and one frozen occurrence pointing at it.
	seedOccurrenceWithWait := func() (runID int64, waitID int64) {
		var teamID int
		Expect(database.QueryRow(
			`INSERT INTO teams (name) VALUES ('occurrence-wait') RETURNING id`).Scan(&teamID)).To(Succeed())
		var definitionID int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('waiting-workflow', 1, $1, 'plan: []', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version, definition_content_hash,
				 idempotency_key, parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			VALUES ('workflow', $1, 'occurrence-wait', $2, 'waiting-workflow', 1, 3, 1, $3,
			        'idem-wait', '{}', $3, 'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("a", 64)).Scan(&runID)).To(Succeed())
		var snapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'human-question', 1, $2, 1, 1, 'application/x-tar')
			RETURNING id
		`, teamID, "sha256:"+strings.Repeat("b", 64)).Scan(&snapshotID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id_evidence, plan_id, attempt, output_name,
				 question_name, question_snapshot_id, expected_type_name, expected_type_version,
				 deadline, timeout_policy, status)
			VALUES ($1, $2, 9001, '1/3', '1', 'approval', 'approve?', $3,
			        'human-answer', 1, now() + interval '1 hour', 'fail', 'waiting')
			RETURNING id
		`, teamID, runID, snapshotID).Scan(&waitID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_node_occurrences
				(workflow_run_id, node_id, retry_attempt, attempt, team_id, workflow_name,
				 workflow_definition_id, workflow_version, node_kind, plan_id, status, wait_id)
			VALUES ($1, 'approval', 1, 1, $2, 'waiting-workflow', $3, 1, 'await', '1/3', 'waiting', $4)
		`, runID, teamID, definitionID, waitID)
		Expect(err).NotTo(HaveOccurred())
		return runID, waitID
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

	// PostgreSQL implements ON DELETE SET NULL as an internal UPDATE, and a
	// BEFORE ROW UPDATE trigger fires for it. An unconditional immutability
	// trigger therefore made the declared referential action unexecutable:
	// deleting any referenced wait aborted with "immutable once frozen", so a
	// wait retention pass, an operator cleanup, or a fixture teardown would
	// have hit a hard failure the first time it ran.
	It("lets a deleted wait clear the live reference without touching the evidence", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		runID, waitID := seedOccurrenceWithWait()

		_, err := database.Exec(`DELETE FROM agent_workflow_waits WHERE id = $1`, waitID)
		Expect(err).NotTo(HaveOccurred())

		var survivingWait sql.NullInt64
		var status, nodeID string
		Expect(database.QueryRow(`
			SELECT wait_id, status, node_id
			FROM agent_workflow_run_node_occurrences WHERE workflow_run_id = $1
		`, runID).Scan(&survivingWait, &status, &nodeID)).To(Succeed())
		Expect(survivingWait.Valid).To(BeFalse())
		Expect(status).To(Equal("waiting"), "the durable evidence is untouched")
		Expect(nodeID).To(Equal("approval"))
	})

	It("still refuses every other correction of frozen history", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		runID, waitID := seedOccurrenceWithWait()

		for _, update := range []string{
			`UPDATE agent_workflow_run_node_occurrences SET status = 'failed' WHERE workflow_run_id = $1`,
			`UPDATE agent_workflow_run_node_occurrences SET cost_usd = 9 WHERE workflow_run_id = $1`,
			// Clearing wait_id is permitted only as the referential action's
			// own no-op: it may not smuggle another edit alongside it.
			`UPDATE agent_workflow_run_node_occurrences
			 SET wait_id = NULL, status = 'failed' WHERE workflow_run_id = $1`,
		} {
			_, err := database.Exec(update, runID)
			Expect(err).To(HaveOccurred(), update)
			Expect(err.Error()).To(ContainSubstring("immutable once frozen"))
		}

		// And re-pointing it at another wait is not a clearing at all.
		_, err := database.Exec(`
			UPDATE agent_workflow_run_node_occurrences SET wait_id = $2 WHERE workflow_run_id = $1
		`, runID, waitID+1)
		Expect(err).To(HaveOccurred())
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
