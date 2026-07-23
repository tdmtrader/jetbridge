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

var _ = Describe("active workflow-run snapshot retention migration", func() {
	const beforeVersion, targetVersion = 1773106120, 1773106121

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

	It("backfills only active ownership, enforces exact run association, and downgrades conservatively", func() {
		suffix := time.Now().UnixNano()
		teamName := fmt.Sprintf("run-retention-%d", suffix)
		var teamID, otherTeamID, definitionID, buildID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName+"-other").Scan(&otherTeamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, teamName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

		createRun := func(key, status string) int64 {
			var id int64
			Expect(database.QueryRow(`
				INSERT INTO agent_workflow_runs
					(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
					 schema_version, signature_version, definition_content_hash, idempotency_key,
					 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
					 created_by, status)
				VALUES ($1, $2, $3, $2, 1, 3, 1, $4, $5, '{}', $6,
				        'manual', '', 'alice', $7)
				RETURNING id
			`, teamID, teamName, definitionID, strings.Repeat("a", 64), key,
				strings.Repeat("b", 64), status).Scan(&id)).To(Succeed())
			return id
		}
		activeRunID := createRun(fmt.Sprintf("active-%d", suffix), "running")
		terminalRunID := createRun(fmt.Sprintf("terminal-%d", suffix), "failed")

		var inputID, productionID, terminalInputID int64
		for index, target := range []*int64{&inputID, &productionID, &terminalInputID} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ('opaque', 1, $1, 1, 1, 'application/x-tar')
				RETURNING id
			`, "sha256:"+strings.Repeat(string(rune('c'+index)), 64)).Scan(target)).To(Succeed())
		}
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES
				($1, 'input', 'subject', $2, now()),
				($3, 'input', 'subject', $4, now())
		`, activeRunID, inputID, terminalRunID, terminalInputID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, actor, reason)
			VALUES
				($1, $2, 'workflow', $3, 'durable workflow-run input'),
				($4, $2, 'workflow', $5, 'durable workflow-run input')
		`, inputID, teamID, fmt.Sprintf("workflow-run:%d:input:subject", activeRunID),
			terminalInputID, fmt.Sprintf("workflow-run:%d:input:subject", terminalRunID))
		Expect(err).NotTo(HaveOccurred())
		Expect(database.QueryRow(`
				INSERT INTO builds (name, status, team_id)
				VALUES ($1, 'started', $2) RETURNING id
			`, teamName+"-build", teamID).Scan(&buildID)).To(Succeed())
		_, err = database.Exec(`
				UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1
			`, activeRunID, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
				INSERT INTO agent_snapshot_productions
					(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
					 plan_id, attempt, step_kind, step_name, output_port)
				VALUES ($1, 'build', $2, $3, $4, 'alice', 'internal-plan', '1',
				        'task', 'internal', 'intermediate')
			`, productionID, buildID, teamID, teamName)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var activeClaims int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM agent_snapshot_retention_claims
			WHERE class = 'run' AND workflow_run_id = $1 AND team_id = $2
			  AND snapshot_id IN ($3, $4) AND expires_at IS NULL
		`, activeRunID, teamID, inputID, productionID).Scan(&activeClaims)).To(Succeed())
		Expect(activeClaims).To(Equal(2))
		var associatedRunID int64
		var associatedDefinitionID int
		Expect(database.QueryRow(`
				SELECT workflow_run_id, workflow_definition_id
				FROM agent_snapshot_productions
				WHERE snapshot_id = $1
			`, productionID).Scan(&associatedRunID, &associatedDefinitionID)).To(Succeed())
		Expect(associatedRunID).To(Equal(activeRunID))
		Expect(associatedDefinitionID).To(Equal(definitionID))

		var terminalClaims, legacyTerminalClaims int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE workflow_run_id = $1
		`, terminalRunID).Scan(&terminalClaims)).To(Succeed())
		Expect(terminalClaims).To(Equal(0))
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'workflow'
		`, terminalInputID).Scan(&legacyTerminalClaims)).To(Succeed())
		Expect(legacyTerminalClaims).To(Equal(0))

		invalidStatements := []struct {
			description string
			query       string
			args        []any
		}{
			{
				description: "run claims require a run ID",
				query: `INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, actor, reason)
					VALUES ($1, $2, 'run', 'missing-run', 'invalid')`,
				args: []any{inputID, teamID},
			},
			{
				description: "run claims cannot cross team ownership",
				query: `INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, workflow_run_id, actor, reason)
					VALUES ($1, $2, 'run', $3, 'wrong-team', 'invalid')`,
				args: []any{inputID, otherTeamID, activeRunID},
			},
			{
				description: "only run claims may name a run",
				query: `INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, workflow_run_id, actor, reason)
					VALUES ($1, $2, 'binding', $3, 'wrong-class', 'invalid')`,
				args: []any{inputID, teamID, activeRunID},
			},
			{
				description: "run claims cannot expire independently",
				query: `INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, workflow_run_id, expires_at, actor, reason)
					VALUES ($1, $2, 'run', $3, now() + interval '1 hour', 'expiring', 'invalid')`,
				args: []any{inputID, teamID, activeRunID},
			},
		}
		for _, statement := range invalidStatements {
			_, err := database.Exec(statement.query, statement.args...)
			Expect(err).To(HaveOccurred(), statement.description)
		}

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var runClaims, preservedClaims, restoredTerminalClaims int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims WHERE class = 'run'
		`).Scan(&runClaims)).To(Succeed())
		Expect(runClaims).To(Equal(0))
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id IN ($1, $2) AND class = 'workflow'
		`, inputID, productionID).Scan(&preservedClaims)).To(Succeed())
		Expect(preservedClaims).To(Equal(2))
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'workflow'
			  AND actor = $2
		`, terminalInputID,
			fmt.Sprintf("workflow-run:%d:input:subject", terminalRunID)).Scan(&restoredTerminalClaims)).To(Succeed())
		Expect(restoredTerminalClaims).To(Equal(1))
	})
})
