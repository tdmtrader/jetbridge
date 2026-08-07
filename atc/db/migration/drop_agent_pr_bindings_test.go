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

var _ = Describe("drop agent PR bindings migration", func() {
	const beforeVersion, targetVersion = 1773106165, 1773106166

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
		teamID   int
		defID    int
		unique   int64
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
		migrator = migration.NewMigrator(
			database, lock.NewLockFactory(lockDB, noop, noop),
		)
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		unique = GinkgoRandomSeed()
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("drop-pr-bindings-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("drop-pr-bindings-%d", unique),
			strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	relationExists := func(name string) bool {
		var present bool
		Expect(database.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)
		`, name).Scan(&present)).To(Succeed())
		return present
	}

	columnExists := func(table, column string) bool {
		var present bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = $1 AND column_name = $2
			)
		`, table, column).Scan(&present)).To(Succeed())
		return present
	}

	indexDefinition := func(name string) string {
		var definition string
		Expect(database.QueryRow(
			`SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name,
		).Scan(&definition)).To(Succeed())
		return definition
	}

	totalDefinitionUniqueConstraints := func() int {
		var total int
		Expect(database.QueryRow(`
			SELECT count(*) FROM pg_constraint
			WHERE conrelid = 'agent_workflow_resource_source_pipelines'::regclass
			  AND contype = 'u'
			  AND pg_get_constraintdef(oid) = 'UNIQUE (team_id, workflow_definition_id)'
		`).Scan(&total)).To(Succeed())
		return total
	}

	// Registers one definition-owned source pipeline. Returns the pipeline id
	// so a caller can try to register a second one against the same
	// definition and watch the restored constraint refuse it.
	registerSourcePipeline := func(suffix string) int {
		var pipelineID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, "drop-pr-bindings-"+suffix, teamID).Scan(&pipelineID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_resource_source_pipelines
				(pipeline_id, team_id, workflow_definition_id, workflow_name,
				 workflow_version, pipeline_config_version, config_hash,
				 source_declarations, state)
			VALUES ($1, $2, $3, $4, 3, 1, $5, '[]'::jsonb, 'active')
		`, pipelineID, teamID, defID, "drop-pr-bindings-"+suffix,
			strings.Repeat("d", 64))
		Expect(err).NotTo(HaveOccurred())
		return pipelineID
	}

	// A binding carrying everything 1773106154 froze onto it. The composite
	// keys demand the creation and baseline occurrences belong to the exact
	// originating run, so the same evidence satisfies all three.
	insertBinding := func(suffix string) {
		runID, occurrenceID, snapshotID := insertPRBindingMigrationEvidence(
			database, teamID, defID, "drop-pr-bindings-"+suffix,
		)
		_, err := database.Exec(`
			INSERT INTO agent_pr_bindings
				(team_id, provider, repository, external_id, url,
				 source_ref, target_ref,
				 originating_workflow_run_id,
				 originating_publication_occurrence_id,
				 monitor_workflow_definition_id, monitor_workflow_version,
				 acknowledged_cursor, last_observation_snapshot_id,
				 last_reconciled_source_sha, last_reconciled_target_sha,
				 last_reconciled_at, destination, approval_policy_version,
				 creation_publication_occurrence_id,
				 approved_baseline_repository_snapshot_id,
				 approved_baseline_validation_snapshot_id,
				 approved_baseline_publication_occurrence_id)
			VALUES ($1, 'github', 'example/repository', '118',
			        'https://github.example/example/repository/pull/118',
			        'refs/heads/feature/pr', 'refs/heads/main',
			        $2, $3, $4, 3, to_jsonb('initial'::text), $5,
			        $6, $7, now(), 'origin', 'v1', $3, $5, $5, $3)
		`, teamID, runID, occurrenceID, defID, snapshotID,
			strings.Repeat("b", 40), strings.Repeat("c", 40))
		Expect(err).NotTo(HaveOccurred())
	}

	It("removes the binding table, the discriminator, and the origin identity index", func() {
		Expect(relationExists("agent_pr_bindings")).To(BeTrue())
		Expect(columnExists("agent_workflow_resource_source_pipelines", "pr_binding_id")).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(relationExists("agent_pr_bindings")).To(BeFalse())
		Expect(columnExists("agent_workflow_resource_source_pipelines", "pr_binding_id")).To(BeFalse())
		Expect(relationExists("agent_workflow_resource_source_pipelines_binding")).To(BeFalse())
		Expect(relationExists("agent_publication_occurrences_pr_binding_origin_identity")).To(BeFalse())

		// The two occurrence indexes that back other tables' keys are not
		// collateral: only the PR-binding origin index goes.
		Expect(relationExists("agent_publication_occurrences_outcome_identity")).To(BeTrue())
		Expect(relationExists("agent_publication_occurrences_id_team_evidence")).To(BeTrue())
	})

	It("restores whole-table uniqueness the partial index only enforced over null bindings", func() {
		Expect(totalDefinitionUniqueConstraints()).To(Equal(0))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(totalDefinitionUniqueConstraints()).To(Equal(1))
		Expect(relationExists("agent_workflow_resource_source_pipelines_definition")).To(BeFalse())

		// The constraint is the point, not its presence in the catalog.
		registerSourcePipeline("first")
		var pipelineID int
		Expect(database.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, "drop-pr-bindings-second", teamID).Scan(&pipelineID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_resource_source_pipelines
				(pipeline_id, team_id, workflow_definition_id, workflow_name,
				 workflow_version, pipeline_config_version, config_hash,
				 source_declarations, state)
			VALUES ($1, $2, $3, $4, 3, 1, $5, '[]'::jsonb, 'active')
		`, pipelineID, teamID, defID, "drop-pr-bindings-second",
			strings.Repeat("d", 64))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("team_id"))
	})

	It("rebuilds the one-active-pipeline index without the binding predicate", func() {
		Expect(indexDefinition("agent_workflow_resource_source_pipelines_active")).
			To(ContainSubstring("pr_binding_id IS NULL"))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		definition := indexDefinition("agent_workflow_resource_source_pipelines_active")
		Expect(definition).NotTo(ContainSubstring("pr_binding_id"))
		Expect(definition).To(ContainSubstring("state = 'active'"))
		Expect(definition).To(ContainSubstring("team_id, workflow_name"))
	})

	It("refuses to drop a binding rather than destroying it", func() {
		insertBinding("live")

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"cannot remove agent PR bindings while bindings exist",
		))

		// The refusal is transactional: nothing was half-dropped.
		Expect(relationExists("agent_pr_bindings")).To(BeTrue())
		Expect(columnExists("agent_workflow_resource_source_pipelines", "pr_binding_id")).To(BeTrue())
	})

	It("restores the historical shape, and no row, on rollback", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(relationExists("agent_pr_bindings")).To(BeTrue())
		Expect(columnExists("agent_workflow_resource_source_pipelines", "pr_binding_id")).To(BeTrue())
		Expect(relationExists("agent_publication_occurrences_pr_binding_origin_identity")).To(BeTrue())
		Expect(totalDefinitionUniqueConstraints()).To(Equal(0))

		for _, index := range []string{
			"agent_workflow_resource_source_pipelines_definition",
			"agent_workflow_resource_source_pipelines_binding",
			"agent_pr_bindings_active_reservation_token",
			"agent_pr_bindings_active_workflow_run",
			"agent_pr_bindings_active_action",
			"agent_pr_bindings_active",
		} {
			Expect(relationExists(index)).To(BeTrue(), index)
		}
		Expect(indexDefinition("agent_workflow_resource_source_pipelines_active")).
			To(ContainSubstring("pr_binding_id IS NULL"))

		var total int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_pr_bindings`).Scan(&total)).To(Succeed())
		Expect(total).To(Equal(0))

		// The restored shape has to be the exact post-1773106154 one, since
		// the rest of the rollback chain drops constraints by name.
		for _, constraint := range []string{
			"agent_pr_bindings_originating_run_team_fkey",
			"agent_pr_bindings_originating_occurrence_run_team_fkey",
			"agent_pr_bindings_creation_occurrence_run_team_fkey",
			"agent_pr_bindings_baseline_repository_team_fkey",
			"agent_pr_bindings_baseline_validation_team_fkey",
			"agent_pr_bindings_baseline_occurrence_team_fkey",
		} {
			var present bool
			Expect(database.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM pg_constraint
					WHERE conrelid = 'agent_pr_bindings'::regclass AND conname = $1
				)
			`, constraint).Scan(&present)).To(Succeed())
			Expect(present).To(BeTrue(), constraint)
		}

		// A rebuilt binding is writable, and the whole chain below still
		// unwinds -- which is what proves the reconstruction is faithful
		// rather than merely table-shaped.
		insertBinding("rebuilt")
		Expect(database.Exec(`DELETE FROM agent_pr_bindings`)).ToNot(BeNil())
		Expect(migrator.Migrate(nil, nil, 1773106150)).To(Succeed())
	})
})
