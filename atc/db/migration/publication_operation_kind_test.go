package migration_test

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("publication operation kind migration", func() {
	const beforeVersion, targetVersion = 1773106152, 1773106153

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
		teamID   int
		defID    int
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

		unique := GinkgoRandomSeed()
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("publication-kind-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("publication-kind-%d", unique), strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("locks publication writers before inspecting the lossy downgrade gate", func() {
		down, err := os.ReadFile(
			"migrations/1773106153_add_publication_operation_kind.down.sql",
		)
		Expect(err).NotTo(HaveOccurred())
		lockAt := strings.Index(
			string(down),
			"LOCK TABLE agent_publications IN ACCESS EXCLUSIVE MODE",
		)
		checkAt := strings.Index(string(down), "IF EXISTS")
		alterAt := strings.Index(string(down), "ALTER TABLE agent_publications")
		Expect(lockAt).To(BeNumerically(">=", 0))
		Expect(checkAt).To(BeNumerically(">", lockAt))
		Expect(alterAt).To(BeNumerically(">", checkAt))
	})

	It("keeps legacy identity null and byte-stable, closes the PR union, and refuses a lossy downgrade", func() {
		_, occurrenceID, _ := insertPRBindingMigrationEvidence(
			database, teamID, defID, "operation-kind",
		)
		var publicationID int64
		var operationKey string
		Expect(database.QueryRow(`
			SELECT publication_id, publication.operation_key
			FROM agent_publication_occurrences occurrence
			JOIN agent_publications publication ON publication.id=occurrence.publication_id
			WHERE occurrence.id=$1
		`, occurrenceID).Scan(&publicationID, &operationKey)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var kind, payload sql.NullString
		var migratedKey string
		Expect(database.QueryRow(`
			SELECT operation_kind, operation_payload::text, operation_key
			FROM agent_publications
			WHERE id=$1
		`, publicationID).Scan(&kind, &payload, &migratedKey)).To(Succeed())
		Expect(kind.Valid).To(BeFalse())
		Expect(payload.Valid).To(BeFalse())
		Expect(migratedKey).To(Equal(operationKey))

		_, err := database.Exec(`
			UPDATE agent_publications
			SET operation_kind='create_pr'
			WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "a kind without an exact payload must fail closed")

		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_payload='{}'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "a payload without a kind must fail closed")

		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind='unknown', operation_payload='{}'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "the durable operation union must remain closed")

		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind='create_pr', operation_payload='[]'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "the exact action payload must be an object")

		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind='create_pr',
			    operation_payload='{"kind":"create_pr","pull_request":{}}'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).NotTo(Succeed(),
			"an older binary cannot safely interpret a provider-native operation")

		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind=NULL, operation_payload=NULL
			WHERE id=$1
		`, publicationID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var downgradedKey string
		Expect(database.QueryRow(`
			SELECT operation_key FROM agent_publications WHERE id=$1
		`, publicationID).Scan(&downgradedKey)).To(Succeed())
		Expect(downgradedKey).To(Equal(operationKey))
	})
})
