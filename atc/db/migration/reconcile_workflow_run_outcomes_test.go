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

var _ = Describe("workflow-run outcome reconciliation migration", func() {
	const (
		beforeVersion = 1773106102
		targetVersion = 1773106103
	)

	var (
		database    *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		migrator    migration.Migrator
		teamID      int
		definition  int
		templateID  int
		instanceID  int
		pipelineRun int
		buildID     int64
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

		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, fmt.Sprintf("workflow-outcomes-%d", time.Now().UnixNano())).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'migration-test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("workflow-outcomes-%d", time.Now().UnixNano()), strings.Repeat("a", 64)).Scan(&definition)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("outcome-template-%d", time.Now().UnixNano()), teamID).Scan(&templateID)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, fmt.Sprintf("outcome-instance-%d", time.Now().UnixNano()), teamID).Scan(&instanceID)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRun)).To(Succeed())
		buildID = int64(1<<31) + 37
		_, err = database.Exec(`
			INSERT INTO builds (id, name, status, team_id, pipeline_id)
			VALUES ($1, 'selected', 'started', $2, $3)
		`, buildID, teamID, instanceID)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	insertRun := func(key string, dependencies any) int64 {
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version,
				 definition_content_hash, idempotency_key, parameterized_config,
				 parameterized_config_hash, concrete_config, concrete_config_hash,
				 actual_plan, actual_plan_hash, resolved_dependencies,
				 origin_kind, origin_reference, created_by, status,
				 pipeline_run_id, template_pipeline_id, instance_pipeline_id,
				 planned_build_id)
			VALUES
				($1, 'migration-team', $2, 'migration-workflow', 1, 3, 1,
				 $3, $4, '{"jobs":[]}', $5, '{"instance":true}', $6,
				 '{"task":"review"}', $7, $8,
				 'migration', $4, 'migration-test', 'running',
				 $9, $10, $11, $12)
			RETURNING id
		`, teamID, definition, strings.Repeat("a", 64), key, strings.Repeat("b", 64),
			strings.Repeat("c", 64), strings.Repeat("d", 64), dependencies,
			pipelineRun, templateID, instanceID, buildID).Scan(&id)).To(Succeed())
		return id
	}

	It("backfills the canonical empty dependency envelope and enforces the outcome schema", func() {
		runID := insertRun("migration-complete", nil)

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var (
			executionStatus sql.NullString
			reconcileAfter  time.Time
			plannedBuild    int64
			dependencies    []byte
		)
		Expect(database.QueryRow(`
			SELECT execution_status, reconcile_after, planned_build_id, resolved_dependencies
			FROM agent_workflow_runs WHERE id = $1
		`, runID).Scan(&executionStatus, &reconcileAfter, &plannedBuild, &dependencies)).To(Succeed())
		Expect(executionStatus.Valid).To(BeFalse())
		Expect(reconcileAfter).NotTo(BeZero())
		Expect(plannedBuild).To(Equal(buildID))
		Expect(dependencies).To(MatchJSON(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`))

		var indexDefinition string
		Expect(database.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'agent_workflow_runs_reconcile_due'`).Scan(&indexDefinition)).To(Succeed())
		Expect(indexDefinition).To(ContainSubstring("(reconcile_after, id)"))
		Expect(indexDefinition).To(ContainSubstring("status = ANY"))

		_, err := database.Exec(`UPDATE agent_workflow_runs SET execution_status = 'future' WHERE id = $1`, runID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_workflow_runs SET instance_pipeline_id = NULL WHERE id = $1`, runID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_workflow_runs SET resolved_dependencies = NULL WHERE id = $1`, runID)
		Expect(err).To(HaveOccurred())

		_, err = database.Exec(`UPDATE agent_workflow_runs SET execution_status = 'succeeded' WHERE id = $1`, runID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_workflow_runs SET planned_build_id = NULL WHERE id = $1`, runID)
		Expect(err).To(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_workflow_run_anomalies
				(workflow_run_id, kind, build_id, build_status)
			VALUES ($1, 'later_build_started', 99, 'started')
		`, runID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_run_anomalies
				(workflow_run_id, kind, build_id, build_status)
			VALUES ($1, 'later_build_started', 99, 'started')
		`, runID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_run_anomalies
				(workflow_run_id, kind, build_id, build_status)
			VALUES ($1, 'unknown', 100, 'started')
		`, runID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_run_anomalies
				(workflow_run_id, kind, build_id, build_status)
			VALUES ($1, 'later_build_completed', 100, 'future')
		`, runID)
		Expect(err).To(HaveOccurred())
	})

	It("retains copied scalar provenance after ephemeral deletion and safely restores foreign keys on down", func() {
		runID := insertRun("migration-deletion", `{ "version": 1, "resources": [], "images": [], "platform_resource_types": [] }`)
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		_, err := database.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipeline_runs WHERE id = $1`, pipelineRun)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM pipelines WHERE id IN ($1, $2)`, templateID, instanceID)
		Expect(err).NotTo(HaveOccurred())

		var gotPipeline, gotTemplate, gotInstance int
		var gotBuild int64
		Expect(database.QueryRow(`
			SELECT pipeline_run_id, template_pipeline_id, instance_pipeline_id, planned_build_id
			FROM agent_workflow_runs WHERE id = $1
		`, runID).Scan(&gotPipeline, &gotTemplate, &gotInstance, &gotBuild)).To(Succeed())
		Expect(gotPipeline).To(Equal(pipelineRun))
		Expect(gotTemplate).To(Equal(templateID))
		Expect(gotInstance).To(Equal(instanceID))
		Expect(gotBuild).To(Equal(buildID))

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var pipeline, template, instance, planned sql.NullInt64
		var concrete []byte
		var concreteHash sql.NullString
		Expect(database.QueryRow(`
			SELECT pipeline_run_id, template_pipeline_id, instance_pipeline_id,
			       concrete_config, concrete_config_hash, planned_build_id
			FROM agent_workflow_runs WHERE id = $1
		`, runID).Scan(&pipeline, &template, &instance, &concrete, &concreteHash, &planned)).To(Succeed())
		Expect(pipeline.Valid || template.Valid || instance.Valid || concrete != nil || concreteHash.Valid || planned.Valid).To(BeFalse())

		rows, err := database.Query(`
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conname IN (
				'agent_workflow_runs_pipeline_run_id_fkey',
				'agent_workflow_runs_template_pipeline_id_fkey',
				'agent_workflow_runs_instance_pipeline_id_fkey',
				'agent_workflow_runs_planned_build_id_fkey')
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var definitions []string
		for rows.Next() {
			var definition string
			Expect(rows.Scan(&definition)).To(Succeed())
			definitions = append(definitions, definition)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(4))
		for _, definition := range definitions {
			Expect(definition).To(ContainSubstring("ON DELETE SET NULL"))
		}
	})

	It("rejects partial pre-migration plan provenance instead of guessing", func() {
		var runID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version,
				 definition_content_hash, idempotency_key, parameterized_config,
				 parameterized_config_hash, resolved_dependencies,
				 origin_kind, origin_reference, created_by, status)
			VALUES ($1, 'migration-team', $2, 'migration-workflow', 1, 3, 1,
			        $3, 'partial-plan', '{}', $4, '{"version":1}',
			        'migration', 'partial', 'migration-test', 'admitting')
			RETURNING id
		`, teamID, definition, strings.Repeat("a", 64), strings.Repeat("b", 64)).Scan(&runID)).To(Succeed())

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("partial workflow-run plan provenance")))
		var columns int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_runs'
			  AND column_name IN ('execution_status', 'reconcile_after')
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(0), "the failed migration must roll back atomically")
	})
})
