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

var _ = Describe("drop publication operation kind migration", func() {
	const beforeVersion, targetVersion = 1773106166, 1773106167

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
		migrator = migration.NewMigrator(
			database, lock.NewLockFactory(lockDB, noop, noop),
		)
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		unique := GinkgoRandomSeed()
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, fmt.Sprintf("drop-operation-kind-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("drop-operation-kind-%d", unique),
			strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	columnExists := func(column string) bool {
		var present bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'agent_publications'
				  AND column_name = $1
			)
		`, column).Scan(&present)).To(Succeed())
		return present
	}

	constraintExists := func() bool {
		var present bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'agent_publications'::regclass
				  AND conname = 'agent_publications_operation_kind_payload_check'
			)
		`).Scan(&present)).To(Succeed())
		return present
	}

	// Returns the id of the agent_publications row the shared evidence helper
	// created. That row is the direct-Git shape: NULL in both discriminator
	// columns, which is what every live publication has always carried.
	insertPublication := func(suffix string) int64 {
		_, occurrenceID, _ := insertPRBindingMigrationEvidence(
			database, teamID, defID, "drop-operation-kind-"+suffix,
		)
		var publicationID int64
		Expect(database.QueryRow(`
			SELECT publication_id FROM agent_publication_occurrences WHERE id=$1
		`, occurrenceID).Scan(&publicationID)).To(Succeed())
		return publicationID
	}

	It("locks publication writers before inspecting the lossy upgrade gate", func() {
		up, err := os.ReadFile(
			"migrations/1773106167_drop_publication_operation_kind.up.sql",
		)
		Expect(err).NotTo(HaveOccurred())
		lockAt := strings.Index(
			string(up),
			"LOCK TABLE agent_publications IN ACCESS EXCLUSIVE MODE",
		)
		checkAt := strings.Index(string(up), "IF EXISTS")
		alterAt := strings.Index(string(up), "ALTER TABLE agent_publications")
		Expect(lockAt).To(BeNumerically(">=", 0))
		Expect(checkAt).To(BeNumerically(">", lockAt))
		Expect(alterAt).To(BeNumerically(">", checkAt))
	})

	It("removes both discriminator columns and the check that paired them", func() {
		Expect(columnExists("operation_kind")).To(BeTrue())
		Expect(columnExists("operation_payload")).To(BeTrue())
		Expect(constraintExists()).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(columnExists("operation_kind")).To(BeFalse())
		Expect(columnExists("operation_payload")).To(BeFalse())
		Expect(constraintExists()).To(BeFalse())
	})

	It("carries a direct-Git publication across losslessly", func() {
		publicationID := insertPublication("legacy")
		var beforeKey, beforeMode, beforeResult string
		Expect(database.QueryRow(`
			SELECT operation_key, mode, result::text
			FROM agent_publications WHERE id=$1
		`, publicationID).Scan(&beforeKey, &beforeMode, &beforeResult)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var afterKey, afterMode, afterResult string
		Expect(database.QueryRow(`
			SELECT operation_key, mode, result::text
			FROM agent_publications WHERE id=$1
		`, publicationID).Scan(&afterKey, &afterMode, &afterResult)).To(Succeed())
		Expect(afterKey).To(Equal(beforeKey))
		Expect(afterMode).To(Equal(beforeMode))
		Expect(afterResult).To(Equal(beforeResult))

		// The occurrence that projects this publication is untouched too: the
		// drop is columns on one table, not a reshaping of the operation.
		var occurrences int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_publication_occurrences WHERE publication_id=$1
		`, publicationID).Scan(&occurrences)).To(Succeed())
		Expect(occurrences).To(Equal(1))
	})

	It("refuses to drop a surviving provider-native operation rather than destroying it", func() {
		publicationID := insertPublication("provider-native")
		_, err := database.Exec(`
			UPDATE agent_publications
			SET operation_kind='create_pr',
			    operation_payload='{"kind":"create_pr","pull_request":{}}'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).NotTo(HaveOccurred())

		err = migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"cannot remove provider-native publication operations while they exist",
		))

		// The refusal is transactional: nothing was half-dropped, and the row
		// the operator still has to decide about is intact.
		Expect(columnExists("operation_kind")).To(BeTrue())
		Expect(columnExists("operation_payload")).To(BeTrue())
		Expect(constraintExists()).To(BeTrue())
		var kind string
		Expect(database.QueryRow(`
			SELECT operation_kind FROM agent_publications WHERE id=$1
		`, publicationID).Scan(&kind)).To(Succeed())
		Expect(kind).To(Equal("create_pr"))

		// Clearing it by hand is the documented remedy, and it unblocks the
		// upgrade -- which is what makes the refusal a gate rather than a wall.
		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind=NULL, operation_payload=NULL
			WHERE id=$1
		`, publicationID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(columnExists("operation_kind")).To(BeFalse())
	})

	It("restores the exact historical shape on rollback, and unwinds below it", func() {
		publicationID := insertPublication("rollback")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(columnExists("operation_kind")).To(BeTrue())
		Expect(columnExists("operation_payload")).To(BeTrue())
		Expect(constraintExists()).To(BeTrue())

		// Restored nullable, and no row was invented: the up migration refuses
		// while any non-NULL row exists, so NULL everywhere is the faithful
		// inverse.
		var total int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_publications
			WHERE operation_kind IS NOT NULL OR operation_payload IS NOT NULL
		`).Scan(&total)).To(Succeed())
		Expect(total).To(Equal(0))

		// The restored CHECK is the point, not its presence in the catalog.
		_, err := database.Exec(`
			UPDATE agent_publications SET operation_kind='create_pr' WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "a kind without an exact payload must fail closed")
		_, err = database.Exec(`
			UPDATE agent_publications SET operation_payload='{}'::jsonb WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "a payload without a kind must fail closed")
		_, err = database.Exec(`
			UPDATE agent_publications
			SET operation_kind='unknown', operation_payload='{}'::jsonb
			WHERE id=$1
		`, publicationID)
		Expect(err).To(HaveOccurred(), "the durable operation union must remain closed")

		// 1773106153's own down drops this constraint by name and then the two
		// columns, so the chain unwinding proves the restore is exact rather
		// than merely column-shaped.
		Expect(migrator.Migrate(nil, nil, 1773106152)).To(Succeed())
		Expect(columnExists("operation_kind")).To(BeFalse())
	})
})
