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

var _ = Describe("workflow outcome publication integrity migration", func() {
	const beforeVersion, targetVersion = 1773106114, 1773106115
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

	It("reconciles unverified legacy claims and enforces exact succeeded publication evidence", func() {
		unique := time.Now().UnixNano()
		var teamID, definitionID, buildID int
		var runID, outputID, nullPublicationIDSnapshotID, otherSnapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("publication-integrity-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'fixture', 3, 1)
			RETURNING id
		`, fmt.Sprintf("publication-integrity-%d", unique), strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'alice') RETURNING id
		`, fmt.Sprintf("publication-integrity-%d", unique), teamID).Scan(&buildID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'alice', 'running', $8)
			RETURNING id
		`, teamID, fmt.Sprintf("publication-integrity-%d", unique), definitionID,
			fmt.Sprintf("publication-integrity-%d", unique), strings.Repeat("a", 64),
			fmt.Sprintf("publication-integrity-%d", unique), strings.Repeat("b", 64), buildID).Scan(&runID)).To(Succeed())
		for index, destination := range []*int64{&outputID, &nullPublicationIDSnapshotID, &otherSnapshotID} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation, content_state)
				VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, "sha256:"+strings.Repeat(fmt.Sprintf("%x", index+1), 64)).Scan(destination)).To(Succeed())
		}
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots (workflow_run_id, direction, port_name, snapshot_id)
			VALUES ($1, 'output', 'change', $2)
		`, runID, outputID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, actor)
			VALUES ($1, $2, $3, 'merged', 'published', 987654321, 'legacy-observer')
		`, teamID, runID, outputID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, actor)
			VALUES ($1, $2, $3, 'accepted', 'published', NULL, 'legacy-observer')
		`, teamID, runID, nullPublicationIDSnapshotID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		rows, err := database.Query(`
			SELECT publication_state, publication_id, publication_status
			FROM agent_workflow_outcomes
			WHERE workflow_run_id = $1
			  AND output_snapshot_id IN ($2, $3)
			ORDER BY output_snapshot_id
		`, runID, outputID, nullPublicationIDSnapshotID)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for range 2 {
			Expect(rows.Next()).To(BeTrue())
			var state string
			var publicationID sql.NullInt64
			var publicationStatus sql.NullString
			Expect(rows.Scan(&state, &publicationID, &publicationStatus)).To(Succeed())
			Expect(state).To(Equal("not_requested"))
			Expect(publicationID.Valid).To(BeFalse())
			Expect(publicationStatus.Valid).To(BeFalse())
		}
		Expect(rows.Next()).To(BeFalse())
		Expect(rows.Err()).NotTo(HaveOccurred())

		var exactPublicationID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_publications
				(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, publisher, destination, mode, parameters,
				 approval_policy_version, status, attempt, result)
			VALUES ($1, $2, $3, $4, $5, 'alice', $6, 'git-publisher/v1',
			        'github.example/team/repo', 'pull-request',
			        '{"source_branch":"agent/change","target_branch":"main"}',
			        'engineering/v2', 'succeeded', 1, '{"status":"succeeded","external_id":"pr-17"}')
			RETURNING id
		`, "sha256:"+strings.Repeat("c", 64), teamID, fmt.Sprintf("publication-integrity-%d", unique),
			runID, buildID, outputID).Scan(&exactPublicationID)).To(Succeed())
		_, err = database.Exec(`
			UPDATE agent_workflow_outcomes
			SET publication_state = 'published', publication_id = $1, publication_status = 'succeeded'
			WHERE workflow_run_id = $2 AND output_snapshot_id = $3
		`, exactPublicationID, runID, outputID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_workflow_outcomes
			SET publication_status = NULL
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, runID, outputID)
		Expect(err).To(HaveOccurred(), "published state cannot bypass evidence validation with a NULL status")

		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, publication_status, actor)
			VALUES ($1, $2, $3, 'accepted', 'published', $4, 'succeeded', 'forged')
		`, teamID, runID, otherSnapshotID, exactPublicationID)
		Expect(err).To(HaveOccurred(), "a publication cannot be attached to another output snapshot")
		_, err = database.Exec(`
			UPDATE agent_workflow_outcomes
			SET publication_id = NULL, publication_status = NULL
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, runID, outputID)
		Expect(err).To(HaveOccurred(), "published state requires exact durable evidence")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var integrityColumns int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_outcomes' AND column_name = 'publication_status'
		`).Scan(&integrityColumns)).To(Succeed())
		Expect(integrityColumns).To(Equal(0))
	})
})
