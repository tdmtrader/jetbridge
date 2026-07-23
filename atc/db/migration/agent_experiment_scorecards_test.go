package migration_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent experiment frozen scorecards migration", func() {
	const beforeVersion, targetVersion = 1773106118, 1773106119
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

	It("adds a shape-checked, terminal-only, immutable scorecard while preserving legacy terminal rows", func() {
		unique := time.Now().UnixNano()
		var teamID int
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("experiment-scorecards-%d", unique)).Scan(&teamID)).To(Succeed())

		insertExperiment := func(name, state string) int64 {
			var id int64
			createdAt := time.Now().Add(-2 * time.Minute)
			startedAt := any(nil)
			completedAt := any(nil)
			targetConfigHash := any(nil)
			if state != "draft" {
				startedAt = time.Now().Add(-time.Minute)
				completedAt = time.Now()
				targetConfigHash = strings.Repeat("a", 64)
			}
			Expect(database.QueryRow(`
				INSERT INTO agent_experiments
					(team_id, team_name, name, state, candidate_signature, repetitions,
					 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
					 evaluator_workflow_version, evaluator_signature,
					 evaluator_target_config_hash, evaluator_measurements_port, created_by,
					 created_at, started_at, completed_at)
				VALUES ($1, $2, $3, $4, '{}', 1, 'workflow', 'judge', 1, 1,
				        '{}', $5, 'measurements', 'alice', $6, $7, $8)
				RETURNING id
			`, teamID, fmt.Sprintf("experiment-scorecards-%d", unique), name, state,
				targetConfigHash, createdAt, startedAt, completedAt).Scan(&id)).To(Succeed())
			return id
		}
		draftID := insertExperiment("draft", "draft")
		legacyTerminalID := insertExperiment("legacy-terminal", "completed")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var legacyPayload []byte
		Expect(database.QueryRow(`
			SELECT frozen_scorecard FROM agent_experiments WHERE id = $1
		`, legacyTerminalID).Scan(&legacyPayload)).To(Succeed())
		Expect(legacyPayload).To(BeNil(), "pre-migration terminal experiments are frozen lazily on their first read")

		validPayload, err := json.Marshal(map[string]any{
			"experiment_id": fmt.Sprint(draftID),
			"control":       "control",
			"variants":      map[string]any{},
			"comparisons":   map[string]any{},
			"cells":         []any{},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_experiments SET frozen_scorecard = $2 WHERE id = $1
		`, draftID, validPayload)
		Expect(err).To(HaveOccurred(), "a running or draft experiment cannot expose a scorecard")

		_, err = database.Exec(`
			UPDATE agent_experiments
			SET state = 'completed', started_at = now(), completed_at = now(),
			    evaluator_target_config_hash = $3, frozen_scorecard = $2
			WHERE id = $1
		`, draftID, validPayload, strings.Repeat("b", 64))
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			UPDATE agent_experiments
			SET frozen_scorecard = jsonb_set(frozen_scorecard, '{control}', '"rewritten"')
			WHERE id = $1
		`, draftID)
		Expect(err).To(MatchError(ContainSubstring("frozen scorecards are immutable")))

		_, err = database.Exec(`
			UPDATE agent_experiments
			SET frozen_scorecard = jsonb_build_object(
				'experiment_id', id::text, 'control', 'control', 'variants', '{}'::jsonb,
				'comparisons', '{}'::jsonb, 'cells', '{}'::jsonb
			)
			WHERE id = $1
		`, legacyTerminalID)
		Expect(err).To(HaveOccurred(), "cells must be a bounded JSON array")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		_, err = database.Exec(`
			UPDATE agent_experiments SET frozen_scorecard = '{}' WHERE id = $1
		`, draftID)
		Expect(err).To(MatchError(ContainSubstring("frozen_scorecard")))
	})
})
