package migration_test

import (
	"database/sql"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent_feedback snapshot-key migration", func() {
	const (
		beforeVersion = 1773106134
		targetVersion = 1773106135
	)

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

	columnExists := func(column string) bool {
		var exists bool
		ExpectWithOffset(1, database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent_feedback' AND column_name = $1
			)
		`, column).Scan(&exists)).To(Succeed())
		return exists
	}

	It("drops the unreachable repo/commit rows and keeps the snapshot-keyed ones", func() {
		var teamID int
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ('feedback-key-team') RETURNING id
		`).Scan(&teamID)).To(Succeed())

		var snapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("a", 64)).Scan(&snapshotID)).To(Succeed())

		// The legacy shape: repo/commit identity, no review snapshot. Since
		// 1773106132 removed agent_reviews.repo/commit_sha, nothing can produce
		// one of these and no read returns it.
		_, err := database.Exec(`
			INSERT INTO agent_feedback (repo, commit_sha, finding_id, verdict, reviewer)
			VALUES ('org/repo', 'abc123', 'ISS-1', 'accurate', 'alice')
		`)
		Expect(err).NotTo(HaveOccurred())

		_, err = database.Exec(`
			INSERT INTO agent_feedback
				(repo, commit_sha, finding_id, verdict, reviewer, review_snapshot_id, review_team_id)
			VALUES ('', '', 'ISS-2', 'noisy', 'bob', $1, $2)
		`, snapshotID, teamID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var findings []string
		rows, err := database.Query(`SELECT finding_id FROM agent_feedback ORDER BY finding_id`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for rows.Next() {
			var finding string
			Expect(rows.Scan(&finding)).To(Succeed())
			findings = append(findings, finding)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(findings).To(Equal([]string{"ISS-2"}), "only the snapshot-keyed row survives")

		for _, column := range []string{"repo", "commit_sha", "ticket_id"} {
			Expect(columnExists(column)).To(BeFalse(), "agent_feedback.%s must be gone", column)
		}

		// The pair is mandatory, so a row that names no review is rejected by the
		// schema rather than stored under an identity nothing can select on.
		_, err = database.Exec(`
			INSERT INTO agent_feedback (finding_id, verdict, reviewer)
			VALUES ('ISS-3', 'accurate', 'carol')
		`)
		Expect(err).To(HaveOccurred())

		// One identity means one upsert key, now a total unique index.
		_, err = database.Exec(`
			INSERT INTO agent_feedback
				(finding_id, verdict, reviewer, review_snapshot_id, review_team_id)
			VALUES ('ISS-2', 'accurate', 'bob', $1, $2)
		`, snapshotID, teamID)
		Expect(err).To(HaveOccurred())
	})

	It("restores the legacy shape on rollback without resurrecting the deleted rows", func() {
		_, err := database.Exec(`
			INSERT INTO agent_feedback (repo, commit_sha, finding_id, verdict, reviewer)
			VALUES ('org/repo', 'abc123', 'ISS-9', 'accurate', 'alice')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		for _, column := range []string{"repo", "commit_sha", "ticket_id"} {
			Expect(columnExists(column)).To(BeTrue(), "rollback must restore agent_feedback.%s", column)
		}
		var legacyRows int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_feedback WHERE finding_id = 'ISS-9'
		`).Scan(&legacyRows)).To(Succeed())
		Expect(legacyRows).To(Equal(0), "rollback recovers the schema, not the owner-test corpus")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
	})
})
