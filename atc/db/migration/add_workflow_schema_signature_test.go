package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("workflow schema signature migration", func() {
	const beforeVersion, targetVersion = 1773106100, 1773106101

	var (
		database    *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		migrator    migration.Migrator
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
		lockFactory = lock.NewLockFactory(lockDB, noop, noop)
		migrator = migration.NewMigrator(database, lockFactory)
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	metadataColumns := func() int {
		var count int
		Expect(database.QueryRow(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_definitions'
			  AND column_name IN ('schema_version', 'signature_version')`).Scan(&count)).To(Succeed())
		return count
	}

	It("adds the metadata columns, their constraints, and the ordered indexes", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(metadataColumns()).To(Equal(2))

		for index, orderedColumns := range map[string]string{
			"agent_workflow_definitions_schema_version":         "(schema_version, name, version DESC)",
			"agent_workflow_definitions_name_signature_version": "(name, signature_version, version DESC)",
		} {
			var definition string
			Expect(database.QueryRow(`
				SELECT indexdef FROM pg_indexes
				WHERE indexname = $1`, index).Scan(&definition)).To(Succeed())
			Expect(definition).To(ContainSubstring(orderedColumns))
		}
	})

	It("requires the metadata columns and enforces the schema/signature pairing", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		insert := func(schemaVersion, signatureVersion any) error {
			_, err := database.Exec(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, schema_version, signature_version)
				VALUES ('paired', 1, 'paired-hash', 'definition', $1, $2)`, schemaVersion, signatureVersion)
			return err
		}

		Expect(insert(nil, 0)).To(HaveOccurred())
		Expect(insert(1, nil)).To(HaveOccurred())
		Expect(insert(0, 0)).To(HaveOccurred())
		Expect(insert(1, -1)).To(HaveOccurred())
		Expect(insert(1, 1)).To(HaveOccurred())
		Expect(insert(3, 0)).To(HaveOccurred())
		Expect(insert(1, 0)).To(Succeed())

		_, err := database.Exec(`UPDATE agent_workflow_definitions SET signature_version = 1 WHERE name = 'paired'`)
		Expect(err).To(HaveOccurred())
	})

	It("drops only the metadata on down and reinstalls it on up", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(metadataColumns()).To(Equal(0))
		var runsTable bool
		Expect(database.QueryRow(`SELECT to_regclass('agent_workflow_runs') IS NOT NULL`).Scan(&runsTable)).To(Succeed())
		Expect(runsTable).To(BeTrue())
		var definitionsTable bool
		Expect(database.QueryRow(`SELECT to_regclass('agent_workflow_definitions') IS NOT NULL`).Scan(&definitionsTable)).To(Succeed())
		Expect(definitionsTable).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(metadataColumns()).To(Equal(2))
	})
})
