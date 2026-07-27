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

// Batch 1: agent spend is attributed to the server-owned workflow run and
// function, never to the v2 plan-env ticket/pipeline identity.
var _ = Describe("agent cost ledger workflow identity migration", func() {
	const beforeVersion, targetVersion = 1773106128, 1773106129

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

	ledgerColumns := func() []string {
		rows, err := database.Query(`
			SELECT column_name FROM information_schema.columns
			WHERE table_name = 'agent_cost_ledger' ORDER BY ordinal_position
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
			WHERE tablename = 'agent_cost_ledger' AND indexname = $1)
		`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	runIDFor := func(buildID int) sql.NullInt64 {
		var runID sql.NullInt64
		Expect(database.QueryRow(
			`SELECT workflow_run_id FROM agent_cost_ledger WHERE build_id = $1`, buildID,
		).Scan(&runID)).To(Succeed())
		return runID
	}

	Context("Up", func() {
		It("backfills run identity from the planned build and narrows the source vocabulary", func() {
			suffix := time.Now().UnixNano()
			teamName := fmt.Sprintf("ledger-identity-%d", suffix)
			var teamID int
			Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName).Scan(&teamID)).To(Succeed())

			runID := createRun(teamID, teamName, fmt.Sprintf("run-%d", suffix))
			plannedBuildID := insertBuild(teamID, teamName+"-planned")
			_, err := database.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, runID, plannedBuildID)
			Expect(err).NotTo(HaveOccurred())
			unrelatedBuildID := insertBuild(teamID, teamName+"-unrelated")

			_, err = database.Exec(`
				INSERT INTO agent_cost_ledger (build_id, ticket_id, source, cost_usd)
				VALUES ($1, 42, 'agent_step', 1.5), ($2, NULL, 'ci_agent', 2.0)
			`, plannedBuildID, unrelatedBuildID)
			Expect(err).NotTo(HaveOccurred())

			// a row from a subsystem that v3 removed
			_, err = database.Exec(`
				INSERT INTO agent_cost_ledger (step_name, source, cost_usd)
				VALUES ('judge', 'harvest_judge', 9.0)
			`)
			Expect(err).NotTo(HaveOccurred())

			Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

			By("binding the planned build's spend to its workflow run")
			Expect(runIDFor(plannedBuildID)).To(Equal(sql.NullInt64{Int64: runID, Valid: true}))

			By("never guessing identity for a build with no planned run")
			Expect(runIDFor(unrelatedBuildID).Valid).To(BeFalse())

			By("dropping the v2 attribution columns for the server-owned pair")
			Expect(ledgerColumns()).NotTo(ContainElements("ticket_id", "pipeline_run_id"))
			Expect(ledgerColumns()).To(ContainElements("workflow_run_id", "function_id"))
			Expect(hasIndex("agent_cost_ledger_ticket")).To(BeFalse())
			Expect(hasIndex("agent_cost_ledger_workflow_run")).To(BeTrue())

			By("deleting spend from the removed sources and refusing new ones")
			var judgeRows int
			Expect(database.QueryRow(`SELECT count(*) FROM agent_cost_ledger WHERE cost_usd = 9.0`).Scan(&judgeRows)).To(Succeed())
			Expect(judgeRows).To(BeZero())
			for _, retired := range []string{"gateway", "harvest_judge", "retrospective", "probe"} {
				_, err = database.Exec(
					`INSERT INTO agent_cost_ledger (source, cost_usd) VALUES ($1, 1)`, retired)
				Expect(err).To(HaveOccurred(), "source %q must be rejected", retired)
			}

			By("releasing spend identity when the run is deleted (ON DELETE SET NULL)")
			_, err = database.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(runIDFor(plannedBuildID).Valid).To(BeFalse())
		})
	})

	Context("Down", func() {
		It("restores the legacy columns, index and source vocabulary", func() {
			Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

			_, err := database.Exec(`
				INSERT INTO agent_cost_ledger (build_id, function_id, source, cost_usd)
				VALUES (918273, 'review', 'agent_step', 0.5)
			`)
			Expect(err).NotTo(HaveOccurred())

			Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

			Expect(ledgerColumns()).To(ContainElements("ticket_id", "pipeline_run_id"))
			Expect(ledgerColumns()).NotTo(ContainElements("workflow_run_id", "function_id"))
			Expect(hasIndex("agent_cost_ledger_ticket")).To(BeTrue())
			Expect(hasIndex("agent_cost_ledger_workflow_run")).To(BeFalse())

			_, err = database.Exec(
				`INSERT INTO agent_cost_ledger (source, cost_usd) VALUES ('harvest_judge', 1)`)
			Expect(err).NotTo(HaveOccurred())

			var count int
			Expect(database.QueryRow(
				`SELECT count(*) FROM agent_cost_ledger WHERE build_id = 918273`).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1))
		})
	})
})
