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

var _ = Describe("review snapshot projection migration", func() {
	const (
		beforeVersion = 1773106104
		targetVersion = 1773106105
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

	It("preserves legacy rows and round-trips disposable snapshot projections", func() {
		var buildID int
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ('review-migration-team') RETURNING id
		`).Scan(&buildID)).To(Succeed())
		teamID := buildID
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id) VALUES ('legacy-build', 'succeeded', $1) RETURNING id
		`, teamID).Scan(&buildID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_reviews (build_id, team_name, repo, commit_sha, review)
			VALUES ($1, 'review-migration-team', 'org/repo', 'legacy', '{}')
		`, buildID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var snapshotID, productionID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('review', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("f", 64)).Scan(&snapshotID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, team_id, team_name, created_by, upload_idempotency_key)
			VALUES ($1, 'upload', $2, 'review-migration-team', 'alice', 'projection') RETURNING id
		`, snapshotID, teamID).Scan(&productionID)).To(Succeed())
		_, err = database.Exec(`
			INSERT INTO agent_reviews
				(build_id, team_name, repo, commit_sha, review, snapshot_id, production_id)
			VALUES (NULL, 'review-migration-team', 'org/repo', 'projected', '{}', $1, $2)
		`, snapshotID, productionID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_feedback
				(repo, commit_sha, finding_id, verdict, reviewer, review_snapshot_id, review_team_id)
			VALUES ('org/repo', 'projected', 'ISS-1', 'accurate', 'alice', $1, $2)
		`, snapshotID, teamID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var legacyCount, projectedCount int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_reviews WHERE commit_sha = 'legacy'`).Scan(&legacyCount)).To(Succeed())
		Expect(database.QueryRow(`SELECT count(*) FROM agent_reviews WHERE commit_sha = 'projected'`).Scan(&projectedCount)).To(Succeed())
		Expect(legacyCount).To(Equal(1))
		Expect(projectedCount).To(Equal(0))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		for table, column := range map[string]string{"agent_reviews": "snapshot_id", "agent_feedback": "review_team_id"} {
			var exists bool
			Expect(database.QueryRow(`
				SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)
			`, table, column).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeTrue())
		}
	})
})
