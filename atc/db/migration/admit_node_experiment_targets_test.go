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

var _ = Describe("admit node experiment targets migration", func() {
	const beforeVersion, targetVersion = 1773106158, 1773106159

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
		unique   int
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
		unique = 0
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	columnExists := func(table, column string) bool {
		var count int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		`, table, column).Scan(&count)).To(Succeed())
		return count > 0
	}

	// Every row is seeded through raw SQL so the spec exercises the schema
	// rather than the factory that reads it.
	seedExperiment := func(evaluatorKind string) int64 {
		unique++
		key := fmt.Sprintf("%s-%d", evaluatorKind, unique)
		var teamID int
		Expect(database.QueryRow(
			`INSERT INTO teams (name) VALUES ($1) RETURNING id`, "team-"+key).Scan(&teamID)).To(Succeed())
		var definitionID int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'plan: []', 'alice', 3, 1)
			RETURNING id
		`, "wf-"+key, fmt.Sprintf("%064d", unique)).Scan(&definitionID)).To(Succeed())
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_experiments
				(team_id, team_name, name, candidate_signature, repetitions,
				 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
				 evaluator_workflow_version, evaluator_signature, evaluator_measurements_port,
				 created_by)
			VALUES ($1, $2, $3, '{}'::jsonb, 1, $4, $5, $6, 1, '{}'::jsonb, 'measurements', 'alice')
			RETURNING id
		`, teamID, "team-"+key, "exp-"+key, evaluatorKind, "wf-"+key, definitionID).Scan(&id)).To(Succeed())
		return id
	}

	insertVariant := func(experimentID int64, kind, functionID, parameters string) error {
		unique++
		var function any
		if functionID != "" {
			function = functionID
		}
		_, err := database.Exec(`
			INSERT INTO agent_experiment_variants
				(experiment_id, label, target_kind, workflow_name, definition_id,
				 workflow_version, function_id, signature_hash, node_parameters)
			VALUES ($1, $2, $3, 'graded-node', 1, 1, $4, $5, $6::jsonb)
		`, experimentID, fmt.Sprintf("variant-%d", unique), kind, function,
			strings.Repeat("a", 64), parameters)
		return err
	}

	It("admits the node kind, adds the parameter columns, and rolls back cleanly", func() {
		Expect(columnExists("agent_experiment_variants", "node_parameters")).To(BeFalse())
		Expect(columnExists("agent_experiments", "evaluator_node_parameters")).To(BeFalse())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(columnExists("agent_experiment_variants", "node_parameters")).To(BeTrue())
		Expect(columnExists("agent_experiments", "evaluator_node_parameters")).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(columnExists("agent_experiment_variants", "node_parameters")).To(BeFalse())
		Expect(columnExists("agent_experiments", "evaluator_node_parameters")).To(BeFalse())

		// The down restores the pre-node constraint names, so re-applying is
		// not a one-way door.
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(columnExists("agent_experiment_variants", "node_parameters")).To(BeTrue())
	})

	It("rejects a node variant before the migration and accepts one with parameters after", func() {
		experimentID := seedExperiment("workflow")
		unique++
		_, err := database.Exec(`
			INSERT INTO agent_experiment_variants
				(experiment_id, label, target_kind, workflow_name, definition_id,
				 workflow_version, signature_hash)
			VALUES ($1, $2, 'node', 'graded-node', 1, 1, $3)
		`, experimentID, fmt.Sprintf("pre-%d", unique), strings.Repeat("a", 64))
		// Either CHECK is decisive: the kind list excluded 'node', and the
		// paired function-selection CHECK had no branch a 'node' row could
		// satisfy. PostgreSQL reports whichever it evaluates first.
		Expect(err).To(MatchError(Or(
			ContainSubstring("agent_experiment_variants_target_kind_check"),
			ContainSubstring("agent_experiment_variants_check"),
		)))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(insertVariant(experimentID, "node", "", `{"MINIMUM_SEVERITY": "high"}`)).To(Succeed())
		var stored string
		Expect(database.QueryRow(`
			SELECT node_parameters::text FROM agent_experiment_variants
			WHERE experiment_id = $1 AND target_kind = 'node'
		`, experimentID).Scan(&stored)).To(Succeed())
		Expect(stored).To(ContainSubstring("MINIMUM_SEVERITY"))
	})

	It("requires a node variant to select no function", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		experimentID := seedExperiment("workflow")

		Expect(insertVariant(experimentID, "node", "review", "{}")).To(
			MatchError(ContainSubstring("agent_experiment_variants_function_selection_check")),
		)
		Expect(insertVariant(experimentID, "workflow", "review", "{}")).To(
			MatchError(ContainSubstring("agent_experiment_variants_function_selection_check")),
		)
		Expect(insertVariant(experimentID, "function", "", "{}")).To(
			MatchError(ContainSubstring("agent_experiment_variants_function_selection_check")),
		)
		Expect(insertVariant(experimentID, "function", "review", "{}")).To(Succeed())
	})

	It("refuses node parameters on a target that has no parameter surface", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		experimentID := seedExperiment("workflow")

		Expect(insertVariant(experimentID, "workflow", "", `{"MODE": "strict"}`)).To(
			MatchError(ContainSubstring("agent_experiment_variants_node_parameters_check")),
		)
		Expect(insertVariant(experimentID, "function", "review", `{"MODE": "strict"}`)).To(
			MatchError(ContainSubstring("agent_experiment_variants_node_parameters_check")),
		)
		Expect(insertVariant(experimentID, "workflow", "", "{}")).To(Succeed())
	})

	It("keeps node parameters a string map", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		experimentID := seedExperiment("workflow")

		Expect(insertVariant(experimentID, "node", "", `{"MAX_TURNS": 12}`)).To(
			MatchError(ContainSubstring("agent_experiment_variants_node_parameters_check")),
		)
		Expect(insertVariant(experimentID, "node", "", `["MAX_TURNS"]`)).To(
			MatchError(ContainSubstring("agent_experiment_variants_node_parameters_check")),
		)
		Expect(insertVariant(experimentID, "node", "", `{"MAX_TURNS": "12"}`)).To(Succeed())
	})

	It("applies the same rules to a node evaluator target", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		experimentID := seedExperiment("node")
		_, err := database.Exec(`
			UPDATE agent_experiments SET evaluator_node_parameters = $2::jsonb WHERE id = $1
		`, experimentID, `{"MODE": "strict"}`)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			UPDATE agent_experiments SET evaluator_function_id = 'judge' WHERE id = $1
		`, experimentID)
		Expect(err).To(MatchError(
			ContainSubstring("agent_experiments_evaluator_function_selection_check")))

		workflowExperiment := seedExperiment("workflow")
		_, err = database.Exec(`
			UPDATE agent_experiments SET evaluator_node_parameters = $2::jsonb WHERE id = $1
		`, workflowExperiment, `{"MODE": "strict"}`)
		Expect(err).To(MatchError(
			ContainSubstring("agent_experiments_evaluator_node_parameters_check")))
	})

	It("refuses to roll back over durable node target rows", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		experimentID := seedExperiment("workflow")
		Expect(insertVariant(experimentID, "node", "", `{"MODE": "strict"}`)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(
			MatchError(ContainSubstring("node target rows exist")))
	})
})
