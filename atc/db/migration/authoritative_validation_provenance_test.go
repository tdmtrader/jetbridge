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

var _ = Describe("authoritative validation provenance migration", func() {
	const beforeVersion, targetVersion = 1773106140, 1773106141

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

	It("upgrades draft and frozen experiment identities without weakening provenance parity", func() {
		teamName := fmt.Sprintf("validation-provenance-%d", time.Now().UnixNano())
		var teamID, draftID, runningID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, teamName).Scan(&teamID)).To(Succeed())
		insertExperiment := func(name, state string, frozen bool) int64 {
			var id int64
			var targetHash any
			if frozen {
				targetHash = strings.Repeat("a", 64)
			}
			if frozen {
				Expect(database.QueryRow(`
					INSERT INTO agent_experiments
						(team_id, team_name, name, state, candidate_signature, repetitions,
						 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
						 evaluator_workflow_version, evaluator_signature, evaluator_target_config_hash,
						 evaluator_measurements_port, created_by, started_at)
					VALUES ($1, $2, $3, $4, '{}', 1, 'workflow', 'judge', 1, 1, '{}', $5, 'measurements', 'alice', now())
					RETURNING id
				`, teamID, teamName, name, state, targetHash).Scan(&id)).To(Succeed())
			} else {
				Expect(database.QueryRow(`
					INSERT INTO agent_experiments
						(team_id, team_name, name, state, candidate_signature, repetitions,
						 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
						 evaluator_workflow_version, evaluator_signature, evaluator_measurements_port, created_by)
					VALUES ($1, $2, $3, $4, '{}', 1, 'workflow', 'judge', 1, 1, '{}', 'measurements', 'alice')
					RETURNING id
				`, teamID, teamName, name, state).Scan(&id)).To(Succeed())
			}
			return id
		}
		draftID = insertExperiment("draft", "draft", false)
		runningID = insertExperiment("running", "running", true)
		for _, row := range []struct {
			id     int64
			frozen bool
		}{{draftID, false}, {runningID, true}} {
			var targetHash any
			if row.frozen {
				targetHash = strings.Repeat("b", 64)
			}
			_, err := database.Exec(`
				INSERT INTO agent_experiment_variants
					(experiment_id, label, is_control, target_kind, workflow_name, definition_id, workflow_version, signature_hash, target_config_hash)
				VALUES ($1, 'control', true, 'workflow', 'candidate', 1, 1, $2, $3)
			`, row.id, strings.Repeat("c", 64), targetHash)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		for _, column := range []struct{ table, name string }{
			{"agent_workflow_runs", "dev_validation_provenance_hash"},
			{"agent_experiment_variants", "dev_validation_provenance_hash"},
			{"agent_experiments", "evaluator_dev_validation_provenance_hash"},
		} {
			var present int
			Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, column.table, column.name).Scan(&present)).To(Succeed())
			Expect(present).To(Equal(1))
		}
		var draftVariant, frozenVariant, draftEvaluator, frozenEvaluator sql.NullString
		Expect(database.QueryRow(`SELECT dev_validation_provenance_hash FROM agent_experiment_variants WHERE experiment_id = $1`, draftID).Scan(&draftVariant)).To(Succeed())
		Expect(database.QueryRow(`SELECT dev_validation_provenance_hash FROM agent_experiment_variants WHERE experiment_id = $1`, runningID).Scan(&frozenVariant)).To(Succeed())
		Expect(database.QueryRow(`SELECT evaluator_dev_validation_provenance_hash FROM agent_experiments WHERE id = $1`, draftID).Scan(&draftEvaluator)).To(Succeed())
		Expect(database.QueryRow(`SELECT evaluator_dev_validation_provenance_hash FROM agent_experiments WHERE id = $1`, runningID).Scan(&frozenEvaluator)).To(Succeed())
		Expect(draftVariant.Valid).To(BeFalse())
		Expect(draftEvaluator.Valid).To(BeFalse())
		Expect(frozenVariant.String).To(Equal(""))
		Expect(frozenEvaluator.String).To(Equal(""))
		_, err := database.Exec(`UPDATE agent_experiment_variants SET dev_validation_provenance_hash = $1 WHERE experiment_id = $2`, strings.Repeat("A", 64), runningID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_experiment_variants SET dev_validation_provenance_hash = NULL WHERE experiment_id = $1`, runningID)
		Expect(err).To(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for _, column := range []struct{ table, name string }{{"agent_workflow_runs", "dev_validation_provenance_hash"}, {"agent_experiment_variants", "dev_validation_provenance_hash"}, {"agent_experiments", "evaluator_dev_validation_provenance_hash"}} {
			var present int
			Expect(database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, column.table, column.name).Scan(&present)).To(Succeed())
			Expect(present).To(Equal(0))
		}
	})
})
