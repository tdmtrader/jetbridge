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

var _ = Describe("PR binding publication authority migration", func() {
	const beforeVersion, targetVersion = 1773106153, 1773106154

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
		`, fmt.Sprintf("binding-authority-%d", unique)).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition,
				 created_by, schema_version, signature_version)
			VALUES ('workflow', $1, 3, $2, 'schema_version: 3', 'test', 3, 1)
			RETURNING id
		`, fmt.Sprintf("binding-authority-%d", unique),
			strings.Repeat("a", 64)).Scan(&defID)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("locks binding writers before both irreversible row gates", func() {
		for _, name := range []string{
			"1773106154_persist_pr_binding_publication_authority.up.sql",
			"1773106154_persist_pr_binding_publication_authority.down.sql",
		} {
			body, err := os.ReadFile("migrations/" + name)
			Expect(err).NotTo(HaveOccurred())
			lockAt := strings.Index(
				string(body),
				"LOCK TABLE agent_pr_bindings IN ACCESS EXCLUSIVE MODE",
			)
			checkAt := strings.Index(string(body), "IF EXISTS")
			alterAt := strings.Index(string(body), "ALTER TABLE agent_pr_bindings")
			Expect(lockAt).To(BeNumerically(">=", 0))
			Expect(checkAt).To(BeNumerically(">", lockAt))
			Expect(alterAt).To(BeNumerically(">", checkAt))
		}
	})

	It("adds only explicit same-team authority columns to an empty table", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		for _, column := range []string{
			"destination",
			"approval_policy_version",
			"creation_publication_occurrence_id",
			"approved_baseline_repository_snapshot_id",
			"approved_baseline_validation_snapshot_id",
			"approved_baseline_publication_occurrence_id",
		} {
			var nullable string
			Expect(database.QueryRow(`
				SELECT is_nullable
				FROM information_schema.columns
				WHERE table_schema=current_schema()
				  AND table_name='agent_pr_bindings'
				  AND column_name=$1
			`, column).Scan(&nullable)).To(Succeed())
			Expect(nullable).To(Equal("NO"), column)
		}

		for _, constraint := range []string{
			"agent_pr_bindings_originating_run_team_fkey",
			"agent_pr_bindings_originating_occurrence_run_team_fkey",
			"agent_pr_bindings_creation_occurrence_run_team_fkey",
			"agent_pr_bindings_baseline_repository_team_fkey",
			"agent_pr_bindings_baseline_validation_team_fkey",
			"agent_pr_bindings_baseline_occurrence_team_fkey",
		} {
			var present bool
			Expect(database.QueryRow(`
				SELECT EXISTS (
					SELECT 1
					FROM pg_constraint
					WHERE conrelid='agent_pr_bindings'::regclass
					  AND conname=$1
				)
			`, constraint).Scan(&present)).To(Succeed())
			Expect(present).To(BeTrue(), constraint)
		}

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
	})

	It("refuses to infer publication authority for any legacy binding", func() {
		runID, occurrenceID, observationID :=
			insertPRBindingMigrationEvidence(
				database, teamID, defID, "binding-authority-legacy",
			)
		_, err := database.Exec(`
			INSERT INTO agent_pr_bindings
				(team_id, provider, repository, external_id, url,
				 source_ref, target_ref,
				 originating_workflow_run_id,
				 originating_publication_occurrence_id,
				 monitor_workflow_definition_id, monitor_workflow_version,
				 acknowledged_cursor, last_observation_snapshot_id,
				 last_reconciled_source_sha, last_reconciled_target_sha,
				 last_reconciled_at)
			VALUES ($1, 'github', 'example/repository', '118',
			        'https://github.example/example/repository/pull/118',
			        'refs/heads/feature/pr', 'refs/heads/main',
			        $2, $3, $4, 3, to_jsonb('initial'::text), $5,
			        $6, $7, now())
		`, teamID, runID, occurrenceID, defID, observationID,
			strings.Repeat("b", 40), strings.Repeat("c", 40))
		Expect(err).NotTo(HaveOccurred())

		err = migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"cannot add PR binding publication authority while legacy bindings exist",
		))
	})
})
