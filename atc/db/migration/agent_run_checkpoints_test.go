package migration_test

import (
	"database/sql"
	"fmt"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent run checkpoints migration", func() {
	const beforeVersion, targetVersion = 1773106143, 1773106144

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

	It("preserves durable checkpoint provenance while enforcing normalized object and journal invariants", func() {
		var teamID, definitionID int
		var workflowRunID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('checkpoint-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('checkpoint-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'checkpoint-migration', $2, 'checkpoint-migration', 1,
				3, 1, $3, 'checkpoint-migration-run', '{}', $4, 'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("b", 64), strings.Repeat("c", 64)).Scan(&workflowRunID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var ciHeadID, workflowHeadID, objectID, firstCheckpointID, secondCheckpointID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads (build_id, plan_id, function_id)
			VALUES (12345, 'ci-plan', 'implement') RETURNING id
		`).Scan(&ciHeadID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads
				(workflow_run_provenance_id, workflow_run_id, build_id, plan_id, function_id)
			VALUES ($1, $1, 12346, 'workflow-plan', 'review') RETURNING id
		`, workflowRunID).Scan(&workflowHeadID)).To(Succeed())
		_, err := database.Exec(`
			UPDATE agent_run_checkpoint_heads SET workflow_run_provenance_id = NULL WHERE id = $1
		`, workflowHeadID)
		Expect(err).To(HaveOccurred(), "workflow-run provenance must remain immutable")
		Expect(database.QueryRow(`
			INSERT INTO agent_checkpoint_objects
				(kind, digest, object_key, generation, status)
			VALUES ('checkpoints', $1, $2, 7, 'available') RETURNING id
		`, "sha256:"+strings.Repeat("d", 64), "hangar/v1/checkpoints/sha256/"+strings.Repeat("d", 64)+".tar.zst").Scan(&objectID)).To(Succeed())

		By("requiring durable leases for exclusive object deletion and reconciliation claims")
		deleteLeaseTx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = deleteLeaseTx.Exec(`
			UPDATE agent_checkpoint_objects
			SET status = 'deleting', delete_token = '22222222-2222-2222-2222-222222222222'
			WHERE id = $1
		`, objectID)
		Expect(err).To(HaveOccurred(), "a deleting token without a lease would become crash-stuck")
		Expect(deleteLeaseTx.Rollback()).To(Succeed())

		reconciliationLeaseTx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciliationLeaseTx.Exec(`
			UPDATE agent_checkpoint_objects
			SET reconciliation_token = '33333333-3333-3333-3333-333333333333'
			WHERE id = $1
		`, objectID)
		Expect(err).To(HaveOccurred(), "a reconciliation token without an uploading lease is invalid")
		Expect(reconciliationLeaseTx.Rollback()).To(Succeed())

		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`
			INSERT INTO agent_run_checkpoints
				(head_id, archive_object_id, generation, expected_previous_generation, execution_attempt, fence_token, status, manifest, stage_expires_at, committed_at)
			VALUES ($1, $2, 1, 0, 1, '11111111-1111-1111-1111-111111111111', 'committed', '{}', now() + interval '1 hour', now()) RETURNING id
		`, ciHeadID, objectID).Scan(&firstCheckpointID)).To(Succeed())
		_, err = tx.Exec(`UPDATE agent_run_checkpoint_heads SET latest_checkpoint_id = $2 WHERE id = $1`, ciHeadID, firstCheckpointID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed(), "the head/checkpoint FK must be deferred for atomic commit")

		_, err = database.Exec(`
			INSERT INTO agent_run_checkpoints
				(head_id, archive_object_id, generation, expected_previous_generation, execution_attempt, fence_token, status, manifest, stage_expires_at, committed_at)
			VALUES ($1, $2, 2, 1, 2, '22222222-2222-2222-2222-222222222222', 'committed', '{}', now() + interval '1 hour', now())
		`, ciHeadID, objectID)
		Expect(err).To(HaveOccurred(), "only one checkpoint may be committed for a head")

		_, err = database.Exec(`UPDATE agent_run_checkpoints SET status = 'superseded', superseded_at = now() WHERE id = $1`, firstCheckpointID)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoints
				(head_id, archive_object_id, generation, expected_previous_generation, execution_attempt, fence_token, status, manifest, stage_expires_at, committed_at)
			VALUES ($1, $2, 2, 1, 2, '22222222-2222-2222-2222-222222222222', 'committed', '{}', now() + interval '1 hour', now()) RETURNING id
		`, ciHeadID, objectID).Scan(&secondCheckpointID)).To(Succeed())
		Expect(secondCheckpointID).To(BeNumerically(">", firstCheckpointID))

		_, err = database.Exec(`
			INSERT INTO agent_run_checkpoints
				(head_id, generation, expected_previous_generation, execution_attempt, fence_token, status, manifest, stage_expires_at)
			VALUES ($1, 1, 0, 3, '33333333-3333-3333-3333-333333333333', 'staged', '{}', now() + interval '1 hour')
		`, ciHeadID)
		Expect(err).To(HaveOccurred(), "generation reservations are never reusable")

		_, err = database.Exec(`
			INSERT INTO agent_run_effects
				(head_id, execution_attempt, tool_call_id, tool_name, provider, adapter_version, fence_token, read_only, state)
			VALUES ($1, 1, 'write-1', 'write_file', 'claude', 'v1', '11111111-1111-1111-1111-111111111111', false, 'begun')
		`, ciHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_run_effects SET state = 'committed', committed_at = now() WHERE head_id = $1 AND tool_call_id = 'write-1'`, ciHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_run_effects SET state = 'begun', committed_at = NULL WHERE head_id = $1 AND tool_call_id = 'write-1'`, ciHeadID)
		Expect(err).To(HaveOccurred(), "effects may only advance begun -> committed")

		_, err = database.Exec(`INSERT INTO agent_run_events (head_id, execution_attempt, event_type) VALUES ($1, 1, 'checkpoint_committed')`, ciHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_run_events SET reason = 'rewritten' WHERE head_id = $1`, ciHeadID)
		Expect(err).To(HaveOccurred(), "recovery events are append-only")

		By("rejecting caller-forged cleanup authorization during the retention window")
		for _, table := range []string{"agent_run_events", "agent_run_effects"} {
			cleanupTx, err := database.Begin()
			Expect(err).NotTo(HaveOccurred())
			_, err = cleanupTx.Exec(`SET LOCAL concourse.agent_checkpoint_cleanup = 'on'`)
			Expect(err).NotTo(HaveOccurred())
			_, err = cleanupTx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE head_id = $1`, table), ciHeadID)
			Expect(err).To(HaveOccurred(), "a caller-settable GUC must not bypass %s append-only retention", table)
			Expect(cleanupTx.Rollback()).To(Succeed())
		}

		By("allowing event and effect deletion only after database-proven terminal retention")
		var cleanupHeadID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads
				(build_id, plan_id, function_id, active, terminal_at)
			VALUES (12347, 'cleanup-plan', 'implement', FALSE, now() - INTERVAL '31 days')
			RETURNING id
		`).Scan(&cleanupHeadID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_run_effects
				(head_id, execution_attempt, tool_call_id, tool_name, provider, adapter_version, fence_token, read_only, state)
			VALUES ($1, 1, 'cleanup-read', 'read_file', 'claude', 'v1', '11111111-1111-1111-1111-111111111111', TRUE, 'begun')
		`, cleanupHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_run_events (head_id, execution_attempt, event_type)
			VALUES ($1, 1, 'session_completed')
		`, cleanupHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_run_events WHERE head_id = $1`, cleanupHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_run_effects WHERE head_id = $1`, cleanupHeadID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_run_checkpoint_heads WHERE id = $1`, cleanupHeadID)
		Expect(err).NotTo(HaveOccurred())

		By("retaining checkpoint/effect/event rows after workflow and build provenance are removed")
		_, err = database.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, workflowRunID)
		Expect(err).NotTo(HaveOccurred())
		var immutableWorkflowProvenance, liveWorkflowRunID sql.NullInt64
		Expect(database.QueryRow(`
			SELECT workflow_run_provenance_id, workflow_run_id
			FROM agent_run_checkpoint_heads WHERE id = $1
		`, workflowHeadID).Scan(&immutableWorkflowProvenance, &liveWorkflowRunID)).To(Succeed())
		Expect(immutableWorkflowProvenance.Valid).To(BeTrue())
		Expect(immutableWorkflowProvenance.Int64).To(Equal(workflowRunID))
		Expect(liveWorkflowRunID.Valid).To(BeFalse())
		var retained int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_run_checkpoints WHERE head_id = $1`, ciHeadID).Scan(&retained)).To(Succeed())
		Expect(retained).To(Equal(2))

		By("leaving an uploading ticket discoverable after a coordinator crash")
		_, err = database.Exec(`
			INSERT INTO agent_checkpoint_objects (kind, digest, object_key, status, upload_token, upload_expires_at)
			VALUES ('checkpoints', $1, $2, 'uploading', '11111111-1111-1111-1111-111111111111', now() - interval '1 hour')
		`, "sha256:"+strings.Repeat("e", 64), "hangar/v1/checkpoints/sha256/"+strings.Repeat("e", 64)+".tar.zst")
		Expect(err).NotTo(HaveOccurred())
		var uploading int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_checkpoint_objects WHERE status = 'uploading' AND upload_expires_at < now()`).Scan(&uploading)).To(Succeed())
		Expect(uploading).To(Equal(1))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var tables int
		Expect(database.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'agent_run_checkpoints'`).Scan(&tables)).To(Succeed())
		Expect(tables).To(Equal(0))
	})
})
