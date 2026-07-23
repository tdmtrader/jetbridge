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

var _ = Describe("repository-change projection migration", func() {
	const beforeVersion, targetVersion = 1773106105, 1773106106
	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator

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

	It("round-trips disposable bounded projection rows while retaining canonical snapshots", func() {
		var snapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository-change', 1, $1, 10, 2, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("a", 64)).Scan(&snapshotID)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_repository_change_projections
				(snapshot_id, status, repository_id, base_sha, result_tree_sha,
				 representation, files, file_count, lines_added, lines_deleted,
				 unified_diff, truncated, truncation_reason)
			VALUES ($1, 'ready', $2, $3, $4, 'patch', '[]', 0, 0, 0, '', false, '')
		`, snapshotID, "sha256:"+strings.Repeat("b", 64), strings.Repeat("c", 40), strings.Repeat("d", 40))
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_repository_change_projections SET unified_diff = $2 WHERE snapshot_id = $1`, snapshotID, strings.Repeat("x", 65537))
		Expect(err).To(HaveOccurred(), "database must independently enforce the durable diff bound")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var snapshots int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE id = $1`, snapshotID).Scan(&snapshots)).To(Succeed())
		Expect(snapshots).To(Equal(1))

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var projections int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_repository_change_projections`).Scan(&projections)).To(Succeed())
		Expect(projections).To(Equal(0))
	})
})
