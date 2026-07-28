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

	// A definition row that predates the metadata columns, as every pre-v3
	// deployment carries. `source` is the standalone definition text; `manifest`
	// is the stored source manifest (nil when the row predates manifests).
	insertLegacyDefinition := func(name, source string, manifest *string) {
		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, live, created_by, source_manifest)
			VALUES ($1, 1, md5($2), $2, false, 'tester', $3)`,
			name, source, manifest)
		Expect(err).NotTo(HaveOccurred())
	}

	schemaOf := func(name string) (int, int) {
		var schemaVersion, signatureVersion int
		Expect(database.QueryRow(`
			SELECT schema_version, signature_version
			FROM agent_workflow_definitions WHERE name = $1`, name).
			Scan(&schemaVersion, &signatureVersion)).To(Succeed())
		return schemaVersion, signatureVersion
	}

	// Regression: the columns land NOT NULL, so pre-existing rows must be
	// backfilled first. Without this the migration fails on every deployment
	// that already holds workflow definitions, while passing on a fresh one.
	It("backfills definitions that predate the metadata columns", func() {
		insertLegacyDefinition("declared-two", "schema_version: 2\nname: declared-two\n", nil)
		insertLegacyDefinition("declared-one", "schema_version: 1\nname: declared-one\n", nil)
		// schema_version was optional originally and defaulted to 1.
		insertLegacyDefinition("undeclared", "name: undeclared\nsteps: []\n", nil)
		// A manifest-backed row keeps its schema in the entry-point file.
		manifest := `{"workflow.yml": "schema_version: 2\nname: manifested\n", "prompts/work.md": "hi"}`
		insertLegacyDefinition("manifested", "", &manifest)

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		schema, signature := schemaOf("declared-two")
		Expect(schema).To(Equal(2))
		Expect(signature).To(BeZero())

		schema, _ = schemaOf("declared-one")
		Expect(schema).To(Equal(1))

		schema, _ = schemaOf("undeclared")
		Expect(schema).To(Equal(1))

		schema, signature = schemaOf("manifested")
		Expect(schema).To(Equal(2))
		Expect(signature).To(BeZero())
	})

	It("refuses a pre-existing definition whose signature it cannot derive", func() {
		// Schema 3 arrives later in the chain, so such a row cannot legitimately
		// exist here; inventing a signature would be worse than failing.
		insertLegacyDefinition("premature-v3", "schema_version: 3\nname: premature-v3\n", nil)

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("schema_version >= 3"))
	})

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
