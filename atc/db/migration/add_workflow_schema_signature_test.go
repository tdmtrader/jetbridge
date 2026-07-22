package migration_test

import (
	"database/sql"
	"encoding/json"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("workflow schema signature migration", func() {
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
		Expect(migrator.Migrate(nil, nil, 1773106100)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("backfills real raw and manifest definitions and installs constraints and ordered indexes", func() {
		v1 := `schema_version: 1
name: migrated-v1
prompts:
  work: work
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`
		v2Manifest := workflow.Manifest{
			"workflow.yml": `schema_version: 2
name: migrated-v2
prompt_files:
  work: prompts/work.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
			"prompts/work.md": "work from manifest",
		}
		manifestJSON, err := json.Marshal(v2Manifest)
		Expect(err).NotTo(HaveOccurred())
		v3 := `schema_version: 3
name: migrated-v3
signature_version: 9
inputs: []
outputs: []
plan:
- agent: work
  function_id: work
  prompt: work
`
		_, err = database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, source_manifest)
			VALUES
				('migrated-v1', 1, 'v1', $1, NULL),
				('migrated-v2', 1, 'v2', $2, $3::jsonb),
				('migrated-v3', 1, 'v3', $4, NULL)
		`, v1, v2Manifest["workflow.yml"], manifestJSON, v3)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, 1773106101)).To(Succeed())

		rows, err := database.Query(`
			SELECT name, schema_version, signature_version
			FROM agent_workflow_definitions ORDER BY name`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		type metadata struct {
			name      string
			schema    int
			signature int
		}
		got := []metadata{}
		for rows.Next() {
			var value metadata
			Expect(rows.Scan(&value.name, &value.schema, &value.signature)).To(Succeed())
			got = append(got, value)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal([]metadata{
			{"migrated-v1", 1, 0},
			{"migrated-v2", 2, 0},
			{"migrated-v3", 3, 9},
		}))

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

		_, err = database.Exec(`UPDATE agent_workflow_definitions SET signature_version = -1 WHERE name = 'migrated-v1'`)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_workflow_definitions SET signature_version = 1 WHERE name = 'migrated-v1'`)
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("rolls the complete migration back for every corrupt stored-source category", func(name, definition string, manifest any) {
		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, source_manifest)
			VALUES ($1, 1, $2, $3, $4::jsonb)`, name, name+"-hash", definition, manifest)
		Expect(err).NotTo(HaveOccurred())

		migrationErr := migrator.Migrate(nil, nil, 1773106101)
		Expect(migrationErr).To(HaveOccurred())
		Expect(migrationErr.Error()).To(ContainSubstring(name))

		var count int
		Expect(database.QueryRow(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_definitions'
			  AND column_name IN ('schema_version', 'signature_version')`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	},
		Entry("malformed raw YAML", "broken-raw", "schema_version: 1\nname: broken-raw\nsteps: []\n", nil),
		Entry("malformed manifest shape", "broken-shape", "ignored", `{"workflow.yml":{"nested":"not a string"}}`),
		Entry("manifest missing workflow.yml", "missing-workflow", "ignored", `{"notes.txt":"nothing"}`),
		Entry("unresolved schema-2 asset", "missing-asset", `schema_version: 2
name: missing-asset
prompt_files:
  work: prompts/missing.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`, `{"workflow.yml":"schema_version: 2\nname: missing-asset\nprompt_files:\n  work: prompts/missing.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n"}`),
		Entry("stored-name mismatch", "stored-name", `schema_version: 1
name: compiled-name
prompts: {work: work}
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`, nil),
	)

	It("rejects a preexisting incompatible positive-signature group atomically", func() {
		first := `schema_version: 3
name: duplicate-signature
signature_version: 1
inputs:
- {name: before, type: repository/v1}
- {name: after, type: repository/v1}
outputs: []
plan:
- agent: work
  function_id: work
  prompt: work
`
		second := `schema_version: 3
name: duplicate-signature
signature_version: 1
inputs:
- {name: after, type: repository/v1}
- {name: before, type: repository/v1}
outputs: []
plan:
- agent: work
  function_id: work
  prompt: changed
`
		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition)
			VALUES
				('duplicate-signature', 1, 'first', $1),
				('duplicate-signature', 2, 'second', $2)`, first, second)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, 1773106101)).NotTo(Succeed())
		var count int
		Expect(database.QueryRow(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'agent_workflow_definitions'
			  AND column_name IN ('schema_version', 'signature_version')`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(0))
	})

	It("drops only metadata on down and deterministically rederives it on up", func() {
		v1 := `schema_version: 1
name: round-trip
prompts: {work: work}
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`
		_, err := database.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition)
			VALUES ('round-trip', 1, 'round-trip', $1)`, v1)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, 1773106101)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, 1773106100)).To(Succeed())

		var definition string
		Expect(database.QueryRow(`SELECT definition FROM agent_workflow_definitions WHERE name = 'round-trip'`).Scan(&definition)).To(Succeed())
		Expect(definition).To(Equal(v1))
		var runsTable bool
		Expect(database.QueryRow(`SELECT to_regclass('agent_workflow_runs') IS NOT NULL`).Scan(&runsTable)).To(Succeed())
		Expect(runsTable).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, 1773106101)).To(Succeed())
		var schema, signature int
		Expect(database.QueryRow(`SELECT schema_version, signature_version FROM agent_workflow_definitions WHERE name = 'round-trip'`).Scan(&schema, &signature)).To(Succeed())
		Expect(schema).To(Equal(1))
		Expect(signature).To(Equal(0))
	})
})
