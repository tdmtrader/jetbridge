package migration_test

import (
	"database/sql"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent experiments migration", func() {
	const beforeVersion, targetVersion = 1773106110, 1773106111
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

	It("creates the normalized immutable experiment matrix and round-trips without deleting dependencies", func() {
		var teamID, definitionID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('experiment-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('experiment-target', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

		var snapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots (type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("b", 64)).Scan(&snapshotID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'alice', 'experiment fixture')
		`, snapshotID, teamID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var experimentID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_experiments
				(team_id, team_name, name, state, candidate_signature, repetitions,
				 per_cell_budget_usd, total_budget_usd, max_tokens_per_cell,
				 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
				 evaluator_workflow_version, evaluator_signature, evaluator_measurements_port,
				 created_by)
			VALUES ($1, 'experiment-migration', 'compare-reviewers', 'draft',
			        '{"inputs":[{"name":"repo","type":"repository/v1","optional":false}],"outputs":[{"name":"review","type":"review/v1","optional":false}]}',
			        2, 1.25, 10, 10000, 'workflow', 'judge', $2, 1,
			        '{"inputs":[{"name":"candidate","type":"review/v1","optional":false}],"outputs":[{"name":"measurements","type":"measurements/v1","optional":false}]}',
			        'measurements', 'alice')
			RETURNING id
		`, teamID, definitionID).Scan(&experimentID)).To(Succeed())

		var variantID, fixtureID, claimID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_variants
				(experiment_id, label, is_control, target_kind, workflow_name,
				 definition_id, workflow_version, signature_hash)
			VALUES ($1, 'control', true, 'workflow', 'experiment-target', $2, 1, $3)
			RETURNING id
		`, experimentID, definitionID, strings.Repeat("c", 64)).Scan(&variantID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_fixtures (experiment_id, label, role)
			VALUES ($1, 'small-repo', 'normal') RETURNING id
		`, experimentID).Scan(&fixtureID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, actor, reason)
			VALUES ($1, $2, 'fixture', $3, 'experiment fixture binding')
			RETURNING id
		`, snapshotID, teamID, "experiment:"+decimal(experimentID)+":fixture:"+decimal(fixtureID)+":port:repo").Scan(&claimID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_experiment_fixture_bindings
				(fixture_id, port_name, snapshot_id, retention_claim_id)
			VALUES ($1, 'repo', $2, $3)
		`, fixtureID, snapshotID, claimID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_experiment_evaluator_mappings
				(experiment_id, evaluator_port, source_direction, source_port)
			VALUES ($1, 'candidate', 'candidate_output', 'review')
		`, experimentID)
		Expect(err).NotTo(HaveOccurred())

		var cellID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, 1, 'pending') RETURNING id
		`, experimentID, fixtureID, variantID).Scan(&cellID)).To(Succeed())
		_, err = database.Exec(`
			UPDATE agent_experiment_cells SET candidate_workflow_run_id = 999999 WHERE id = $1
		`, cellID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_experiment_evaluations (cell_id, evaluator_workflow_run_id)
			VALUES ($1, 999999)
		`, cellID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_experiment_evaluations
				(cell_id, status, measurements, completed_at)
			VALUES ($1, 'valid_measurement',
			        '{"schema_version":"1.0.0","evaluator_version":"judge-v1","valid":true,"metrics":[{"name":"quality","value":1,"unit":"score","direction":"higher"}]}',
			        now())
		`, cellID)
		Expect(err).NotTo(HaveOccurred())

		By("enforcing one cell for each fixture, variant, and repetition")
		_, err = database.Exec(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, 1, 'pending')
		`, experimentID, fixtureID, variantID)
		Expect(err).To(HaveOccurred())

		By("enforcing target-kind and assertion threshold unions")
		_, err = database.Exec(`
			INSERT INTO agent_experiment_variants
				(experiment_id, label, is_control, target_kind, workflow_name,
				 definition_id, workflow_version, function_id, signature_hash)
			VALUES ($1, 'invalid', false, 'workflow', 'experiment-target', $2, 1, 'node', $3)
		`, experimentID, definitionID, strings.Repeat("d", 64))
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_experiment_control_assertions
				(fixture_id, metric_name, comparator, threshold_one, threshold_two)
			VALUES ($1, 'quality', 'lt', 1, 2)
		`, fixtureID)
		Expect(err).To(HaveOccurred())

		By("requiring canonical rendered-config identities before start")
		_, err = database.Exec(`
			UPDATE agent_experiment_variants SET target_config_hash = 'mutable-tag' WHERE id = $1
		`, variantID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiments SET state = 'running', started_at = now() WHERE id = $1
		`, experimentID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiment_variants SET target_config_hash = $2 WHERE id = $1
		`, variantID, strings.Repeat("e", 64))
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiments
			SET state = 'running', started_at = now(), evaluator_target_config_hash = $2
			WHERE id = $1
		`, experimentID, strings.Repeat("f", 64))
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for table, id := range map[string]int64{
			"agent_workflow_definitions": int64(definitionID),
			"agent_snapshots":            snapshotID,
		} {
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1), table)
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_experiments`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})
})

func decimal(value int64) string {
	return strconv.FormatInt(value, 10)
}
