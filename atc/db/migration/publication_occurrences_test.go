package migration_test

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("publication occurrences migration", func() {
	const beforeVersion, targetVersion = 1773106117, 1773106118
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

	It("backfills exact occurrences and fails closed for cross-run aliases on downgrade", func() {
		unique := time.Now().UnixNano()
		var teamID, definitionID, firstBuildID int
		var firstRunID, snapshotID, publicationID int64
		teamName := fmt.Sprintf("publication-occurrence-%d", unique)
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, teamName).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, teamName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'alice') RETURNING id
		`, teamName+"-first", teamID).Scan(&firstBuildID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'alice', 'running', $8)
			RETURNING id
		`, teamID, teamName, definitionID, teamName, strings.Repeat("a", 64),
			teamName+"-first", strings.Repeat("b", 64), firstBuildID).Scan(&firstRunID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, "sha256:"+strings.Repeat("c", 64)).Scan(&snapshotID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'output', 'change', $2, now())
		`, firstRunID, snapshotID)
		Expect(err).NotTo(HaveOccurred())
		Expect(database.QueryRow(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, status, attempt, result)
			VALUES ($1, $2, $3, $4, $5, 'alice', $6, 'git-publisher/v1',
			        'github.example/team/repo', 'pull-request',
			        '{"source_branch":"agent/change","target_branch":"main"}',
			        'engineering/v2', 'succeeded', 1,
			        '{"status":"succeeded","external_id":"pr-shared"}')
			RETURNING id
		`, "sha256:"+strings.Repeat("d", 64), teamID, teamName, firstRunID,
			firstBuildID, snapshotID).Scan(&publicationID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, publication_status, actor)
			VALUES ($1, $2, $3, 'accepted', 'published', $4, 'succeeded', 'alice')
		`, teamID, firstRunID, snapshotID, publicationID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var firstOccurrenceID, ownerID, linkedOutcomeID int64
		Expect(database.QueryRow(`
			SELECT occurrence.id, publication.lease_owner_occurrence_id
			FROM agent_publication_occurrences occurrence
			JOIN agent_publications publication ON publication.id = occurrence.publication_id
			WHERE publication.id = $1
		`, publicationID).Scan(&firstOccurrenceID, &ownerID)).To(Succeed())
		Expect(ownerID).To(Equal(firstOccurrenceID))
		Expect(database.QueryRow(`
			SELECT publication_id
			FROM agent_workflow_outcomes
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, firstRunID, snapshotID).Scan(&linkedOutcomeID)).To(Succeed())
		Expect(linkedOutcomeID).To(Equal(firstOccurrenceID))
		_, err = database.Exec(`
			UPDATE agent_publications SET status = 'failed' WHERE id = $1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "linked occurrence status cannot diverge from authoritative succeeded evidence")

		var secondBuildID int
		var secondRunID, secondOccurrenceID int64
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'bob') RETURNING id
		`, teamName+"-second", teamID).Scan(&secondBuildID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'bob', 'running', $8)
			RETURNING id
		`, teamID, teamName, definitionID, teamName, strings.Repeat("a", 64),
			teamName+"-second", strings.Repeat("b", 64), secondBuildID).Scan(&secondRunID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_publication_occurrences
				(publication_id, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, status)
			VALUES ($1, $2, $3, $4, $5, 'bob', $6, 'succeeded')
			RETURNING id
		`, publicationID, teamID, teamName, secondRunID, secondBuildID, snapshotID).Scan(&secondOccurrenceID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, publication_status, actor)
			VALUES ($1, $2, $3, 'accepted', 'published', $4, 'succeeded', 'bob')
		`, teamID, secondRunID, snapshotID, secondOccurrenceID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var firstState, secondState string
		Expect(database.QueryRow(`
			SELECT publication_state FROM agent_workflow_outcomes
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, firstRunID, snapshotID).Scan(&firstState)).To(Succeed())
		Expect(firstState).To(Equal("published"))
		Expect(database.QueryRow(`
			SELECT publication_state FROM agent_workflow_outcomes
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, secondRunID, snapshotID).Scan(&secondState)).To(Succeed())
		Expect(secondState).To(Equal("not_requested"))
	})
})
