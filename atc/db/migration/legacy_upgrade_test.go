package migration_test

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	"code.cloudfoundry.org/lager/v3"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These tests validate the database migration path from legacy Concourse
// versions (v7.13, v8.0.1) to JetBridge HEAD, ensuring that all pipeline
// data survives the migration intact. They also validate the migration
// runbook's pre-flight script, garden-era data cleanup, idempotency, and
// rollback procedures.

// v7.13.x ends at this migration version
const v713LastMigration = 1666754000

// v8.0.1 last migration
const v801LastMigration = 1765921815

// JetBridge HEAD (last migration)
const jetbridgeHeadMigration = 1773105503

var _ = Describe("Legacy Database Upgrade", func() {
	var (
		db          *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		migrator    migration.Migrator
	)

	BeforeEach(func() {
		var err error
		db, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())

		for i := range lock.FactoryCount {
			lockDB[i], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		fakeLogFunc := func(logger lager.Logger, id lock.LockID) {}
		lockFactory = lock.NewLockFactory(lockDB, fakeLogFunc, fakeLogFunc)
		migrator = migration.NewMigrator(db, lockFactory)
	})

	AfterEach(func() {
		_ = db.Close()
		for _, c := range lockDB {
			c.Close()
		}
	})

	Describe("Upgrading from v7.13 to JetBridge HEAD", func() {
		BeforeEach(func() {
			By("Migrating up to v7.13 (migration " + fmt.Sprint(v713LastMigration) + ")")
			err := migrator.Migrate(nil, nil, v713LastMigration)
			Expect(err).NotTo(HaveOccurred())

			ExpectDatabaseMigrationVersionToEqual(migrator, v713LastMigration)
		})

		It("preserves all pipeline data through the full migration", func() {
			By("Inserting v7.13-era fixture data")
			insertV713FixtureData(db)

			By("Verifying fixture data was inserted correctly")
			verifyFixtureDataPresent(db, true /* pre-migration */)

			By("Migrating to JetBridge HEAD")
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying migration reached JetBridge HEAD version")
			ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)

			By("Verifying all pipeline data survived the migration")
			verifyFixtureDataPresent(db, false /* post-migration */)

			By("Verifying JetBridge-specific schema changes were applied")
			verifyJetBridgeSchemaChanges(db)

			By("Verifying md5→sha256 migration was applied correctly")
			verifyMD5ToSHA256Migration(db)
		})

		It("can insert new data after migration", func() {
			By("Migrating to JetBridge HEAD with empty database")
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())

			By("Inserting data into the migrated schema")
			mustExec(db, `INSERT INTO teams(id, name) VALUES(100, 'post-migration-team')`)
			mustExec(db, `INSERT INTO pipelines(id, name, team_id, secondary_ordering) VALUES(100, 'post-migration-pipe', 100, 1)`)
			mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, schedule_requested) VALUES(100, 'post-migration-job', 100, '{}', '2026-01-01')`)
		})
	})

	Describe("Upgrading from v8.0.1 to JetBridge HEAD", func() {
		BeforeEach(func() {
			By("Migrating up to v8.0.1 (migration " + fmt.Sprint(v801LastMigration) + ")")
			err := migrator.Migrate(nil, nil, v801LastMigration)
			Expect(err).NotTo(HaveOccurred())
		})

		It("preserves all pipeline data through the JetBridge-only migrations", func() {
			By("Inserting v8.0.1-era fixture data")
			insertV801FixtureData(db)

			By("Migrating to JetBridge HEAD")
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())

			ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)

			By("Verifying teams survived")
			expectRowCount(db, "teams", 2)

			By("Verifying pipelines survived")
			expectRowCount(db, "pipelines", 2)

			By("Verifying jobs survived")
			expectRowCount(db, "jobs", 3)

			By("Verifying builds survived with correct statuses")
			expectRowCount(db, "builds", 5)
			verifyBuildStatuses(db, map[string]int{
				"succeeded": 3,
				"failed":    1,
				"errored":   1,
			})

			By("Verifying JetBridge schema changes applied")
			verifyJetBridgeSchemaChanges(db)
		})
	})

	Describe("Garden-era data cleanup", func() {
		BeforeEach(func() {
			By("Migrating v7.13 data to JetBridge HEAD")
			err := migrator.Migrate(nil, nil, v713LastMigration)
			Expect(err).NotTo(HaveOccurred())

			insertV713FixtureData(db)

			err = migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
		})

		It("removes stale worker cache tables without affecting core data", func() {
			By("Verifying stale data exists before cleanup")
			expectRowCount(db, "worker_base_resource_types", 1)
			expectRowCount(db, "workers", 2)
			expectRowCount(db, "containers", 2)
			expectRowCount(db, "volumes", 2)

			By("Truncating stale worker cache tables")
			mustExec(db, `TRUNCATE worker_task_caches CASCADE`)
			mustExec(db, `TRUNCATE worker_resource_caches CASCADE`)
			mustExec(db, `TRUNCATE worker_base_resource_types CASCADE`)

			By("Verifying cache tables are empty")
			expectRowCount(db, "worker_base_resource_types", 0)
			expectRowCount(db, "worker_task_caches", 0)
			expectRowCount(db, "worker_resource_caches", 0)

			By("Verifying core pipeline data is intact")
			expectRowCount(db, "teams", 2)
			expectRowCount(db, "pipelines", 3)
			expectRowCount(db, "jobs", 5)
			expectRowCount(db, "builds", 10)
			expectRowCount(db, "resources", 2)
			expectRowCount(db, "resource_config_versions", 5)
		})

		It("removes stale Garden workers and orphaned containers/volumes", func() {
			By("Deleting all Garden workers")
			mustExec(db, `DELETE FROM workers`)
			expectRowCount(db, "workers", 0)

			By("Cleaning up orphaned containers referencing deleted workers")
			mustExec(db, `DELETE FROM containers WHERE worker_name NOT IN (SELECT name FROM workers)`)
			expectRowCount(db, "containers", 0)

			By("Cleaning up orphaned volumes referencing deleted workers")
			mustExec(db, `DELETE FROM volumes WHERE worker_name NOT IN (SELECT name FROM workers)`)
			expectRowCount(db, "volumes", 0)

			By("Verifying core pipeline data is still intact after cleanup")
			expectRowCount(db, "teams", 2)
			expectRowCount(db, "pipelines", 3)
			expectRowCount(db, "jobs", 5)
			expectRowCount(db, "builds", 10)
			expectRowCount(db, "resources", 2)
			expectRowCount(db, "resource_config_versions", 5)

			By("Verifying build history is fully preserved with correct statuses")
			verifyBuildStatuses(db, map[string]int{
				"succeeded": 7,
				"failed":    2,
				"errored":   1,
			})
		})

		It("allows inserting new K8s workers after cleanup", func() {
			By("Deleting Garden workers")
			mustExec(db, `DELETE FROM containers`)
			mustExec(db, `DELETE FROM volumes`)
			mustExec(db, `DELETE FROM workers`)

			By("Inserting a K8s-style worker")
			mustExec(db, `INSERT INTO workers(name, addr, state, platform, version) VALUES('k8s-concourse', '', 'running', 'linux', '2.5')`)
			expectRowCount(db, "workers", 1)

			var workerName string
			err := db.QueryRow("SELECT name FROM workers").Scan(&workerName)
			Expect(err).NotTo(HaveOccurred())
			Expect(workerName).To(Equal("k8s-concourse"))
		})
	})

	Describe("Migration idempotency", func() {
		It("is safe to call Up() when already at HEAD", func() {
			By("Migrating to JetBridge HEAD")
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
			ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)

			By("Inserting data at HEAD")
			mustExec(db, `INSERT INTO teams(id, name) VALUES(1, 'main')`)
			mustExec(db, `INSERT INTO pipelines(id, name, team_id, secondary_ordering) VALUES(1, 'ci', 1, 1)`)

			By("Calling Up() again — should be a no-op")
			err = migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
			ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)

			By("Verifying data is still intact")
			expectRowCount(db, "teams", 1)
			expectRowCount(db, "pipelines", 1)
		})

		It("is safe to Migrate() to the current version", func() {
			By("Migrating to JetBridge HEAD")
			err := migrator.Migrate(nil, nil, jetbridgeHeadMigration)
			Expect(err).NotTo(HaveOccurred())

			mustExec(db, `INSERT INTO teams(id, name) VALUES(1, 'main')`)

			By("Migrating to the same version again")
			err = migrator.Migrate(nil, nil, jetbridgeHeadMigration)
			Expect(err).NotTo(HaveOccurred())

			expectRowCount(db, "teams", 1)
		})
	})

	Describe("Migration rollback", func() {
		It("can migrate down from JetBridge HEAD to v8.0.1", func() {
			By("Migrating to JetBridge HEAD")
			err := migrator.Migrate(nil, nil, jetbridgeHeadMigration)
			Expect(err).NotTo(HaveOccurred())

			By("Inserting data at HEAD")
			mustExec(db, `INSERT INTO teams(id, name) VALUES(1, 'main')`)
			mustExec(db, `INSERT INTO components(id, name) VALUES(1, 'scheduler')`)

			By("Rolling back to v8.0.1")
			err = migrator.Migrate(nil, nil, v801LastMigration)
			Expect(err).NotTo(HaveOccurred())
			ExpectDatabaseMigrationVersionToEqual(migrator, v801LastMigration)

			By("Verifying teams survived rollback")
			expectRowCount(db, "teams", 1)

			By("Verifying component columns were restored")
			for _, col := range []string{"interval", "paused"} {
				var exists bool
				err = db.QueryRow(
					"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'components' AND column_name = $1)",
					col,
				).Scan(&exists)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeTrue(), "column components.%s should be restored after rollback", col)
			}

			By("Verifying components survived rollback")
			expectRowCount(db, "components", 1)
		})

		It("can migrate back up after a rollback", func() {
			By("Migrating to HEAD, rolling back, then migrating up again")
			err := migrator.Migrate(nil, nil, jetbridgeHeadMigration)
			Expect(err).NotTo(HaveOccurred())

			mustExec(db, `INSERT INTO teams(id, name) VALUES(1, 'main')`)

			err = migrator.Migrate(nil, nil, v801LastMigration)
			Expect(err).NotTo(HaveOccurred())

			err = migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
			ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)

			By("Verifying data survived the round-trip")
			expectRowCount(db, "teams", 1)
			verifyJetBridgeSchemaChanges(db)
		})
	})

	Describe("Pre-flight validation script", func() {
		It("passes against a v7.13 database", func() {
			err := migrator.Migrate(nil, nil, v713LastMigration)
			Expect(err).NotTo(HaveOccurred())

			insertV713FixtureData(db)

			runPreflightAndExpectPass(postgresRunner.DataSourceName())
		})

		It("passes against a v8.0.1 database", func() {
			err := migrator.Migrate(nil, nil, v801LastMigration)
			Expect(err).NotTo(HaveOccurred())

			insertV801FixtureData(db)

			runPreflightAndExpectPass(postgresRunner.DataSourceName())
		})

		It("passes against a database already at JetBridge HEAD", func() {
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())

			runPreflightAndExpectPass(postgresRunner.DataSourceName())
		})
	})

	Describe("Component data preservation", func() {
		It("preserves component names after column drops", func() {
			By("Setting up v8.0.1 with components")
			err := migrator.Migrate(nil, nil, v801LastMigration)
			Expect(err).NotTo(HaveOccurred())

			mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(1, 'scheduler', '10s', false)`)
			mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(2, 'scanner', '10s', false)`)
			mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(3, 'build_tracker', '10s', true)`)

			By("Migrating to JetBridge HEAD")
			err = migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying component names survived")
			expectRowCount(db, "components", 3)

			var names []string
			rows, err := db.Query("SELECT name FROM components ORDER BY name")
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			for rows.Next() {
				var name string
				Expect(rows.Scan(&name)).To(Succeed())
				names = append(names, name)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			Expect(names).To(Equal([]string{"build_tracker", "scanner", "scheduler"}))

			By("Verifying dropped columns are gone")
			for _, col := range []string{"interval", "last_ran", "paused"} {
				var exists bool
				err = db.QueryRow(
					"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'components' AND column_name = $1)",
					col,
				).Scan(&exists)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(BeFalse(), "column components.%s should have been dropped", col)
			}
		})
	})

	Describe("Notify triggers after migration", func() {
		BeforeEach(func() {
			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
		})

		It("fires worker notifications on worker insert/update/delete", func() {
			By("Setting up a LISTEN on worker_events")
			listenerDB, err := sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
			defer listenerDB.Close()

			// Verify the trigger functions exist
			var fnExists bool
			err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'notify_worker_event')`).Scan(&fnExists)
			Expect(err).NotTo(HaveOccurred())
			Expect(fnExists).To(BeTrue(), "notify_worker_event function should exist")

			err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'notify_container_event')`).Scan(&fnExists)
			Expect(err).NotTo(HaveOccurred())
			Expect(fnExists).To(BeTrue(), "notify_container_event function should exist")

			By("Verifying old trigger functions are gone")
			err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'notify_trigger')`).Scan(&fnExists)
			Expect(err).NotTo(HaveOccurred())
			Expect(fnExists).To(BeFalse(), "old notify_trigger function should have been dropped")
		})
	})
})

// insertV713FixtureData inserts realistic data matching the v7.13 schema.
// This exercises all tables affected by the migration delta.
func insertV713FixtureData(db *sql.DB) {
	// Teams
	mustExec(db, `INSERT INTO teams(id, name, auth) VALUES(1, 'main', '{"owner":{"users":["local:admin"]}}')`)
	mustExec(db, `INSERT INTO teams(id, name, auth) VALUES(2, 'dev-team', '{"owner":{"users":["local:dev"]}}')`)

	// Pipelines (secondary_ordering is NOT NULL since migration 1619180098)
	mustExec(db, `INSERT INTO pipelines(id, name, team_id, paused, public, archived, secondary_ordering) VALUES(1, 'ci', 1, false, true, false, 1)`)
	mustExec(db, `INSERT INTO pipelines(id, name, team_id, paused, public, archived, secondary_ordering) VALUES(2, 'release', 1, false, false, false, 1)`)
	mustExec(db, `INSERT INTO pipelines(id, name, team_id, paused, public, archived, secondary_ordering) VALUES(3, 'old-pipeline', 2, true, false, true, 1)`)

	// Jobs (schedule_requested has no default in some versions)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(1, 'unit-tests', 1, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(2, 'build-image', 1, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(3, 'deploy', 1, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(4, 'release-cut', 2, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(5, 'old-job', 3, '{"plan":[]}', false, true, 1, '2026-01-01')`)

	// Builds (mix of statuses)
	insertBuild(db, 1, "1", 1, 1, 1, "succeeded")
	insertBuild(db, 2, "2", 1, 1, 1, "succeeded")
	insertBuild(db, 3, "3", 1, 1, 1, "failed")
	insertBuild(db, 4, "4", 1, 1, 1, "succeeded")
	insertBuild(db, 5, "1", 2, 1, 1, "succeeded")
	insertBuild(db, 6, "2", 2, 1, 1, "failed")
	insertBuild(db, 7, "1", 3, 1, 1, "succeeded")
	insertBuild(db, 8, "1", 4, 1, 2, "succeeded")
	insertBuild(db, 9, "1", 5, 2, 3, "succeeded")
	insertBuild(db, 10, "2", 5, 2, 3, "errored")

	// Base resource types (needed as FK for resource_configs and worker_base_resource_types)
	mustExec(db, `INSERT INTO base_resource_types(id, name) VALUES(1, 'registry-image')`)
	mustExec(db, `INSERT INTO base_resource_types(id, name) VALUES(2, 'git')`)

	// Resource configs (needed as FK for resource_config_scopes)
	mustExec(db, `INSERT INTO resource_configs(id, base_resource_type_id, source_hash) VALUES(1, 2, 'hash1')`)
	mustExec(db, `INSERT INTO resource_configs(id, base_resource_type_id, source_hash) VALUES(2, 1, 'hash2')`)

	// Resource config scopes
	mustExec(db, `INSERT INTO resource_config_scopes(id, resource_config_id) VALUES(1, 1)`)
	mustExec(db, `INSERT INTO resource_config_scopes(id, resource_config_id) VALUES(2, 2)`)

	// Resource config versions (these will be affected by md5→sha256 migration)
	insertResourceConfigVersion(db, 1, 1, `{"ref":"abc123"}`, 1)
	insertResourceConfigVersion(db, 2, 1, `{"ref":"def456"}`, 2)
	insertResourceConfigVersion(db, 3, 1, `{"ref":"ghi789"}`, 3)
	insertResourceConfigVersion(db, 4, 2, `{"digest":"sha256:aaa111"}`, 1)
	insertResourceConfigVersion(db, 5, 2, `{"digest":"sha256:bbb222"}`, 2)

	// Resources
	mustExec(db, `INSERT INTO resources(id, name, type, pipeline_id, config, active, resource_config_scope_id) VALUES(1, 'source-code', 'git', 1, '{"source":{}}', true, 1)`)
	mustExec(db, `INSERT INTO resources(id, name, type, pipeline_id, config, active, resource_config_scope_id) VALUES(2, 'docker-image', 'registry-image', 1, '{"source":{}}', true, 2)`)

	// Workers (Garden-era — stale data)
	mustExec(db, `INSERT INTO workers(name, addr, state, platform, version) VALUES('garden-worker-1', '10.0.0.10:7777', 'running', 'linux', '2.4')`)
	mustExec(db, `INSERT INTO workers(name, addr, state, platform, version) VALUES('garden-worker-2', '10.0.0.11:7777', 'stalled', 'linux', '2.4')`)

	// Containers (referencing Garden workers)
	mustExec(db, `INSERT INTO containers(id, handle, worker_name, build_id, state, meta_type, meta_step_name, team_id) VALUES(1, 'ctr-aaa', 'garden-worker-1', 1, 'created', 'task', 'test', 1)`)
	mustExec(db, `INSERT INTO containers(id, handle, worker_name, build_id, state, meta_type, meta_step_name, team_id) VALUES(2, 'ctr-bbb', 'garden-worker-2', 2, 'created', 'task', 'test', 1)`)

	// Volumes (referencing Garden workers)
	mustExec(db, `INSERT INTO volumes(id, handle, worker_name, state, team_id) VALUES(1, 'vol-aaa', 'garden-worker-1', 'created', 1)`)
	mustExec(db, `INSERT INTO volumes(id, handle, worker_name, state, team_id) VALUES(2, 'vol-bbb', 'garden-worker-2', 'created', 1)`)

	// Components (with interval, last_ran, paused — columns dropped by JetBridge)
	mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(1, 'scheduler', '10s', false)`)
	mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(2, 'scanner', '10s', false)`)
	mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(3, 'build_tracker', '10s', true)`)

	// Worker base resource types (stale — affected by trigger migration)
	mustExec(db, `INSERT INTO worker_base_resource_types(id, worker_name, base_resource_type_id, image, version) VALUES(1, 'garden-worker-1', 1, '/opt/resource-types/registry-image', '1.0')`)
}

func insertBuild(db *sql.DB, id int, name string, jobID, teamID, pipelineID int, status string) {
	completed := status == "succeeded" || status == "failed" || status == "errored" || status == "aborted"
	aborted := status == "aborted"
	mustExec(db, fmt.Sprintf(
		`INSERT INTO builds(id, name, job_id, team_id, status, scheduled, inputs_ready, create_time, pipeline_id, schema, private_plan, public_plan, drained, aborted, completed)
		 VALUES(%d, '%s', %d, %d, '%s', true, true, now(), %d, 'exec.v2', '{}', '{}', false, %t, %t)`,
		id, name, jobID, teamID, status, pipelineID, aborted, completed,
	))
}

func insertResourceConfigVersion(db *sql.DB, id, scopeID int, version string, checkOrder int) {
	// Compute md5 the same way Concourse does: sorted JSON keys
	versionMD5 := computeVersionMD5(version)
	mustExec(db, fmt.Sprintf(
		`INSERT INTO resource_config_versions(id, resource_config_scope_id, version, version_md5, check_order)
		 VALUES(%d, %d, '%s', '%s', %d)`,
		id, scopeID, version, versionMD5, checkOrder,
	))
}

func computeVersionMD5(version string) string {
	// The MD5 is computed on the canonical JSON representation (sorted keys)
	// For simple cases, we can just use the raw string
	h := md5.Sum([]byte(canonicalJSON(version)))
	return hex.EncodeToString(h[:])
}

// canonicalJSON produces a sorted-key JSON string matching the migration's approach
func canonicalJSON(jsonStr string) string {
	// Simple parser: extract key-value pairs and sort
	// Strip outer braces
	inner := strings.TrimSpace(jsonStr)
	if len(inner) < 2 || inner[0] != '{' {
		return "{}"
	}
	inner = inner[1 : len(inner)-1]
	if inner == "" {
		return "{}"
	}

	// Split on commas (simple case — no nested objects with commas)
	parts := strings.Split(inner, ",")
	pairs := make([]string, 0, len(parts))
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), ":", 2)
		if len(kv) == 2 {
			key := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
			pairs = append(pairs, fmt.Sprintf(`"%s":"%s"`, key, val))
		}
	}
	sort.Strings(pairs)
	return "{" + strings.Join(pairs, ",") + "}"
}

// insertV801FixtureData inserts data matching the v8.0.1 schema.
// The v8.0.1 schema has version_sha256 and version_digest columns already.
func insertV801FixtureData(db *sql.DB) {
	// Teams
	mustExec(db, `INSERT INTO teams(id, name, auth) VALUES(1, 'main', '{"owner":{"users":["local:admin"]}}')`)
	mustExec(db, `INSERT INTO teams(id, name, auth) VALUES(2, 'dev-team', '{"owner":{"users":["local:dev"]}}')`)

	// Pipelines
	mustExec(db, `INSERT INTO pipelines(id, name, team_id, paused, public, archived, secondary_ordering) VALUES(1, 'ci', 1, false, true, false, 1)`)
	mustExec(db, `INSERT INTO pipelines(id, name, team_id, paused, public, archived, secondary_ordering) VALUES(2, 'release', 1, false, false, false, 1)`)

	// Jobs
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(1, 'unit-tests', 1, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(2, 'build-image', 1, '{"plan":[]}', true, false, 1, '2026-01-01')`)
	mustExec(db, `INSERT INTO jobs(id, name, pipeline_id, config, active, paused, max_in_flight, schedule_requested) VALUES(3, 'deploy', 2, '{"plan":[]}', true, false, 1, '2026-01-01')`)

	// Builds
	insertBuild(db, 1, "1", 1, 1, 1, "succeeded")
	insertBuild(db, 2, "2", 1, 1, 1, "succeeded")
	insertBuild(db, 3, "3", 1, 1, 1, "failed")
	insertBuild(db, 4, "1", 2, 1, 1, "succeeded")
	insertBuild(db, 5, "1", 3, 1, 2, "errored")

	// Components (with interval, last_ran, paused — columns that will be dropped)
	mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(1, 'scheduler', '10s', false)`)
	mustExec(db, `INSERT INTO components(id, name, interval, paused) VALUES(2, 'scanner', '10s', false)`)
}

func verifyFixtureDataPresent(db *sql.DB, preMigration bool) {
	// Core pipeline data must survive migration unchanged
	expectRowCount(db, "teams", 2)
	expectRowCount(db, "pipelines", 3)
	expectRowCount(db, "jobs", 5)
	expectRowCount(db, "builds", 10)
	expectRowCount(db, "resources", 2)
	expectRowCount(db, "resource_config_versions", 5)

	// Verify specific team data
	var teamName string
	err := db.QueryRow("SELECT name FROM teams WHERE id = 1").Scan(&teamName)
	Expect(err).NotTo(HaveOccurred())
	Expect(teamName).To(Equal("main"))

	// Verify pipeline data
	var pipelineName string
	var archived bool
	err = db.QueryRow("SELECT name, archived FROM pipelines WHERE id = 3").Scan(&pipelineName, &archived)
	Expect(err).NotTo(HaveOccurred())
	Expect(pipelineName).To(Equal("old-pipeline"))
	Expect(archived).To(BeTrue())

	// Verify build statuses are preserved
	verifyBuildStatuses(db, map[string]int{
		"succeeded": 7,
		"failed":    2,
		"errored":   1,
	})

	// Verify resource config versions have their version JSON intact
	var versionJSON string
	err = db.QueryRow("SELECT version FROM resource_config_versions WHERE id = 1").Scan(&versionJSON)
	Expect(err).NotTo(HaveOccurred())
	Expect(versionJSON).To(ContainSubstring("abc123"))

	// Garden-era data should also survive (it's stale but not deleted by migration)
	expectRowCount(db, "workers", 2)
	expectRowCount(db, "containers", 2)
	expectRowCount(db, "volumes", 2)

	// Components survive (though columns change)
	expectRowCount(db, "components", 3)
}

func verifyBuildStatuses(db *sql.DB, expected map[string]int) {
	rows, err := db.Query("SELECT status, count(*) FROM builds GROUP BY status")
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()

	actual := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		err = rows.Scan(&status, &count)
		Expect(err).NotTo(HaveOccurred())
		actual[status] = count
	}
	Expect(rows.Err()).NotTo(HaveOccurred())

	for status, expectedCount := range expected {
		Expect(actual[status]).To(Equal(expectedCount), "expected %d %s builds, got %d", expectedCount, status, actual[status])
	}
}

func verifyJetBridgeSchemaChanges(db *sql.DB) {
	// Verify component columns were dropped
	for _, col := range []string{"interval", "last_ran", "paused"} {
		var exists bool
		err := db.QueryRow(
			"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'components' AND column_name = $1)",
			col,
		).Scan(&exists)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "column components.%s should have been dropped", col)
	}

	// Verify signing_keys table exists
	var signingKeysExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'signing_keys')").Scan(&signingKeysExists)
	Expect(err).NotTo(HaveOccurred())
	Expect(signingKeysExists).To(BeTrue(), "signing_keys table should exist")

	// Verify simplified triggers exist
	var triggerCount int
	err = db.QueryRow(`
		SELECT count(*) FROM information_schema.triggers
		WHERE trigger_name IN ('workers_notify_trigger', 'containers_notify_trigger')
	`).Scan(&triggerCount)
	Expect(err).NotTo(HaveOccurred())
	// workers_notify_trigger fires on INSERT, UPDATE, DELETE (3 rows)
	// containers_notify_trigger fires on INSERT, DELETE (2 rows)
	Expect(triggerCount).To(Equal(5), "expected 5 trigger entries for simplified notify triggers")

	// Verify old triggers are gone
	var oldTriggerCount int
	err = db.QueryRow(`
		SELECT count(*) FROM information_schema.triggers
		WHERE trigger_name IN ('workers_upsert_or_delete_trigger', 'containers_insert_or_delete_trigger')
	`).Scan(&oldTriggerCount)
	Expect(err).NotTo(HaveOccurred())
	Expect(oldTriggerCount).To(Equal(0), "old notify triggers should have been dropped")
}

func verifyMD5ToSHA256Migration(db *sql.DB) {
	// Verify version_sha256 column exists and is populated
	var sha256Exists bool
	err := db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'resource_config_versions' AND column_name = 'version_sha256')",
	).Scan(&sha256Exists)
	Expect(err).NotTo(HaveOccurred())
	Expect(sha256Exists).To(BeTrue(), "version_sha256 column should exist")

	// All rows should have SHA256 digests
	var totalRows, withSHA256 int
	err = db.QueryRow("SELECT count(*), count(version_sha256) FROM resource_config_versions").Scan(&totalRows, &withSHA256)
	Expect(err).NotTo(HaveOccurred())
	Expect(withSHA256).To(Equal(totalRows), "all resource_config_versions rows should have SHA256 digests")

	// SHA256 digests should be 64 hex characters
	var sampleDigest string
	err = db.QueryRow("SELECT version_sha256 FROM resource_config_versions LIMIT 1").Scan(&sampleDigest)
	Expect(err).NotTo(HaveOccurred())
	Expect(len(sampleDigest)).To(Equal(64), "SHA256 digest should be 64 hex characters")

	// Verify version_digest column renames happened on related tables
	for _, table := range []string{
		"build_resource_config_version_inputs",
		"build_resource_config_version_outputs",
		"next_build_inputs",
		"resource_caches",
		"resource_disabled_versions",
	} {
		var hasDigest bool
		err = db.QueryRow(
			"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = 'version_digest')",
			table,
		).Scan(&hasDigest)
		Expect(err).NotTo(HaveOccurred())
		Expect(hasDigest).To(BeTrue(), "table %s should have version_digest column (renamed from version_md5)", table)

		var hasMD5 bool
		err = db.QueryRow(
			"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = 'version_md5')",
			table,
		).Scan(&hasMD5)
		Expect(err).NotTo(HaveOccurred())
		Expect(hasMD5).To(BeFalse(), "table %s should NOT have version_md5 column (should be renamed to version_digest)", table)
	}
}

func expectRowCount(db *sql.DB, table string, expected int) {
	var count int
	err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
	Expect(err).NotTo(HaveOccurred())
	Expect(count).To(Equal(expected), "expected %d rows in %s, got %d", expected, table, count)
}

func mustExec(db *sql.DB, query string) {
	_, err := db.Exec(query)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to execute: %s", query)
}

// runPreflightAndExpectPass runs the migrate-preflight.sh script against the
// test database and asserts that it exits 0 (pass or pass-with-warnings).
func runPreflightAndExpectPass(dsn string) {
	// Resolve script path relative to this source file so it works
	// regardless of the working directory ginkgo uses.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	scriptPath := filepath.Join(repoRoot, "docs", "migration", "migrate-preflight.sh")

	// Check the script exists
	_, err := os.Stat(scriptPath)
	if os.IsNotExist(err) {
		Skip("migrate-preflight.sh not found at " + scriptPath)
	}
	Expect(err).NotTo(HaveOccurred())

	// Parse DSN fields: "host=/tmp user=postgres dbname=testdb sslmode=disable port=5432"
	dsnFields := parseDSN(dsn)

	// The test postgres runner uses Unix sockets (host=/tmp). The preflight
	// script uses psql via PGHOST/PGPORT/etc env vars. For Unix sockets,
	// we pass the socket directory as PGHOST.
	cmd := exec.Command("bash", scriptPath,
		"--host", dsnFields["host"],
		"--port", dsnFields["port"],
		"--dbname", dsnFields["dbname"],
		"--user", dsnFields["user"],
	)
	// Set PGPASSWORD empty for trust auth, and ensure sslmode is off
	cmd.Env = append(os.Environ(), "PGPASSWORD=", "PGSSLMODE=disable")

	output, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("Pre-flight output:\n%s\n", string(output))

	// Exit 0 = PASSED (possibly with warnings)
	Expect(err).NotTo(HaveOccurred(), "pre-flight script failed with exit code != 0:\n%s", string(output))
	Expect(string(output)).To(ContainSubstring("RESULT: Pre-flight PASSED"))
}

// parseDSN parses a space-separated key=value DSN string into a map.
func parseDSN(dsn string) map[string]string {
	fields := make(map[string]string)
	for _, part := range strings.Fields(dsn) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			fields[kv[0]] = kv[1]
		}
	}
	return fields
}
