package migration_test

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("snapshot direct team ownership migration", func() {
	const beforeVersion, targetVersion = 1773106138, 1773106139

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

	insertTeam := func(label string) int {
		var teamID int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, fmt.Sprintf("snapshot-owner-%s-%d", label, time.Now().UnixNano())).Scan(&teamID)).To(Succeed())
		return teamID
	}

	insertLegacySnapshot := func(digestByte string) int64 {
		var snapshotID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository', 1, $1, 1, 1, 'application/x-tar')
			RETURNING id
		`, "sha256:"+strings.Repeat(digestByte, 64)).Scan(&snapshotID)).To(Succeed())
		return snapshotID
	}

	grant := func(snapshotID int64, teamID int) {
		_, err := database.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'migration-test', 'legacy ownership')
		`, snapshotID, teamID)
		Expect(err).NotTo(HaveOccurred())
	}

	It("migrates the 6138 schema to direct ownership and permits independently owned equal digests", func() {
		firstTeamID := insertTeam("first")
		secondTeamID := insertTeam("second")
		snapshotID := insertLegacySnapshot("a")
		grant(snapshotID, firstTeamID)
		_, err := database.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'migration-test', 'idempotent legacy retry')
			ON CONFLICT (snapshot_id, team_id) DO NOTHING
		`, snapshotID, firstTeamID)
		Expect(err).NotTo(HaveOccurred(), "a duplicate legacy retry for the same team must remain unambiguous")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var ownerID int
		Expect(database.QueryRow(`SELECT team_id FROM agent_snapshots WHERE id = $1`, snapshotID).Scan(&ownerID)).To(Succeed())
		Expect(ownerID).To(Equal(firstTeamID))
		var grantsTable int
		Expect(database.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'agent_snapshot_grants'`).Scan(&grantsTable)).To(Succeed())
		Expect(grantsTable).To(Equal(0))

		var duplicateID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'repository', 1, $2, 1, 1, 'application/x-tar')
			RETURNING id
		`, secondTeamID, "sha256:"+strings.Repeat("a", 64)).Scan(&duplicateID)).To(Succeed())
		Expect(duplicateID).NotTo(Equal(snapshotID))

		var crossTeamReadCount int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_snapshots WHERE id = $1 AND team_id = $2`, snapshotID, secondTeamID).Scan(&crossTeamReadCount)).To(Succeed())
		Expect(crossTeamReadCount).To(Equal(0), "the same physical digest must not authorize another team")
	})

	It("creates a fresh ownership-only snapshot schema", func() {
		Expect(migrator.Up(nil, nil)).To(Succeed())

		var teamColumn, grantsTable int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_snapshots' AND column_name = 'team_id' AND is_nullable = 'NO'
		`).Scan(&teamColumn)).To(Succeed())
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.tables WHERE table_name = 'agent_snapshot_grants'
		`).Scan(&grantsTable)).To(Succeed())
		Expect(teamColumn).To(Equal(1))
		Expect(grantsTable).To(Equal(0))
	})

	It("fails closed when a legacy snapshot has no owning grant", func() {
		_ = insertLegacySnapshot("b")

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("requires exactly one distinct grant team per snapshot")))
	})

	It("fails closed when a legacy snapshot has grants for multiple teams", func() {
		firstTeamID := insertTeam("ambiguous-first")
		secondTeamID := insertTeam("ambiguous-second")
		snapshotID := insertLegacySnapshot("c")
		grant(snapshotID, firstTeamID)
		grant(snapshotID, secondTeamID)

		err := migrator.Migrate(nil, nil, targetVersion)
		Expect(err).To(MatchError(ContainSubstring("requires exactly one distinct grant team per snapshot")))
	})

	It("rolls back only when direct identities remain representable by the legacy global identity", func() {
		teamID := insertTeam("rollback")
		snapshotID := insertLegacySnapshot("d")
		grant(snapshotID, teamID)
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var restoredOwner int
		Expect(database.QueryRow(`SELECT team_id FROM agent_snapshot_grants WHERE snapshot_id = $1`, snapshotID).Scan(&restoredOwner)).To(Succeed())
		Expect(restoredOwner).To(Equal(teamID))
		var teamColumn int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_snapshots' AND column_name = 'team_id'
		`).Scan(&teamColumn)).To(Succeed())
		Expect(teamColumn).To(Equal(0))
	})

	It("refuses a rollback that would coalesce independently owned equal digests", func() {
		firstTeamID := insertTeam("down-first")
		secondTeamID := insertTeam("down-second")
		snapshotID := insertLegacySnapshot("e")
		grant(snapshotID, firstTeamID)
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		_, err := database.Exec(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, 'repository', 1, $2, 1, 1, 'application/x-tar')
		`, secondTeamID, "sha256:"+strings.Repeat("e", 64))
		Expect(err).NotTo(HaveOccurred())

		err = migrator.Migrate(nil, nil, beforeVersion)
		Expect(err).To(MatchError(ContainSubstring("cannot downgrade team-scoped duplicate content identities")))
	})
})
