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
		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('code-review', 1, 'workflow-v1', 'schema_version: 3', 3, 1)
		`)
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

		_, err = database.Exec(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, schema_version, signature_version)
			VALUES ('node', 'code-review', 1, 'node-v1', 'schema_version: 1', 3, 1)
		`)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			UPDATE agent_workflow_definitions
			SET live = true
			WHERE definition_kind = 'node' AND name = 'code-review' AND version = 1
		`)
		Expect(err).To(HaveOccurred())
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
