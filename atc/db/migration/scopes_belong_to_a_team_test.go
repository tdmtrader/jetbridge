package migration_test

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	beforeScopesBelongToATeamVersion = 1789793170
	scopesBelongToATeamVersion       = 1789793171
)

// Before 0.4.6 a config's shared scope served every team whose resources or
// resource types had that config, so one team's checks, puts and
// clear-versions wrote into another team's history. The migration gives each
// team its own copy of the history it read, and from then on a scope belongs
// to one team.
var _ = Describe("1789793171 gives each team its own resource config scope", func() {
	var database *sql.DB

	openAt := func(version int) {
		if database != nil {
			Expect(database.Close()).To(Succeed())
		}
		database = postgresRunner.OpenDBAtVersion(version)
	}

	BeforeEach(func() {
		database = nil
		DeferCleanup(func() {
			if database != nil {
				Expect(database.Close()).To(Succeed())
			}
		})
	})

	scopeOf := func(resourceID int) sql.NullInt64 {
		GinkgoHelper()
		var scopeID sql.NullInt64
		Expect(database.QueryRow(`SELECT resource_config_scope_id FROM resources WHERE id = $1`, resourceID).Scan(&scopeID)).To(Succeed())
		return scopeID
	}

	teamOf := func(scopeID int64) sql.NullInt64 {
		GinkgoHelper()
		var teamID sql.NullInt64
		Expect(database.QueryRow(`SELECT team_id FROM resource_config_scopes WHERE id = $1`, scopeID).Scan(&teamID)).To(Succeed())
		return teamID
	}

	scopeExists := func(scopeID int64) bool {
		GinkgoHelper()
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS (SELECT 1 FROM resource_config_scopes WHERE id = $1)`, scopeID).Scan(&exists)).To(Succeed())
		return exists
	}

	type version struct {
		Digest     string
		CheckOrder int
		Metadata   string
	}

	versionsNewestFirst := func(scopeID int64) []version {
		GinkgoHelper()
		rows, err := database.Query(`
			SELECT version_sha256, check_order, metadata::text FROM resource_config_versions
			WHERE resource_config_scope_id = $1
			ORDER BY check_order DESC, version_sha256 DESC`, scopeID)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		versions := []version{}
		for rows.Next() {
			var v version
			Expect(rows.Scan(&v.Digest, &v.CheckOrder, &v.Metadata)).To(Succeed())
			versions = append(versions, v)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return versions
	}

	liveSharedScopeOf := func(configID, teamID int) int64 {
		GinkgoHelper()
		var scopeID int64
		Expect(database.QueryRow(`
			SELECT id FROM resource_config_scopes
			WHERE resource_config_id = $1 AND team_id = $2
			AND resource_id IS NULL AND deprecated_at IS NULL`, configID, teamID).Scan(&scopeID)).To(Succeed())
		return scopeID
	}

	expectCheckViolation := func(err error) {
		GinkgoHelper()
		Expect(err).To(HaveOccurred())
		var pgErr *pgconn.PgError
		Expect(errors.As(err, &pgErr)).To(BeTrue(), "%v", err)
		Expect(pgErr.Code).To(Equal("23514"), pgErr.Message)
	}

	Context("when teams share scopes", func() {
		BeforeEach(func() {
			openAt(beforeScopesBelongToATeamVersion)

			seedBypassingConstraints(database, `
				INSERT INTO teams (id, name) VALUES
					(1, 'main'), (2, 'beta'), (3, 'gamma'), (4, 'type-only');
				INSERT INTO pipelines (id, name, team_id, secondary_ordering, paused, archived) VALUES
					(1, 'p-main',     1, 1, false, false),
					(2, 'p-beta',     2, 1, false, false),
					(3, 'p-gamma',    3, 1, true,  false),
					(4, 'p-typeonly', 4, 1, false, false),
					(5, 'p-archived', 2, 2, false, true);
				INSERT INTO base_resource_types (id, name, unique_version_history) VALUES
					(1, 'git', false),
					(2, 'time', true);
				INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES
					(10, 1, 'three-teams'),
					(20, 1, 'one-team'),
					(30, 2, 'unique'),
					(40, 1, 'unowned'),
					(50, 1, 'resource-and-type'),
					(60, 1, 'deprecated'),
					(70, 1, 'in-memory');
				INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_id, resource_config_scope_id) VALUES
					(1, 'r-main',     1, 'git',  '{}', true,  10, 100),
					(2, 'r-beta',     2, 'git',  '{}', true,  10, 100),
					(3, 'r-gamma',    3, 'git',  '{}', false, 10, 100),
					(4, 'r-archived', 5, 'git',  '{}', false, 10, 100),
					(5, 'r-alone',    1, 'git',  '{}', true,  20, 200),
					(6, 'r-ticker',   2, 'time', '{}', true,  30, 300),
					(7, 'r-typed',    1, 'git',  '{}', true,  50, 500),
					(8, 'r-moved',    2, 'git',  '{}', true,  10, 100),
					(9, 'r-stale',    3, 'git',  '{}', false, 60, 600);
				-- 5100 is an in-memory check build of beta's resource: it has no
				-- builds row.
				INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_id, resource_config_scope_id, in_memory_build_id) VALUES
					(10, 'r-main-mem', 1, 'git', '{}', true, 70, 700, NULL),
					(11, 'r-beta-mem', 2, 'git', '{}', true, 70, 700, 5100);
				INSERT INTO resource_types (id, name, pipeline_id, type, config, active, resource_config_id) VALUES
					(1, 'some-type', 4, 'git', '{}', true, 50),
					(2, 'beta-type', 2, 'git', '{}', true, 10);
				INSERT INTO builds (id, name, status, team_id) VALUES
					(1000, 'check', 'succeeded', 1),
					(1001, 'check', 'succeeded', 4);
				INSERT INTO resource_config_scopes
					(id, resource_config_id, resource_id, last_check_end_time, last_check_succeeded, last_check_build_id, last_check_build_plan,
					 deprecated_at, deprecated_from_resource_id) VALUES
					(100, 10, NULL, '2026-03-01', true,  1000, '{"check":"main"}', NULL, NULL),
					(200, 20, NULL, '2026-03-01', true,  1000, '{"check":"main"}', NULL, NULL),
					(300, 30, 6,    '2026-03-01', true,  NULL, '{}',               NULL, NULL),
					(400, 40, NULL, '2026-03-01', true,  NULL, '{}',               NULL, NULL),
					(500, 50, NULL, '2026-03-02', false, 1001, '{"check":"type"}', NULL, NULL),
					(600, 60, NULL, '2026-03-01', true,  NULL, '{}',               '2026-03-05', 8),
					(610, 60, NULL, '2026-03-01', true,  NULL, '{}',               '2026-03-04', NULL),
					(700, 70, NULL, '2026-03-01', true,  5100, '{"check":"mem"}',  NULL, NULL);
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_md5, version_sha256, metadata, check_order) VALUES
					(100, '{"ref":"a1"}', 'a1', 'a1', '[{"name":"n","value":"1"}]', 1),
					(100, '{"ref":"a2"}', 'a2', 'a2', '[{"name":"n","value":"2"}]', 2),
					(100, '{"ref":"a3"}', 'a3', 'a3', 'null', 5),
					(200, '{"ref":"s1"}', 's1', 's1', 'null', 1),
					(300, '{"time":"t1"}', 't1', 't1', 'null', 1),
					(400, '{"ref":"u1"}', 'u1', 'u1', 'null', 1),
					(500, '{"ref":"m1"}', 'm1', 'm1', 'null', 1),
					(500, '{"ref":"m2"}', 'm2', 'm2', 'null', 2),
					(600, '{"ref":"d1"}', 'd1', 'd1', 'null', 1);
				INSERT INTO resource_pins (resource_id, version, comment_text) VALUES
					(2, '{"ref":"a2"}', 'pinned by beta');
				INSERT INTO resource_disabled_versions (resource_id, version_digest) VALUES
					(2, 'a1');
				INSERT INTO build_resource_config_version_inputs (build_id, resource_id, version_digest, name) VALUES
					(2000, 1, 'a3', 'r-main'),
					(2001, 2, 'a3', 'r-beta');
			`)

			openAt(scopesBelongToATeamVersion)
		})

		It("leaves the original scope with the lowest team, and its resources on it", func() {
			Expect(teamOf(100)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
			Expect(scopeOf(1)).To(Equal(sql.NullInt64{Int64: 100, Valid: true}))
		})

		It("gives every other team that shared a scope its own copy, and points that team's resources at it", func() {
			beta := liveSharedScopeOf(10, 2)
			gamma := liveSharedScopeOf(10, 3)
			Expect(beta).NotTo(Equal(int64(100)))
			Expect(gamma).NotTo(Equal(int64(100)))
			Expect(beta).NotTo(Equal(gamma))

			By("an active, an inactive and a moved resource of one team, and one in an archived pipeline")
			Expect(scopeOf(2).Int64).To(Equal(beta))
			Expect(scopeOf(4).Int64).To(Equal(beta))
			Expect(scopeOf(8).Int64).To(Equal(beta))

			By("an inactive resource in a paused pipeline")
			Expect(scopeOf(3).Int64).To(Equal(gamma))
		})

		It("copies the history with its order and metadata", func() {
			original := versionsNewestFirst(100)
			Expect(original).To(Equal([]version{
				{"a3", 5, "null"},
				{"a2", 2, `[{"name": "n", "value": "2"}]`},
				{"a1", 1, `[{"name": "n", "value": "1"}]`},
			}))
			Expect(versionsNewestFirst(liveSharedScopeOf(10, 2))).To(Equal(original))
			Expect(versionsNewestFirst(liveSharedScopeOf(10, 3))).To(Equal(original))
		})

		It("copies the last check's times, but keeps a last check build only with its own team", func() {
			var (
				endTime   string
				succeeded bool
				buildID   sql.NullInt64
				plan      string
			)
			lastCheck := func(scopeID int64) {
				GinkgoHelper()
				Expect(database.QueryRow(`
					SELECT last_check_end_time::date::text, last_check_succeeded, last_check_build_id, last_check_build_plan::text
					FROM resource_config_scopes WHERE id = $1`, scopeID).Scan(&endTime, &succeeded, &buildID, &plan)).To(Succeed())
			}

			lastCheck(100)
			Expect(buildID).To(Equal(sql.NullInt64{Int64: 1000, Valid: true}))
			Expect(plan).To(Equal(`{"check":"main"}`))

			lastCheck(liveSharedScopeOf(10, 2))
			Expect(endTime).To(Equal("2026-03-01"))
			Expect(succeeded).To(BeTrue())
			Expect(buildID.Valid).To(BeFalse())
			Expect(plan).To(Equal(`{}`))

			By("a scope whose last check was another team's build")
			lastCheck(500)
			Expect(buildID.Valid).To(BeFalse())
			Expect(plan).To(Equal(`{}`))

			lastCheck(liveSharedScopeOf(50, 4))
			Expect(endTime).To(Equal("2026-03-02"))
			Expect(succeeded).To(BeFalse())
			Expect(buildID).To(Equal(sql.NullInt64{Int64: 1001, Valid: true}))
			Expect(plan).To(Equal(`{"check":"type"}`))
		})

		It("keeps an in-memory last check build only on the copy of the team whose resource ran it", func() {
			var buildID sql.NullInt64
			var plan string
			Expect(database.QueryRow(`SELECT last_check_build_id, last_check_build_plan::text FROM resource_config_scopes WHERE id = 700`).Scan(&buildID, &plan)).To(Succeed())
			Expect(buildID.Valid).To(BeFalse())
			Expect(plan).To(Equal(`{}`))

			Expect(database.QueryRow(`SELECT last_check_build_id, last_check_build_plan::text FROM resource_config_scopes WHERE id = $1`, liveSharedScopeOf(70, 2)).Scan(&buildID, &plan)).To(Succeed())
			Expect(buildID).To(Equal(sql.NullInt64{Int64: 5100, Valid: true}))
			Expect(plan).To(Equal(`{"check":"mem"}`))
		})

		It("still resolves a pin, a disabled version and a build input for each team's resource", func() {
			resolves := func(query string, resourceID int) string {
				GinkgoHelper()
				var digest string
				Expect(database.QueryRow(query, resourceID).Scan(&digest)).To(Succeed())
				return digest
			}

			pinned := `
				SELECT v.version_sha256 FROM resources r
				JOIN resource_pins p ON p.resource_id = r.id
				JOIN resource_config_versions v ON v.resource_config_scope_id = r.resource_config_scope_id AND v.version = p.version
				WHERE r.id = $1`
			Expect(resolves(pinned, 2)).To(Equal("a2"))

			disabled := `
				SELECT v.version_sha256 FROM resources r
				JOIN resource_disabled_versions d ON d.resource_id = r.id
				JOIN resource_config_versions v ON v.resource_config_scope_id = r.resource_config_scope_id AND d.version_digest IN (v.version_md5, v.version_sha256)
				WHERE r.id = $1`
			Expect(resolves(disabled, 2)).To(Equal("a1"))

			input := `
				SELECT v.version_sha256 FROM resources r
				JOIN build_resource_config_version_inputs i ON i.resource_id = r.id
				JOIN resource_config_versions v ON v.resource_config_scope_id = r.resource_config_scope_id AND i.version_digest IN (v.version_md5, v.version_sha256)
				WHERE r.id = $1`
			Expect(resolves(input, 1)).To(Equal("a3"))
			Expect(resolves(input, 2)).To(Equal("a3"))
		})

		It("gives a team whose only use of a config is a resource type its own scope", func() {
			typed := liveSharedScopeOf(50, 4)
			Expect(typed).NotTo(Equal(int64(500)))
			Expect(teamOf(500)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
			Expect(versionsNewestFirst(typed)).To(Equal(versionsNewestFirst(500)))
			Expect(scopeOf(7).Int64).To(Equal(int64(500)))
		})

		It("does not copy a team's history again for a resource type of a team that already has the scope", func() {
			var scopes int
			Expect(database.QueryRow(`
				SELECT count(*) FROM resource_config_scopes
				WHERE resource_config_id = 10 AND resource_id IS NULL AND deprecated_at IS NULL`).Scan(&scopes)).To(Succeed())
			Expect(scopes).To(Equal(3))
		})

		It("gives a single team's scope that team, copying nothing", func() {
			Expect(teamOf(200)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
			Expect(scopeOf(5).Int64).To(Equal(int64(200)))
			var scopes int
			Expect(database.QueryRow(`SELECT count(*) FROM resource_config_scopes WHERE resource_config_id = 20`).Scan(&scopes)).To(Succeed())
			Expect(scopes).To(Equal(1))
		})

		It("deletes a live shared scope no resource or resource type uses", func() {
			Expect(scopeExists(400)).To(BeFalse())
			var versions int
			Expect(database.QueryRow(`SELECT count(*) FROM resource_config_versions WHERE resource_config_scope_id = 400`).Scan(&versions)).To(Succeed())
			Expect(versions).To(BeZero())
		})

		It("gives a unique history and a deprecated scope their resource's team", func() {
			Expect(teamOf(300)).To(Equal(sql.NullInt64{Int64: 2, Valid: true}))
			Expect(scopeOf(6).Int64).To(Equal(int64(300)))
			Expect(teamOf(600)).To(Equal(sql.NullInt64{Int64: 2, Valid: true}))
		})

		It("keeps a deprecated scope whose resource is gone, with no team", func() {
			Expect(scopeExists(610)).To(BeTrue())
			Expect(teamOf(610).Valid).To(BeFalse())
		})

		It("detaches a resource from a scope of another team it still pointed at", func() {
			Expect(scopeOf(9).Valid).To(BeFalse())
			Expect(versionsNewestFirst(600)).To(HaveLen(1))
		})

		It("leaves every resource on a scope of its own team, or none", func() {
			var crossing int
			Expect(database.QueryRow(`
				SELECT count(*) FROM resources r
				JOIN pipelines p ON p.id = r.pipeline_id
				JOIN resource_config_scopes s ON s.id = r.resource_config_scope_id
				WHERE s.team_id IS DISTINCT FROM p.team_id`).Scan(&crossing)).To(Succeed())
			Expect(crossing).To(BeZero())
		})

		It("refuses pointing a resource at another team's scope", func() {
			_, err := database.Exec(`UPDATE resources SET resource_config_scope_id = 100 WHERE id = 2`)
			expectCheckViolation(err)

			_, err = database.Exec(`
				INSERT INTO resources (name, pipeline_id, type, config, active, resource_config_id, resource_config_scope_id)
				VALUES ('r-new', 2, 'git', '{}', true, 10, 100)`)
			expectCheckViolation(err)

			By("allowing its own team's scope, or none")
			_, err = database.Exec(`UPDATE resources SET resource_config_scope_id = NULL WHERE id = 2`)
			Expect(err).NotTo(HaveOccurred())
			_, err = database.Exec(`UPDATE resources SET resource_config_scope_id = $1 WHERE id = 2`, liveSharedScopeOf(10, 2))
			Expect(err).NotTo(HaveOccurred())
		})

		It("keeps one live shared scope per config and team, and none without a team", func() {
			_, err := database.Exec(`INSERT INTO resource_config_scopes (resource_config_id, team_id) VALUES (10, 2)`)
			Expect(err).To(HaveOccurred())

			_, err = database.Exec(`INSERT INTO resource_config_scopes (resource_config_id, team_id) VALUES (40, NULL)`)
			expectCheckViolation(err)

			_, err = database.Exec(`INSERT INTO resource_config_scopes (resource_config_id, team_id) VALUES (40, 2)`)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when only one team uses each scope", func() {
		type scopeRow struct {
			ID, ConfigID int
			ResourceID   sql.NullInt64
			LastBuildID  sql.NullInt64
		}

		snapshot := func() ([]scopeRow, map[int]int64, []int64) {
			GinkgoHelper()
			rows, err := database.Query(`SELECT id, resource_config_id, resource_id, last_check_build_id FROM resource_config_scopes ORDER BY id`)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			scopes := []scopeRow{}
			for rows.Next() {
				var s scopeRow
				Expect(rows.Scan(&s.ID, &s.ConfigID, &s.ResourceID, &s.LastBuildID)).To(Succeed())
				scopes = append(scopes, s)
			}

			pointers := map[int]int64{}
			rrows, err := database.Query(`SELECT id, resource_config_scope_id FROM resources`)
			Expect(err).NotTo(HaveOccurred())
			defer rrows.Close()
			for rrows.Next() {
				var id int
				var scopeID int64
				Expect(rrows.Scan(&id, &scopeID)).To(Succeed())
				pointers[id] = scopeID
			}

			versions := []int64{}
			vrows, err := database.Query(`SELECT id FROM resource_config_versions ORDER BY id`)
			Expect(err).NotTo(HaveOccurred())
			defer vrows.Close()
			for vrows.Next() {
				var id int64
				Expect(vrows.Scan(&id)).To(Succeed())
				versions = append(versions, id)
			}
			return scopes, pointers, versions
		}

		It("changes nothing but the team column, keeping a last check build it cannot place", func() {
			openAt(beforeScopesBelongToATeamVersion)
			seedBypassingConstraints(database, `
				INSERT INTO teams (id, name) VALUES (1, 'main'), (2, 'other');
				INSERT INTO pipelines (id, name, team_id, secondary_ordering) VALUES (1, 'p1', 1, 1), (2, 'p2', 1, 2), (3, 'p3', 2, 1);
				INSERT INTO base_resource_types (id, name, unique_version_history) VALUES (1, 'git', false);
				INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES (10, 1, 'a'), (20, 1, 'b');
				INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_id, resource_config_scope_id, in_memory_build_id) VALUES
					(1, 'one', 1, 'git', '{}', true, 10, 100, 5000),
					(2, 'two', 2, 'git', '{}', true, 10, 100, NULL),
					(3, 'three', 3, 'git', '{}', true, 20, 200, NULL);
				-- 5000 is an in-memory check build of resource one; 6000 has
				-- no builds row and no resource running it.
				INSERT INTO resource_config_scopes (id, resource_config_id, last_check_build_id) VALUES (100, 10, 5000), (200, 20, 6000);
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_md5, version_sha256, check_order) VALUES
					(100, '{"ref":"a"}', 'a', 'a', 1),
					(200, '{"ref":"b"}', 'b', 'b', 1);
			`)
			scopes, pointers, versions := snapshot()

			openAt(scopesBelongToATeamVersion)
			afterScopes, afterPointers, afterVersions := snapshot()
			Expect(afterScopes).To(Equal(scopes))
			Expect(afterPointers).To(Equal(pointers))
			Expect(afterVersions).To(Equal(versions))
			Expect(teamOf(100)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
			Expect(teamOf(200)).To(Equal(sql.NullInt64{Int64: 2, Valid: true}))
		})
	})

	// An outgoing web keeps running while the migration does. Its
	// transactions lock resources and scopes in either order: a put or a move
	// locks its resource row and then reads scopes; the deprecated-scope and
	// config collectors delete scopes, whose ON DELETE SET NULL then writes
	// resources. Neither order may deadlock the migration.
	Context("while an outgoing web's transaction holds one of the tables", func() {
		var other *sql.DB

		BeforeEach(func() {
			openAt(beforeScopesBelongToATeamVersion)
			seedBypassingConstraints(database, `
				INSERT INTO teams (id, name) VALUES (1, 'main'), (2, 'other');
				INSERT INTO pipelines (id, name, team_id, secondary_ordering) VALUES (1, 'p1', 1, 1), (2, 'p2', 2, 1);
				INSERT INTO base_resource_types (id, name, unique_version_history) VALUES (1, 'git', false);
				INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES (10, 1, 'a');
				INSERT INTO resources (id, name, pipeline_id, type, config, nonce, active, resource_config_id, resource_config_scope_id) VALUES
					(1, 'one', 1, 'git', '{}', NULL, true, 10, 100),
					(2, 'two', 2, 'git', '{}', NULL, true, 10, 100);
				INSERT INTO resource_config_scopes (id, resource_config_id) VALUES (100, 10);
				INSERT INTO resource_config_versions (resource_config_scope_id, version, version_md5, version_sha256, check_order) VALUES
					(100, '{"ref":"a"}', 'a', 'a', 1);
			`)
			Expect(database.Close()).To(Succeed())
			database = nil

			var err error
			other, err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
			other.SetMaxOpenConns(2)
			DeferCleanup(other.Close)
		})

		migrate := func() chan error {
			migrated := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				migrated_db, err := postgresRunner.TryOpenDBAtVersion(scopesBelongToATeamVersion)
				if migrated_db != nil {
					database = migrated_db
				}
				migrated <- err
			}()
			return migrated
		}

		lockWaiters := func() int {
			var waiting int
			Expect(other.QueryRow(`SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&waiting)).To(Succeed())
			return waiting
		}

		It("commits when the transaction holds a resource row and then reads scopes", func() {
			tx, err := other.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()
			// Resource two is the one the migration repoints at its team's copy.
			_, err = tx.Exec(`SELECT id FROM resources WHERE id = 2 FOR NO KEY UPDATE`)
			Expect(err).NotTo(HaveOccurred())

			migrated := migrate()
			Eventually(lockWaiters, 10*time.Second).Should(BeNumerically(">", 0))

			var scopes int
			readErr := tx.QueryRow(`SELECT count(*) FROM resource_config_scopes`).Scan(&scopes)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			Eventually(migrated, 60*time.Second).Should(Receive(BeNil()))
			Expect(teamOf(100)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
		})

		It("commits when the transaction holds scopes and then locks a resource row", func() {
			tx, err := other.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()
			var scopes int
			Expect(tx.QueryRow(`SELECT count(*) FROM resource_config_scopes`).Scan(&scopes)).To(Succeed())

			migrated := migrate()
			Eventually(lockWaiters, 10*time.Second).Should(BeNumerically(">", 0))

			// Either this session waits for the migration and proceeds when
			// it rolls its attempt back, or Postgres ends this one as the
			// deadlock's victim; the migration must not be the one to fail.
			_, _ = tx.Exec(`SELECT id FROM resources WHERE id = 2 FOR NO KEY UPDATE`)
			_ = tx.Rollback()

			Eventually(migrated, 60*time.Second).Should(Receive(BeNil()))
			Expect(teamOf(100)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
		})
	})

	// A 0.4.5 destroy-team deletes the team row, then its pipelines'
	// resources: it holds teams and then waits on resources, while the
	// migration's new foreign key needs teams.
	Context("while an outgoing web destroys a team", func() {
		var other *sql.DB

		BeforeEach(func() {
			openAt(beforeScopesBelongToATeamVersion)
			seedBypassingConstraints(database, `
				INSERT INTO teams (id, name) VALUES (1, 'main'), (2, 'other'), (3, 'doomed');
				INSERT INTO pipelines (id, name, team_id, secondary_ordering) VALUES (1, 'p1', 1, 1), (2, 'p2', 2, 1);
				INSERT INTO base_resource_types (id, name, unique_version_history) VALUES (1, 'git', false);
				INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES (10, 1, 'a');
				INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_id, resource_config_scope_id) VALUES
					(1, 'one', 1, 'git', '{}', true, 10, 100),
					(2, 'two', 2, 'git', '{}', true, 10, 100);
				INSERT INTO resource_config_scopes (id, resource_config_id) VALUES (100, 10);
			`)
			Expect(database.Close()).To(Succeed())
			database = nil

			var err error
			other, err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
			other.SetMaxOpenConns(2)
			DeferCleanup(other.Close)
		})

		It("commits when the session has deleted a team and then waits on resources", func() {
			tx, err := other.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()
			_, err = tx.Exec(`DELETE FROM teams WHERE id = 3`)
			Expect(err).NotTo(HaveOccurred())

			migrated := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				migratedDB, err := postgresRunner.TryOpenDBAtVersion(scopesBelongToATeamVersion)
				if migratedDB != nil {
					database = migratedDB
				}
				migrated <- err
			}()

			Eventually(func() int {
				var waiting int
				Expect(other.QueryRow(`SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&waiting)).To(Succeed())
				return waiting
			}, 10*time.Second).Should(BeNumerically(">", 0))

			// Either this session waits for the migration's attempt to roll
			// back, or Postgres ends it as the deadlock's victim; the
			// migration must not be the one to fail.
			_, _ = tx.Exec(`SELECT id FROM resources WHERE id = 2 FOR NO KEY UPDATE`)
			_ = tx.Rollback()

			Eventually(migrated, 60*time.Second).Should(Receive(BeNil()))
			Expect(teamOf(100)).To(Equal(sql.NullInt64{Int64: 1, Valid: true}))
		})
	})

	Context("when the database is empty", func() {
		It("migrates", func() {
			openAt(beforeScopesBelongToATeamVersion)
			openAt(scopesBelongToATeamVersion)

			var scopes int
			Expect(database.QueryRow(`SELECT count(*) FROM resource_config_scopes`).Scan(&scopes)).To(Succeed())
			Expect(scopes).To(BeZero())
		})
	})
})
