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

var _ = Describe("agent PR bindings migration", func() {
	const beforeVersion, targetVersion = 1773106150, 1773106151

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
		teamID   int
		defID    int
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

		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("pr-binding-%d", GinkgoRandomSeed())).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("pr-monitor-%d", GinkgoRandomSeed()), strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("discovers the generated legacy constraint and replaces it with instance-scoped indexes", func() {
		var beforeCount int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_resource_source_pipelines'::regclass
			  AND contype = 'u'
			  AND pg_get_constraintdef(oid) = 'UNIQUE (team_id, workflow_definition_id)'
		`).Scan(&beforeCount)).To(Succeed())
		Expect(beforeCount).To(Equal(1))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var afterCount int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_resource_source_pipelines'::regclass
			  AND contype = 'u'
			  AND pg_get_constraintdef(oid) = 'UNIQUE (team_id, workflow_definition_id)'
		`).Scan(&afterCount)).To(Succeed())
		Expect(afterCount).To(BeZero())

		var definitionIndex, bindingIndex, activeIndex string
		Expect(database.QueryRow(`
			SELECT indexdef FROM pg_indexes
			WHERE tablename='agent_workflow_resource_source_pipelines'
			  AND indexname='agent_workflow_resource_source_pipelines_definition'
		`).Scan(&definitionIndex)).To(Succeed())
		Expect(definitionIndex).To(ContainSubstring("WHERE (pr_binding_id IS NULL)"))
		Expect(database.QueryRow(`
			SELECT indexdef FROM pg_indexes
			WHERE tablename='agent_workflow_resource_source_pipelines'
			  AND indexname='agent_workflow_resource_source_pipelines_binding'
		`).Scan(&bindingIndex)).To(Succeed())
		Expect(bindingIndex).To(ContainSubstring("WHERE (pr_binding_id IS NOT NULL)"))
		Expect(database.QueryRow(`
			SELECT indexdef FROM pg_indexes
			WHERE tablename='agent_workflow_resource_source_pipelines'
			  AND indexname='agent_workflow_resource_source_pipelines_active'
		`).Scan(&activeIndex)).To(Succeed())
		Expect(activeIndex).To(ContainSubstring("pr_binding_id IS NULL"))
	})

	It("lets distinct bindings share a monitor definition while preserving legacy uniqueness", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		originRunID, occurrenceID, observationID := insertPRBindingMigrationEvidence(
			database, teamID, defID, "shared-definition",
		)

		insertBinding := func(externalID string) int64 {
			var id int64
			Expect(database.QueryRow(`
				INSERT INTO agent_pr_bindings
					(team_id, provider, repository, external_id, url, source_ref, target_ref,
					 originating_workflow_run_id, originating_publication_occurrence_id,
					 monitor_workflow_definition_id, monitor_workflow_version,
					 acknowledged_cursor, last_observation_snapshot_id,
					 last_reconciled_source_sha, last_reconciled_target_sha, last_reconciled_at)
				VALUES ($1, 'github', 'example/repository', $2, $3, 'refs/heads/source',
				        'refs/heads/main', $4, $5, $6, 3, to_jsonb($7::text), $8, $9, $10, now())
				RETURNING id
			`, teamID, externalID, "https://example.invalid/pull/"+externalID,
				originRunID, occurrenceID, defID, `{"opaque":"`+externalID+`"}`,
				observationID, strings.Repeat("b", 40), strings.Repeat("c", 40),
			).Scan(&id)).To(Succeed())
			return id
		}
		firstBinding := insertBinding("101")
		secondBinding := insertBinding("102")

		insertPipeline := func(name string) int {
			var id int
			Expect(database.QueryRow(`
				INSERT INTO pipelines (name, team_id, secondary_ordering, paused)
				VALUES ($1, $2, 1, true) RETURNING id
			`, name, teamID).Scan(&id)).To(Succeed())
			return id
		}
		register := func(pipelineID int, bindingID *int64, name string) error {
			_, err := database.Exec(`
				INSERT INTO agent_workflow_resource_source_pipelines
					(pipeline_id, team_id, workflow_definition_id, workflow_name,
					 workflow_version, pipeline_config_version, config_hash,
					 source_declarations, state, pr_binding_id)
				VALUES ($1, $2, $3, $4, 3, 1, $5, '[]', 'active', $6)
			`, pipelineID, teamID, defID, name, strings.Repeat("d", 64), bindingID)
			return err
		}

		Expect(register(insertPipeline("binding-one"), &firstBinding, "pr-monitor")).To(Succeed())
		Expect(register(insertPipeline("binding-two"), &secondBinding, "pr-monitor")).To(Succeed())
		Expect(register(insertPipeline("legacy-one"), nil, "legacy-one")).To(Succeed())
		Expect(register(insertPipeline("legacy-two"), nil, "legacy-two")).To(HaveOccurred())
	})

	It("nulls a binding pipeline link on physical deletion and refuses an unsafe down migration", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		originRunID, occurrenceID, observationID := insertPRBindingMigrationEvidence(
			database, teamID, defID, "pipeline-link",
		)
		var bindingID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_pr_bindings
				(team_id, provider, repository, external_id, url, source_ref, target_ref,
				 originating_workflow_run_id, originating_publication_occurrence_id,
				 monitor_workflow_definition_id, monitor_workflow_version,
				 acknowledged_cursor, last_observation_snapshot_id,
				 last_reconciled_source_sha, last_reconciled_target_sha, last_reconciled_at)
			VALUES ($1, 'github', 'org/repository', '17',
			        'https://github.example/org/repository/pull/17',
			        'refs/heads/source', 'refs/heads/main', $2, $3, $4, 3,
			        to_jsonb('opaque'::text), $5, $6, $7, now())
			RETURNING id
		`, teamID, originRunID, occurrenceID, defID, observationID,
			strings.Repeat("b", 40), strings.Repeat("c", 40),
		).Scan(&bindingID)).To(Succeed())

		var disposablePipelineID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ('disposable-binding-link', $1, 1) RETURNING id
		`, teamID).Scan(&disposablePipelineID)).To(Succeed())
		_, err := database.Exec(`UPDATE agent_pr_bindings SET pipeline_id=$1 WHERE id=$2`, disposablePipelineID, bindingID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE id=$1`, disposablePipelineID)
		Expect(err).NotTo(HaveOccurred())
		var linked sql.NullInt64
		Expect(database.QueryRow(`SELECT pipeline_id FROM agent_pr_bindings WHERE id=$1`, bindingID).Scan(&linked)).To(Succeed())
		Expect(linked.Valid).To(BeFalse())

		var ownedPipelineID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering, paused)
			VALUES ('owned-binding-link', $1, 1, true) RETURNING id
		`, teamID).Scan(&ownedPipelineID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_resource_source_pipelines
				(pipeline_id, team_id, workflow_definition_id, workflow_name,
				 workflow_version, pipeline_config_version, config_hash,
				 source_declarations, state, pr_binding_id)
			VALUES ($1, $2, $3, 'pr-monitor', 3, 1, $4, '[]', 'active', $5)
		`, ownedPipelineID, teamID, defID, strings.Repeat("d", 64), bindingID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_pr_bindings WHERE id=$1`, bindingID)
		Expect(err).To(HaveOccurred(), "binding provenance must not orphan its physical pipeline registry")
		Expect(migrator.Migrate(nil, nil, beforeVersion)).NotTo(Succeed())

		_, err = database.Exec(`DELETE FROM agent_workflow_resource_source_pipelines WHERE pr_binding_id=$1`, bindingID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var restored int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = 'agent_workflow_resource_source_pipelines'::regclass
			  AND contype = 'u'
			  AND pg_get_constraintdef(oid) = 'UNIQUE (team_id, workflow_definition_id)'
		`).Scan(&restored)).To(Succeed())
		Expect(restored).To(Equal(1))
	})
})

func insertPRBindingMigrationEvidence(
	database *sql.DB,
	teamID int,
	definitionID int,
	suffix string,
) (int64, int64, int64) {
	var (
		pipelineID    int
		buildID       int64
		workflowRunID int64
		snapshotID    int64
	)
	Expect(database.QueryRow(`
		INSERT INTO pipelines (name, team_id, secondary_ordering)
		VALUES ($1, $2, 1) RETURNING id
	`, "pr-binding-origin-"+suffix, teamID).Scan(&pipelineID)).To(Succeed())
	Expect(database.QueryRow(`
		INSERT INTO builds (name, status, team_id, pipeline_id, completed, end_time)
		VALUES ($1, 'succeeded', $2, $3, true, now()) RETURNING id
	`, "pr-binding-origin-"+suffix, teamID, pipelineID).Scan(&buildID)).To(Succeed())
	Expect(database.QueryRow(`
		INSERT INTO agent_workflow_runs
			(definition_kind, team_id, team_name, workflow_definition_id,
			 workflow_name, workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key, parameterized_config,
			 parameterized_config_hash, origin_kind, origin_reference, created_by,
			 status, planned_build_id, completed_at)
		SELECT 'workflow', $1, team.name, $2, definition.name,
		       definition.version, definition.schema_version, definition.signature_version,
		       definition.content_hash, $3, '{}'::jsonb, $4,
		       'initial-publish', $5, 'test', 'succeeded', $6, now()
		FROM teams team, agent_workflow_definitions definition
		WHERE team.id=$1 AND definition.id=$2
		RETURNING id
	`, teamID, definitionID, "pr-binding-origin-"+suffix, strings.Repeat("e", 64),
		suffix, buildID).Scan(&workflowRunID)).To(Succeed())
	Expect(database.QueryRow(`
		INSERT INTO agent_snapshots
			(team_id, type_name, type_version, digest, byte_size, file_count, representation)
		VALUES ($1, 'pull-request', 1, $2, 1, 1,
		        'application/vnd.jetbridge.snapshot.tar.v1')
		RETURNING id
	`, teamID, "sha256:"+fmt.Sprintf("%064x", int64(len(suffix))+workflowRunID)).Scan(&snapshotID)).To(Succeed())

	tx, err := database.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = tx.Rollback() }()
	var publicationID, occurrenceID int64
	Expect(tx.QueryRow(`SELECT nextval('agent_publications_id_seq')`).Scan(&publicationID)).To(Succeed())
	Expect(tx.QueryRow(`SELECT nextval('agent_publication_occurrences_id_seq')`).Scan(&occurrenceID)).To(Succeed())
	_, err = tx.Exec(`SET CONSTRAINTS agent_publications_lease_owner_occurrence_fkey DEFERRED`)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publications
			(id, operation_key, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, publisher, destination, mode, parameters,
			 approval_policy_version, status, result, lease_owner_occurrence_id)
		SELECT $1, $2, $3, team.name, $4, $5, 'test', $6,
		       'forge-pr', 'origin', 'pull-request', '{}', 'v1',
		       'succeeded', '{"external_id":"created"}', $7
		FROM teams team WHERE team.id=$3
	`, publicationID, "sha256:"+fmt.Sprintf("%064x", publicationID+1000), teamID,
		workflowRunID, buildID, snapshotID, occurrenceID)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`
		INSERT INTO agent_publication_occurrences
			(id, publication_id, team_id, team_name, workflow_run_id, build_id,
			 actor, input_snapshot_id, status)
		SELECT $1, $2, $3, team.name, $4, $5, 'test', $6, 'succeeded'
		FROM teams team WHERE team.id=$3
	`, occurrenceID, publicationID, teamID, workflowRunID, buildID, snapshotID)
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	return workflowRunID, occurrenceID, snapshotID
}
