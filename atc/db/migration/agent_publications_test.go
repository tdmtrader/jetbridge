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

var _ = Describe("agent publications migration", func() {
	const beforeVersion, targetVersion = 1773106109, 1773106110
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

	It("round-trips durable publication audit without deleting its snapshot", func() {
		var teamID, definitionID, buildID int
		var workflowRunID, snapshotID, questionID, answerID, waitID int64
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ('publication-migration') RETURNING id
		`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('publication-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("c", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ('publication-migration', 'started', $1, 'alice') RETURNING id
		`, teamID).Scan(&buildID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, 'publication-migration', $2, 'publication-migration', 1,
			        3, 1, $3, 'publication-migration', '{}', $4,
			        'manual', '', 'alice', 'running', $5)
			RETURNING id
		`, teamID, definitionID, strings.Repeat("c", 64), strings.Repeat("d", 64), buildID).Scan(&workflowRunID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar')
			RETURNING id
		`, "sha256:"+strings.Repeat("a", 64)).Scan(&snapshotID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('question', 1, $1, 1, 1, 'application/x-tar')
			RETURNING id
		`, "sha256:"+strings.Repeat("1", 64)).Scan(&questionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('human-answer', 1, $1, 1, 1, 'application/x-tar')
			RETURNING id
		`, "sha256:"+strings.Repeat("2", 64)).Scan(&answerID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence,
				 plan_id, attempt, output_name, question_name, question_snapshot_id,
				 expected_type_name, expected_type_version, deadline, timeout_policy,
				 status, answer_snapshot_id, resolved_by, resolution_source, resolved_at)
			VALUES ($1, $2, $3, $3, 'merge-approval', '1', 'approval', 'approve merge', $4,
			        'human-answer', 1, '2026-07-23T12:00:00Z', 'fail',
			        'resolved', $5, 'alice', 'human', '2026-07-22T12:00:00Z')
			RETURNING id
		`, teamID, workflowRunID, buildID, questionID, answerID).Scan(&waitID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, status, attempt, lease_until)
			VALUES ($1, $2, 'publication-migration', $3, $4, 'alice',
			        $5, 'git-publisher/v1', 'github.example/team/repo', 'pull-request',
			        '{"source_branch":"agent/change","target_branch":"main"}',
			        'engineering/v2', 'pending', 1, now() + interval '1 minute')
		`, "sha256:"+strings.Repeat("b", 64), teamID, workflowRunID, buildID, snapshotID)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, approved_by, approval_wait_id,
				 approval_question_snapshot_id, approval_answer_snapshot_id, approval_resolved_at,
				 status, attempt, lease_until)
			VALUES ($1, $2, 'publication-migration', $3, $4, 'alice',
			        $5, 'git-publisher/v1', 'github.example/team/repo', 'merge',
			        '{"target_branch":"main","expected_base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}',
			        'engineering/v2', 'alice', $6, $7, $8, '2026-07-22T12:00:00Z',
			        'pending', 1, now() + interval '1 minute')
		`, "sha256:"+strings.Repeat("d", 64), teamID, workflowRunID, buildID, snapshotID,
			waitID, questionID, answerID)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`DELETE FROM agent_workflow_waits WHERE id = $1`, waitID)
		Expect(err).To(HaveOccurred(), "publication audit must retain its exact durable wait")

		var otherTeamID int
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ('other-publication-migration') RETURNING id
		`).Scan(&otherTeamID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, status, attempt, lease_until)
			VALUES ($1, $2, 'other-publication-migration', $3, $4, 'alice',
			        $5, 'git-publisher/v1', 'github.example/team/repo', 'pull-request',
			        '{"source_branch":"agent/change","target_branch":"main"}',
			        'engineering/v2', 'pending', 1, now() + interval '1 minute')
		`, "sha256:"+strings.Repeat("f", 64), otherTeamID, workflowRunID, buildID, snapshotID)
		Expect(err).To(HaveOccurred(), "publication audit cannot attribute a run to another team")

		_, err = database.Exec(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, approved_by, status, attempt, lease_until)
			VALUES ($1, $2, 'publication-migration', $3, $4, 'alice',
			        $5, 'git-publisher/v1', 'github.example/team/repo', 'branch', '{}',
			        'engineering/v2', 'forged-human', 'pending', 1, now() + interval '1 minute')
		`, "sha256:"+strings.Repeat("e", 64), teamID, workflowRunID, buildID, snapshotID)
		Expect(err).To(HaveOccurred(), "non-merge operations cannot carry an approval actor")

		_, err = database.Exec(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, approved_by, status, attempt, lease_until)
			VALUES ($1, $2, 'publication-migration', $3, $4, 'alice',
			        $5, 'git-publisher/v1', 'github.example/team/repo', 'merge',
			        '{"target_branch":"main","expected_base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}',
			        'engineering/v2', 'alice', 'pending', 1, now() + interval '1 minute')
		`, "sha256:"+strings.Repeat("c", 64), teamID, workflowRunID, buildID, snapshotID)
		Expect(err).To(HaveOccurred(), "merge operations require complete durable approval evidence")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE id = $1`, snapshotID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(database.QueryRow(`SELECT count(*) FROM agent_publications`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})
})
