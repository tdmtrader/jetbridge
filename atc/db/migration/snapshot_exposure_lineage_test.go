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

var _ = Describe("snapshot exposure lineage migration", func() {
	const beforeVersion, targetVersion = 1773106126, 1773106127
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

	It("backfills full-tree exposure for existing lineage and refuses unrecordable materialization", func() {
		unique := time.Now().UnixNano()
		suffix := fmt.Sprintf("exposure-%d", unique)
		inputDigest := "sha256:" + strings.Repeat("a", 64)
		otherDigest := "sha256:" + strings.Repeat("b", 64)
		outputDigest := "sha256:" + strings.Repeat("c", 64)

		var teamID, buildID int
		var inputSnapshotID, otherSnapshotID, outputSnapshotID, productionID int64
		Expect(database.QueryRow(`
			INSERT INTO teams (name) VALUES ($1) RETURNING id
		`, suffix).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'alice') RETURNING id
		`, suffix, teamID).Scan(&buildID)).To(Succeed())
		insertSnapshot := func(typeName string, digest string) int64 {
			var id int64
			Expect(database.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, 1, $2, 10, 1, 'application/x-tar')
				RETURNING id
			`, typeName, digest).Scan(&id)).To(Succeed())
			return id
		}
		inputSnapshotID = insertSnapshot(suffix+"-input", inputDigest)
		otherSnapshotID = insertSnapshot(suffix+"-other", otherDigest)
		outputSnapshotID = insertSnapshot(suffix+"-output", outputDigest)
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
				 plan_id, attempt, step_kind, step_name, output_port)
			VALUES ($1, 'build', $2, $3, $4, 'alice', 'plan-1', '1', 'agent', 'judge', 'review')
			RETURNING id
		`, outputSnapshotID, buildID, teamID, suffix).Scan(&productionID)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
			VALUES ($1, 0, 'diff', $2), ($1, 1, 'base', $3)
		`, productionID, inputSnapshotID, otherSnapshotID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		type exposureRow struct {
			mode      string
			tree      string
			mountPath sql.NullString
		}
		read := func(port string) exposureRow {
			var row exposureRow
			Expect(database.QueryRow(`
				SELECT materialization_mode, tree_digest, mount_path
				FROM agent_snapshot_exposures
				WHERE production_id = $1 AND input_port = $2
			`, productionID, port).Scan(&row.mode, &row.tree, &row.mountPath)).To(Succeed())
			return row
		}
		// No partial mounting has ever existed, so 'full' is the accurate
		// historical claim and the exposed tree is the input's own digest.
		Expect(read("diff")).To(Equal(exposureRow{mode: "full", tree: inputDigest}))
		Expect(read("base")).To(Equal(exposureRow{mode: "full", tree: otherDigest}))

		var exposureID int64
		Expect(database.QueryRow(`
			SELECT id FROM agent_snapshot_exposures WHERE production_id = $1 AND input_port = 'diff'
		`, productionID).Scan(&exposureID)).To(Succeed())

		// A full exposure records the whole tree, so an enumerated path set
		// under it is unrepresentable, not merely discouraged.
		_, err = database.Exec(`
			INSERT INTO agent_snapshot_exposure_paths (exposure_id, path, digest)
			VALUES ($1, 'record.json', $2)
		`, exposureID, outputDigest)
		Expect(err).To(HaveOccurred())

		// Dynamic, agent-driven partial mounting cannot have its lineage known
		// at admission, so the mode is not expressible at all.
		_, err = database.Exec(`
			INSERT INTO agent_snapshot_exposures
				(production_id, input_port, input_snapshot_id, materialization_mode, tree_digest)
			VALUES ($1, 'dyn', $2, 'dynamic', $3)
		`, productionID, inputSnapshotID, inputDigest)
		Expect(err).To(HaveOccurred())

		// An exposure may only describe an input the production was actually
		// given, and only with that input's own digest.
		_, err = database.Exec(`
			INSERT INTO agent_snapshot_exposures
				(production_id, input_port, input_snapshot_id, materialization_mode, tree_digest)
			VALUES ($1, 'never-exposed', $2, 'full', $3)
		`, productionID, inputSnapshotID, inputDigest)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`
			UPDATE agent_snapshot_exposures SET tree_digest = $1 WHERE id = $2
		`, otherDigest, exposureID)
		Expect(err).To(HaveOccurred())

		// A static selector is the only mode that may enumerate paths.
		_, err = database.Exec(`
			UPDATE agent_snapshot_exposures
			SET materialization_mode = 'static-selector', mount_path = '/tmp/build/plan/diff'
			WHERE id = $1
		`, exposureID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_snapshot_exposure_paths (exposure_id, path, digest)
			VALUES ($1, 'record.json', $2)
		`, exposureID, outputDigest)
		Expect(err).NotTo(HaveOccurred())
		for _, path := range []string{"/record.json", "../record.json", "findings/*.json", "findings/", ""} {
			_, err = database.Exec(`
				INSERT INTO agent_snapshot_exposure_paths (exposure_id, path, digest)
				VALUES ($1, $2, $3)
			`, exposureID, path, outputDigest)
			Expect(err).To(HaveOccurred(), "path %q must not be storable", path)
		}

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var relations int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.tables
			WHERE table_name IN ('agent_snapshot_exposures', 'agent_snapshot_exposure_paths')
		`).Scan(&relations)).To(Succeed())
		Expect(relations).To(Equal(0))
		var lineageRows int
		Expect(database.QueryRow(`
			SELECT count(*) FROM agent_snapshot_lineage WHERE production_id = $1
		`, productionID).Scan(&lineageRows)).To(Succeed())
		Expect(lineageRows).To(Equal(2))
	})
})
