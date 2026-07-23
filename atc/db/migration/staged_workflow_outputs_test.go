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

var _ = Describe("staged workflow outputs migration", func() {
	const beforeVersion, targetVersion = 1773106116, 1773106117
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

	It("preserves successful results while hiding incomplete historical candidates", func() {
		var teamID, definitionID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('staged-output-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('staged-output-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

		createRun := func(key, status string) int64 {
			var id int64
			Expect(database.QueryRow(`
				INSERT INTO agent_workflow_runs
					(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
					 schema_version, signature_version, definition_content_hash, idempotency_key,
					 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
					 created_by, status)
				VALUES ($1, 'staged-output-migration', $2, 'staged-output-migration', 1,
				        3, 1, $3, $4, '{}', $5, 'manual', '', 'alice', $6)
				RETURNING id
			`, teamID, definitionID, strings.Repeat("a", 64), key, strings.Repeat("b", 64), status).Scan(&id)).To(Succeed())
			return id
		}
		succeededRun := createRun("migration-succeeded", "succeeded")
		failedRun := createRun("migration-failed", "failed")

		var inputID, succeededID, failedID int64
		for index, target := range []*int64{&inputID, &succeededID, &failedID} {
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ('opaque', 1, $1, 1, 1, 'application/x-tar') RETURNING id
			`, "sha256:"+strings.Repeat(string(rune('c'+index)), 64)).Scan(target)).To(Succeed())
		}
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots (workflow_run_id, direction, port_name, snapshot_id)
			VALUES
				($1, 'input', 'subject', $3),
				($1, 'output', 'result', $4),
				($2, 'output', 'partial', $5)
		`, succeededRun, failedRun, inputID, succeededID, failedID)
		Expect(err).NotTo(HaveOccurred())
		var failedProductionID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
				 plan_id, attempt, step_kind, step_name, output_port,
				 workflow_definition_id, workflow_run_id)
			VALUES ($1, 'build', $2, $3, 'staged-output-migration', 'alice',
			        'partial-plan', '1', 'task', 'partial', 'partial', $4, $5)
			RETURNING id
		`, failedID, failedRun, teamID, definitionID, failedRun).Scan(&failedProductionID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		for _, row := range []struct {
			runID     int64
			direction string
			port      string
			promoted  bool
		}{
			{runID: succeededRun, direction: "input", port: "subject", promoted: true},
			{runID: succeededRun, direction: "output", port: "result", promoted: true},
			{runID: failedRun, direction: "output", port: "partial", promoted: false},
		} {
			var promotedAt sql.NullTime
			Expect(database.QueryRow(`
				SELECT promoted_at FROM agent_workflow_run_snapshots
				WHERE workflow_run_id = $1 AND direction = $2 AND port_name = $3
			`, row.runID, row.direction, row.port).Scan(&promotedAt)).To(Succeed())
			Expect(promotedAt.Valid).To(Equal(row.promoted))
		}

		_, err = database.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'input', 'invalid', $2, NULL)
		`, failedRun, inputID)
		Expect(err).To(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var promotedColumn int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_run_snapshots' AND column_name = 'promoted_at'
		`).Scan(&promotedColumn)).To(Succeed())
		Expect(promotedColumn).To(Equal(0))

		var bindingCount int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = 'partial'
		`, failedRun).Scan(&bindingCount)).To(Succeed())
		Expect(bindingCount).To(Equal(0), "downgrade must not expose an unpromoted output to pre-migration readers")
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1
			  AND (
			    (direction = 'input' AND port_name = 'subject')
			    OR (direction = 'output' AND port_name = 'result')
			  )
		`, succeededRun).Scan(&bindingCount)).To(Succeed())
		Expect(bindingCount).To(Equal(2), "downgrade must preserve admitted inputs and promoted outputs")

		for _, snapshotID := range []int64{inputID, succeededID, failedID} {
			var snapshotCount int
			Expect(database.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE id = $1`, snapshotID).Scan(&snapshotCount)).To(Succeed())
			Expect(snapshotCount).To(Equal(1), "downgrade must preserve snapshot content")
		}
		var productionCount int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_productions
			WHERE id = $1 AND snapshot_id = $2 AND workflow_run_id = $3
		`, failedProductionID, failedID, failedRun).Scan(&productionCount)).To(Succeed())
		Expect(productionCount).To(Equal(1), "downgrade must preserve provenance for the hidden snapshot")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = 'partial'
		`, failedRun).Scan(&bindingCount)).To(Succeed())
		Expect(bindingCount).To(Equal(0), "re-upgrade must not resurrect a discarded partial output binding")
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND promoted_at IS NOT NULL
		`, succeededRun).Scan(&bindingCount)).To(Succeed())
		Expect(bindingCount).To(Equal(2), "re-upgrade must restore promotion metadata for retained bindings")
	})
})
