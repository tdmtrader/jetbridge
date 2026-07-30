package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("workflow node bindings migration", func() {
	const beforeVersion, targetVersion = 1773106149, 1773106150

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

	It("binds complete immutable node identities only to workflow definitions", func() {
		var workflowID, nodeID int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('workflow', 'consumer', 1, 'workflow-hash', 'schema_version: 3', 3, 1)
			RETURNING id`).Scan(&workflowID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('node', 'review', 2, 'node-hash', 'schema_version: 1', 3, 1)
			RETURNING id`).Scan(&nodeID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		insert := func(instance string, workflowDefinitionID, nodeDefinitionID int, nodeHash string) error {
			_, err := database.Exec(`
				INSERT INTO agent_workflow_node_bindings
					(workflow_definition_id, instance_name, node_definition_id, node_name, node_version,
					 node_content_hash, input_mapping, output_mapping, parameters)
				VALUES ($1, $2, $3, 'review', 2, $4, '{}', '{}', '{}')`,
				workflowDefinitionID, instance, nodeDefinitionID, nodeHash)
			return err
		}

		Expect(insert("review-change", workflowID, nodeID, "node-hash")).To(Succeed())
		Expect(insert("workflow-is-node", nodeID, nodeID, "node-hash")).To(HaveOccurred(), "workflow FK must reject node kind")
		Expect(insert("node-is-workflow", workflowID, workflowID, "workflow-hash")).To(HaveOccurred(), "node FK must reject workflow kind")
		Expect(insert("hash-mismatch", workflowID, nodeID, "other-hash")).To(HaveOccurred(), "node FK must prove full immutable identity")
		_, err := database.Exec(`
			INSERT INTO agent_workflow_node_bindings
				(workflow_definition_id, workflow_definition_kind, instance_name, node_definition_id,
				 node_name, node_version, node_content_hash, input_mapping, output_mapping, parameters)
			VALUES ($1, 'node', 'wrong-kind', $2, 'review', 2, 'node-hash', '{}', '{}', '{}')`, workflowID, nodeID)
		Expect(err).To(HaveOccurred(), "literal workflow kind must not be caller-selectable")
	})
})
