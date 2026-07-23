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

var _ = Describe("workflow waits migration", func() {
	const beforeVersion, targetVersion = 1773106108, 1773106109
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

	It("round-trips waits without deleting their durable inputs", func() {
		var teamID, definitionID, buildID int
		var runID, questionID, defaultID, answerID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('workflow-wait-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('workflow-wait-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'workflow-wait-migration', $2, 'workflow-wait-migration', 1,
			        3, 1, $3, 'workflow-wait-run', '{}', $4, 'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("a", 64), strings.Repeat("b", 64)).Scan(&runID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id)
			VALUES ('workflow-wait-build', 'started', $1) RETURNING id
		`, teamID).Scan(&buildID)).To(Succeed())
		for _, target := range []struct {
			digest string
			id     *int64
		}{
			{digest: "sha256:" + strings.Repeat("c", 64), id: &questionID},
			{digest: "sha256:" + strings.Repeat("d", 64), id: &defaultID},
			{digest: "sha256:" + strings.Repeat("e", 64), id: &answerID},
		} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ('human-answer', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
			`, target.digest).Scan(target.id)).To(Succeed())
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt, output_name,
				 question_name, question_snapshot_id, expected_type_name,
				 expected_type_version, deadline, timeout_policy)
			VALUES ($1, $2, $3, $3, 'plan-1', '0', 'answer',
			        'question', $4, 'human-answer', 1, now() + interval '1 hour', 'fail')
		`, teamID, runID, buildID, questionID)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt, output_name,
				 question_name, question_snapshot_id, expected_type_name,
				 expected_type_version, deadline, timeout_policy, default_snapshot_id)
			VALUES ($1, $2, $3, $3, 'plan-2', '0', 'answer',
			        'question', $4, 'human-answer', 1, now() + interval '1 hour', 'fail', $5)
		`, teamID, runID, buildID, questionID, defaultID)
		Expect(err).To(HaveOccurred(), "fail waits cannot carry a default")

		_, err = database.Exec(`
			UPDATE agent_workflow_waits
			SET status = 'resolved', answer_snapshot_id = $1,
			    resolved_by = 'alice', resolution_source = 'human', resolved_at = now()
			WHERE build_id = $2 AND plan_id = 'plan-1'
		`, answerID, buildID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for table, id := range map[string]int64{
			"agent_workflow_runs": runID,
			"agent_snapshots":     questionID,
			"builds":              int64(buildID),
		} {
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1), table)
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_workflow_waits`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})

	It("releases the live build reference while retaining copied wait evidence", func() {
		var teamID, definitionID, buildID int
		var runID, questionID, answerID, waitID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('workflow-wait-gc') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('workflow-wait-gc', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("f", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'workflow-wait-gc', $2, 'workflow-wait-gc', 1,
			        3, 1, $3, 'workflow-wait-gc-run', '{}', $4, 'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("f", 64), strings.Repeat("0", 64)).Scan(&runID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id)
			VALUES ('workflow-wait-gc-build', 'succeeded', $1) RETURNING id
		`, teamID).Scan(&buildID)).To(Succeed())
		for _, target := range []struct {
			typeName string
			digest   string
			id       *int64
		}{
			{typeName: "question", digest: "sha256:" + strings.Repeat("1", 64), id: &questionID},
			{typeName: "human-answer", digest: "sha256:" + strings.Repeat("2", 64), id: &answerID},
		} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, 1, $2, 1, 1, 'filesystem-tree-v1') RETURNING id
			`, target.typeName, target.digest).Scan(target.id)).To(Succeed())
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt, output_name,
				 question_name, question_snapshot_id, expected_type_name, expected_type_version,
				 deadline, timeout_policy, status, answer_snapshot_id, resolved_by,
				 resolution_source, resolved_at)
			VALUES ($1, $2, $3, $3, 'plan-gc', '0', 'answer', 'question', $4,
			        'human-answer', 1, now() + interval '1 hour', 'fail', 'resolved', $5,
			        'alice', 'human', now())
			RETURNING id
		`, teamID, runID, buildID, questionID, answerID).Scan(&waitID)).To(Succeed())

		_, err := database.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())

		var liveBuild sql.NullInt64
		var evidence, storedRunID, storedAnswerID int64
		var status string
		Expect(database.QueryRow(`
			SELECT build_id, build_id_evidence, workflow_run_id, status, answer_snapshot_id
			FROM agent_workflow_waits WHERE id = $1
		`, waitID).Scan(&liveBuild, &evidence, &storedRunID, &status, &storedAnswerID)).To(Succeed())
		Expect(liveBuild.Valid).To(BeFalse())
		Expect(evidence).To(Equal(int64(buildID)))
		Expect(storedRunID).To(Equal(runID))
		Expect(status).To(Equal("resolved"))
		Expect(storedAnswerID).To(Equal(answerID))

		var runCount int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_workflow_runs WHERE id = $1`, runID).Scan(&runCount)).To(Succeed())
		Expect(runCount).To(Equal(1))

		var foreignKey string
		Expect(database.QueryRow(`
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_waits'::regclass
			  AND conname = 'agent_workflow_waits_build_id_fkey'
		`).Scan(&foreignKey)).To(Succeed())
		Expect(foreignKey).To(ContainSubstring("ON DELETE SET NULL"))
	})
})
