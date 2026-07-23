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

var _ = Describe("workflow outcomes migration", func() {
	const beforeVersion, targetVersion = 1773106106, 1773106107
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

	It("round-trips outcome audit rows without deleting durable runs or snapshots", func() {
		var teamID, definitionID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('workflow-outcome-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('workflow-outcome-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		var runID, outputID, modificationID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'workflow-outcome-migration', $2, 'workflow-outcome-migration', 1,
			        3, 1, $3, 'migration-run', '{}', $4, 'manual', '', 'alice', 'admitting')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("a", 64), strings.Repeat("b", 64)).Scan(&runID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots (type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("c", 64)).Scan(&outputID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots (type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository-change', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("d", 64)).Scan(&modificationID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots (workflow_run_id, direction, port_name, snapshot_id)
			VALUES ($1, 'output', 'review', $2)
		`, runID, outputID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition, publication_state,
				 human_modified, modification_snapshot_id, intervention_count, labels, actor)
			VALUES ($1, $2, $3, 'merged', 'published', true, $4, 1, '["dogfood"]', 'alice')
		`, teamID, runID, outputID, modificationID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for table, id := range map[string]int64{
			"agent_workflow_runs": runID,
			"agent_snapshots":     outputID,
		} {
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1), table)
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_workflow_outcomes`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})
})
