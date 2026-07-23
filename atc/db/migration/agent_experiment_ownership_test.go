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

var _ = Describe("agent experiment relational ownership migration", func() {
	const beforeVersion, targetVersion = 1773106119, 1773106120

	type experimentRows struct {
		experimentID int64
		fixtureID    int64
		variantID    int64
	}

	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator
	var first, second experimentRows

	insertExperimentRows := func(teamID int, teamName, name string) experimentRows {
		var rows experimentRows
		Expect(database.QueryRow(`
			INSERT INTO agent_experiments
				(team_id, team_name, name, state, candidate_signature, repetitions,
				 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
				 evaluator_workflow_version, evaluator_signature,
				 evaluator_measurements_port, created_by)
			VALUES ($1, $2, $3, 'draft', '{}', 3, 'workflow', 'judge', 1, 1,
			        '{}', 'measurements', 'alice')
			RETURNING id
		`, teamID, teamName, name).Scan(&rows.experimentID)).To(Succeed())

		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_fixtures (experiment_id, label, role)
			VALUES ($1, $2, 'normal')
			RETURNING id
		`, rows.experimentID, name+"-fixture").Scan(&rows.fixtureID)).To(Succeed())

		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_variants
				(experiment_id, label, is_control, target_kind, workflow_name,
				 definition_id, workflow_version, signature_hash)
			VALUES ($1, $2, false, 'workflow', 'candidate', 1, 1, $3)
			RETURNING id
		`, rows.experimentID, name+"-variant", strings.Repeat("a", 64)).Scan(&rows.variantID)).To(Succeed())

		return rows
	}

	insertCell := func(experimentID, fixtureID, variantID int64, repetition int) int64 {
		var cellID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, $4, 'pending')
			RETURNING id
		`, experimentID, fixtureID, variantID, repetition).Scan(&cellID)).To(Succeed())
		return cellID
	}

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

		unique := time.Now().UnixNano()
		teamName := fmt.Sprintf("experiment-ownership-%d", unique)
		var teamID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName).Scan(&teamID)).To(Succeed())
		first = insertExperimentRows(teamID, teamName, "first")
		second = insertExperimentRows(teamID, teamName, "second")
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("refuses to reinterpret a legacy cell whose fixture belongs to another experiment", func() {
		corruptCellID := insertCell(first.experimentID, second.fixtureID, first.variantID, 1)

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("cell fixture belongs to another experiment")))

		var count int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_experiment_cells WHERE id = $1
		`, corruptCellID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1), "a failed migration must leave historical evidence untouched")
	})

	It("refuses to reinterpret a legacy cell whose variant belongs to another experiment", func() {
		corruptCellID := insertCell(first.experimentID, first.fixtureID, second.variantID, 1)

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("cell variant belongs to another experiment")))

		var count int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_experiment_cells WHERE id = $1
		`, corruptCellID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1), "a failed migration must leave historical evidence untouched")
	})

	It("refuses to reinterpret a legacy reservation owned by another experiment", func() {
		cellID := insertCell(first.experimentID, first.fixtureID, first.variantID, 1)
		_, err := database.Exec(`
			INSERT INTO agent_experiment_budget_reservations
				(cell_id, experiment_id, reserved_usd, max_tokens, state, budget_day)
			VALUES ($1, $2, 1, 100, 'active', CURRENT_DATE)
		`, cellID, second.experimentID)
		Expect(err).NotTo(HaveOccurred(), "the pre-migration schema permits the mismatch")

		err = migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("budget reservation belongs to another experiment than its cell")))

		var count int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_experiment_budget_reservations WHERE cell_id = $1
		`, cellID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1), "a failed migration must leave historical evidence untouched")
	})

	It("enforces same-experiment ownership on inserts and updates and downgrades safely", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		validCellID := insertCell(first.experimentID, first.fixtureID, first.variantID, 1)

		_, err := database.Exec(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, 2, 'pending')
		`, first.experimentID, second.fixtureID, first.variantID)
		Expect(err).To(HaveOccurred(), "a cell cannot borrow another experiment's fixture")

		_, err = database.Exec(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, 2, 'pending')
		`, first.experimentID, first.fixtureID, second.variantID)
		Expect(err).To(HaveOccurred(), "a cell cannot borrow another experiment's variant")

		_, err = database.Exec(`
			UPDATE agent_experiment_cells SET fixture_id = $2 WHERE id = $1
		`, validCellID, second.fixtureID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiment_cells SET variant_id = $2 WHERE id = $1
		`, validCellID, second.variantID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiment_cells SET experiment_id = $2 WHERE id = $1
		`, validCellID, second.experimentID)
		Expect(err).To(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_experiment_budget_reservations
				(cell_id, experiment_id, reserved_usd, max_tokens, state, budget_day)
			VALUES ($1, $2, 1, 100, 'active', CURRENT_DATE)
		`, validCellID, first.experimentID)
		Expect(err).NotTo(HaveOccurred())

		secondCellID := insertCell(first.experimentID, first.fixtureID, first.variantID, 2)
		_, err = database.Exec(`
			INSERT INTO agent_experiment_budget_reservations
				(cell_id, experiment_id, reserved_usd, max_tokens, state, budget_day)
			VALUES ($1, $2, 1, 100, 'active', CURRENT_DATE)
		`, secondCellID, second.experimentID)
		Expect(err).To(HaveOccurred(), "a reservation cannot claim a cell from another experiment")

		_, err = database.Exec(`
			UPDATE agent_experiment_budget_reservations SET experiment_id = $2 WHERE cell_id = $1
		`, validCellID, second.experimentID)
		Expect(err).To(HaveOccurred())

		_, err = database.Exec(`
			UPDATE agent_experiment_fixtures SET experiment_id = $2 WHERE id = $1
		`, first.fixtureID, second.experimentID)
		Expect(err).To(HaveOccurred(), "a referenced fixture cannot be re-parented")
		_, err = database.Exec(`
			UPDATE agent_experiment_variants SET experiment_id = $2 WHERE id = $1
		`, first.variantID, second.experimentID)
		Expect(err).To(HaveOccurred(), "a referenced variant cannot be re-parented")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var ownerConstraintCount int
		Expect(database.QueryRow(`
			SELECT count(*)
			FROM pg_constraint
			WHERE conname IN (
				'agent_experiment_fixtures_owner_key',
				'agent_experiment_variants_owner_key',
				'agent_experiment_cells_owner_key',
				'agent_experiment_cells_fixture_owner_fkey',
				'agent_experiment_cells_variant_owner_fkey',
				'agent_experiment_budget_reservations_cell_owner_fkey'
			)
		`).Scan(&ownerConstraintCount)).To(Succeed())
		Expect(ownerConstraintCount).To(Equal(0))

		_, err = database.Exec(`
			INSERT INTO agent_experiment_cells
				(experiment_id, fixture_id, variant_id, repetition, status)
			VALUES ($1, $2, $3, 3, 'pending')
		`, first.experimentID, second.fixtureID, first.variantID)
		Expect(err).NotTo(HaveOccurred(), "downgrade must restore the prior independent-reference schema")
	})
})
