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

// Task 1: agent run metrics carry durable workflow run identity, not tickets.
var _ = Describe("agent run metrics workflow run identity migration", func() {
	const beforeVersion, targetVersion = 1773106123, 1773106124

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

	// createRun mirrors the schema-v3 agent_workflow_runs insert used across the
	// migration suite (see active_workflow_run_retention_test.go).
	createRun := func(teamID int, teamName, key string) int64 {
		var definitionID int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, teamName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $2, 1, 3, 1, $4, $5, '{}', $6,
			        'manual', '', 'alice', 'running')
			RETURNING id
		`, teamID, teamName, definitionID, strings.Repeat("a", 64), key,
			strings.Repeat("b", 64)).Scan(&id)).To(Succeed())
		return id
	}

	insertBuild := func(teamID int, name string) int {
		var buildID int
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id) VALUES ($1, 'started', $2) RETURNING id
		`, name, teamID).Scan(&buildID)).To(Succeed())
		return buildID
	}

	insertMetric := func(buildID int, planID string) {
		_, err := database.Exec(`
			INSERT INTO agent_run_metrics (build_id, plan_id, step_name, status)
			VALUES ($1, $2, 'implement', 'ok')
		`, buildID, planID)
		Expect(err).NotTo(HaveOccurred())
	}

	metricWorkflowRun := func(buildID int, planID string) sql.NullInt64 {
		var runID sql.NullInt64
		Expect(database.QueryRow(`
			SELECT workflow_run_id FROM agent_run_metrics WHERE build_id = $1 AND plan_id = $2
		`, buildID, planID).Scan(&runID)).To(Succeed())
		return runID
	}

	metricUniqueKey := func() []string {
		rows, err := database.Query(`
			SELECT a.attname
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
			WHERE c.relname = 'agent_run_metrics_build_plan'
			ORDER BY array_position(i.indkey::int2[], a.attnum)
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			cols = append(cols, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return cols
	}

	metricColumns := func() []string {
		rows, err := database.Query(`
			SELECT column_name FROM information_schema.columns
			WHERE table_name = 'agent_run_metrics' ORDER BY ordinal_position
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			cols = append(cols, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return cols
	}

	hasIndex := func(name string) bool {
		var exists bool
		Expect(database.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM pg_indexes
			WHERE tablename = 'agent_run_metrics' AND indexname = $1)
		`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	Context("Up", func() {
		It("backfills exact workflow run identity without changing metric idempotency", func() {
			suffix := time.Now().UnixNano()
			teamName := fmt.Sprintf("metrics-identity-%d", suffix)
			var teamID int
			Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName).Scan(&teamID)).To(Succeed())

			runID := createRun(teamID, teamName, fmt.Sprintf("run-%d", suffix))
			plannedBuildID := insertBuild(teamID, teamName+"-planned")
			_, err := database.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, runID, plannedBuildID)
			Expect(err).NotTo(HaveOccurred())

			// A build with no planned workflow run — a pure-CI invocation.
			unrelatedBuildID := insertBuild(teamID, teamName+"-unrelated")

			insertMetric(plannedBuildID, "plan-agent")
			insertMetric(unrelatedBuildID, "unrelated")

			Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

			By("binding the planned build's metric to the workflow run")
			Expect(metricWorkflowRun(plannedBuildID, "plan-agent")).To(Equal(sql.NullInt64{
				Int64: runID, Valid: true,
			}))

			By("never guessing identity for an unrelated build")
			Expect(metricWorkflowRun(unrelatedBuildID, "unrelated").Valid).To(BeFalse())

			By("keeping (build_id, plan_id) as the ingestion idempotency key")
			Expect(metricUniqueKey()).To(Equal([]string{"build_id", "plan_id"}))

			By("removing the ticket identity columns and adding durable identity")
			Expect(metricColumns()).NotTo(ContainElements("ticket_id", "pipeline_run_id"))
			Expect(metricColumns()).To(ContainElements("workflow_run_id", "function_id"))

			By("replacing the ticket index with the durable workflow-run index")
			Expect(hasIndex("agent_run_metrics_ticket")).To(BeFalse())
			Expect(hasIndex("agent_run_metrics_workflow_run")).To(BeTrue())

			By("releasing metric identity when the run is deleted (ON DELETE SET NULL)")
			_, err = database.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(metricWorkflowRun(plannedBuildID, "plan-agent").Valid).To(BeFalse())
		})
	})

	Context("Down", func() {
		It("restores the nullable ticket columns and ticket index, dropping durable identity", func() {
			Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

			// agent_run_metrics.build_id has no FK, so a durable-schema row needs
			// no real build — only proof it survives the down round trip.
			const buildID = 918273
			_, err := database.Exec(`
				INSERT INTO agent_run_metrics (build_id, plan_id, step_name, status, function_id)
				VALUES ($1, 'p', 'implement', 'ok', 'review')
			`, buildID)
			Expect(err).NotTo(HaveOccurred())

			Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

			Expect(metricColumns()).To(ContainElements("ticket_id", "pipeline_run_id"))
			Expect(metricColumns()).NotTo(ContainElements("workflow_run_id", "function_id"))
			Expect(hasIndex("agent_run_metrics_ticket")).To(BeTrue())
			Expect(hasIndex("agent_run_metrics_workflow_run")).To(BeFalse())

			// the legacy row survives the round trip with its (build_id, plan_id) key
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM agent_run_metrics WHERE build_id = $1 AND plan_id = 'p'`, buildID).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1))
		})
	})
})
