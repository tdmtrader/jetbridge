package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("reusable node definition migration", func() {
	const beforeVersion, targetVersion = 1773106148, 1773106149

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
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
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("separates workflow and node versions while preserving the workflow rollback contract", func() {
		var workflowDefinitionID int
		err := database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('code-review', 1, 'workflow-v1', 'schema_version: 3', 3, 1)
			RETURNING id
		`).Scan(&workflowDefinitionID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var kind string
		var count int
		Expect(database.QueryRow(`
			SELECT definition_kind, count(*)
			FROM agent_workflow_definitions
			GROUP BY definition_kind
		`).Scan(&kind, &count)).To(Succeed())
		Expect(kind).To(Equal("workflow"))
		Expect(count).To(Equal(1))

		var nodeDefinitionID int
		err = database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('node', 'code-review', 1, 'node-v1', 'schema_version: 1', 3, 1)
			RETURNING id
		`).Scan(&nodeDefinitionID)
		Expect(err).NotTo(HaveOccurred())

		insertRun := func(kind, key string, definitionID int, retryID any) (int64, error) {
			var runID int64
			err := database.QueryRow(`
				INSERT INTO agent_workflow_runs
					(definition_kind, team_id, team_name, workflow_definition_id,
					 workflow_name, workflow_version, schema_version, signature_version,
					 definition_content_hash, idempotency_key, parameterized_config,
					 parameterized_config_hash, origin_kind, origin_reference, created_by,
					 status, retry_of_workflow_run_id)
				VALUES
					($1, 1, 'main', $2, 'code-review', 1, 3, 1,
					 $3, $4, '{}', 'config-hash', 'migration-test', '', 'tester',
					 'admitting', $5)
				RETURNING id
			`, kind, definitionID, kind+"-hash", key, retryID).Scan(&runID)
			return runID, err
		}

		workflowRunID, err := insertRun("workflow", "same-key", workflowDefinitionID, nil)
		Expect(err).NotTo(HaveOccurred())
		nodeRunID, err := insertRun("node", "same-key", nodeDefinitionID, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeRunID).NotTo(Equal(workflowRunID))
		mutationTx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = mutationTx.Exec(`
			UPDATE agent_workflow_runs
			SET definition_kind = 'workflow', idempotency_key = 'kind-mutation'
			WHERE id = $1
		`, nodeRunID)
		Expect(mutationTx.Rollback()).To(Succeed())
		Expect(err).To(HaveOccurred())

		start := make(chan struct{})
		kindUpdateErr := make(chan error, 1)
		type retryResult struct {
			id  int64
			err error
		}
		concurrentRetry := make(chan retryResult, 1)
		go func() {
			<-start
			_, updateErr := database.Exec(`
				UPDATE agent_workflow_runs
				SET definition_kind = 'workflow', idempotency_key = 'concurrent-kind-mutation'
				WHERE id = $1
			`, nodeRunID)
			kindUpdateErr <- updateErr
		}()
		go func() {
			<-start
			id, retryErr := insertRun(
				"node",
				"concurrent-same-kind-retry",
				nodeDefinitionID,
				nodeRunID,
			)
			concurrentRetry <- retryResult{id: id, err: retryErr}
		}()
		close(start)
		Expect(<-kindUpdateErr).To(HaveOccurred())
		concurrent := <-concurrentRetry
		Expect(concurrent.err).NotTo(HaveOccurred())
		Expect(concurrent.id).To(BeNumerically(">", 0))

		_, err = insertRun("workflow", "cross-kind-retry", workflowDefinitionID, nodeRunID)
		Expect(err).To(HaveOccurred())

		nodeRetryID, err := insertRun("node", "same-kind-retry", nodeDefinitionID, nodeRunID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_workflow_runs WHERE id = $1`, nodeRunID)
		Expect(err).NotTo(HaveOccurred())
		var retryID sql.NullInt64
		Expect(database.QueryRow(`
			SELECT retry_of_workflow_run_id FROM agent_workflow_runs WHERE id = $1
		`, nodeRetryID).Scan(&retryID)).To(Succeed())
		Expect(retryID.Valid).To(BeFalse())

		_, err = database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE definition_kind = 'node' AND name = 'code-review' AND version = 1
		`)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`DELETE FROM agent_workflow_runs`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			DELETE FROM agent_workflow_definitions
			WHERE definition_kind = 'node' AND name = 'code-review' AND version = 1
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for _, index := range []string{
			"agent_workflow_definitions_name_version_key",
			"agent_workflow_definitions_hash",
		} {
			var exists bool
			Expect(database.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeTrue(), index)
		}
	})
})
