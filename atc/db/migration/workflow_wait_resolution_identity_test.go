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

var _ = Describe("workflow wait resolution identity migration", func() {
	const beforeVersion, targetVersion = 1773106112, 1773106113
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

	It("backfills display identity and constrains durable answer intents", func() {
		var teamID, definitionID, buildID int
		var runID, questionID, answerID, waitID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('workflow-wait-identity') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('workflow-wait-identity', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'workflow-wait-identity', $2, 'workflow-wait-identity', 1,
			        3, 1, $3, 'workflow-wait-identity-run', '{}', $4,
			        'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("a", 64), strings.Repeat("b", 64)).Scan(&runID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id)
			VALUES ('workflow-wait-identity-build', 'started', $1) RETURNING id
		`, teamID).Scan(&buildID)).To(Succeed())
		for _, target := range []struct {
			typeName string
			digest   string
			id       *int64
		}{
			{typeName: "question", digest: "sha256:" + strings.Repeat("c", 64), id: &questionID},
			{typeName: "human-answer", digest: "sha256:" + strings.Repeat("d", 64), id: &answerID},
		} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, 1, $2, 1, 1, 'application/x-tar') RETURNING id
			`, target.typeName, target.digest).Scan(target.id)).To(Succeed())
		}
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_waits
				(team_id, workflow_run_id, build_id, build_id_evidence, plan_id, attempt, output_name,
				 question_name, question_snapshot_id, expected_type_name, expected_type_version,
				 deadline, timeout_policy, status, answer_snapshot_id, resolved_by,
				 resolution_source, resolved_at)
			VALUES ($1, $2, $3, $3, 'plan', '0', 'answer', 'question', $4,
			        'human-answer', 1, now() + interval '1 hour', 'fail', 'resolved',
			        $5, 'subject:sha256:stable', 'human', now())
			RETURNING id
		`, teamID, runID, buildID, questionID, answerID).Scan(&waitID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var displayName string
		Expect(database.QueryRow(`
			SELECT resolved_by_display_name FROM agent_workflow_waits WHERE id = $1
		`, waitID).Scan(&displayName)).To(Succeed())
		Expect(displayName).To(Equal("subject:sha256:stable"))

		_, err := database.Exec(`
			UPDATE agent_workflow_waits
			SET resolution_intent_answer = 'approve',
			    resolution_intent_actor = 'subject:sha256:stable'
			WHERE id = $1
		`, waitID)
		Expect(err).To(HaveOccurred(), "partial intents must fail closed")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_workflow_waits`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})
})
