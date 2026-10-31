package migration_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// v801LastMigration is upstream Concourse 8.0.x's last migration and this
// fork's baseline: 1765921815_baseline.up.sql builds its schema from nothing.
const v801LastMigration = 1765921815

// v713LastMigration is upstream Concourse 7.13's last migration, which the
// baseline replaced.
const v713LastMigration = 1666754000

var _ = Describe("Baseline", func() {
	var (
		db          *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		migrator    migration.Migrator
		head        int
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

		head, err = migrator.SupportedVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(head).To(BeNumerically(">", v801LastMigration))
	})

	AfterEach(func() {
		_ = db.Close()
		for _, c := range lockDB {
			c.Close()
		}
	})

	It("is the earliest migration, and has no down", func() {
		migrations, err := migrator.Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(migrations[0].Version).To(Equal(v801LastMigration))
		Expect(migrations[0].Name).To(Equal("1765921815_baseline.up.sql"))
		for _, m := range migrations {
			Expect(m.Direction).To(Equal("up"), m.Name)
		}
	})

	It("is recorded as the only migration a fresh database ran to reach it", func() {
		Expect(migrator.Migrate(nil, nil, v801LastMigration)).To(Succeed())
		ExpectDatabaseMigrationVersionToEqual(migrator, v801LastMigration)
		expectRowCount(db, "migrations_history", 1)
	})

	It("refuses a database that stopped at an upstream release before it", func() {
		SetupMigrationsHistoryTableToExistAtVersion(db, v713LastMigration)
		err := migrator.Up(nil, nil)
		Expect(err).To(MatchError(fmt.Sprintf("database is at migration %d, older than this release's baseline migration %d: upgrade it with JetBridge 0.3.1 first", v713LastMigration, v801LastMigration)))
		ExpectDatabaseMigrationVersionToEqual(migrator, v713LastMigration)
		expectRowCount(db, "migrations_history", 1)
	})

	It("keeps upstream 8.0.1 data through the fork migrations", func() {
		Expect(migrator.Migrate(nil, nil, v801LastMigration)).To(Succeed())
		insertV801FixtureData(db)

		Expect(migrator.Up(nil, nil)).To(Succeed())
		ExpectDatabaseMigrationVersionToEqual(migrator, head)

		expectRowCount(db, "teams", 2)
		expectRowCount(db, "pipelines", 2)
		expectRowCount(db, "jobs", 3)
		expectRowCount(db, "builds", 5)
		verifyBuildStatuses(db, map[string]int{
			"succeeded": 3,
			"failed":    1,
			"errored":   1,
		})
		verifyJetBridgeSchemaChanges(db)
	})

	It("is a no-op to migrate again at HEAD", func() {
		Expect(migrator.Up(nil, nil)).To(Succeed())
		mustExec(db, `INSERT INTO teams(id, name) VALUES(1, 'main')`)
		Expect(migrator.Up(nil, nil)).To(Succeed())
		Expect(migrator.Migrate(nil, nil, head)).To(Succeed())
		ExpectDatabaseMigrationVersionToEqual(migrator, head)
		expectRowCount(db, "teams", 1)
	})

	Describe("the pre-flight script", func() {
		It("passes against an upstream 8.0.1 database", func() {
			Expect(migrator.Migrate(nil, nil, v801LastMigration)).To(Succeed())
			insertV801FixtureData(db)
			runPreflightAndExpectPass(postgresRunner.DataSourceName())
		})

		It("passes against a database already at HEAD", func() {
			Expect(migrator.Up(nil, nil)).To(Succeed())
			runPreflightAndExpectPass(postgresRunner.DataSourceName())
		})

		It("fails a database below the baseline, naming the release that can upgrade it", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, v713LastMigration)
			output, err := runPreflight(postgresRunner.DataSourceName())
			Expect(err).To(HaveOccurred())
			Expect(output).To(ContainSubstring(fmt.Sprintf("Database version (%d) is older than the baseline (%d", v713LastMigration, v801LastMigration)))
			Expect(output).To(ContainSubstring("Upgrade it with JetBridge 0.3.1"))
			Expect(output).To(ContainSubstring("RESULT: Pre-flight FAILED"))
		})
	})
})

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

func insertBuild(db *sql.DB, id int, name string, jobID, teamID, pipelineID int, status string) {
	completed := status == "succeeded" || status == "failed" || status == "errored" || status == "aborted"
	aborted := status == "aborted"
	// drained belongs to the v7 fixture schema this inserts into; migration 1789793162 drops it.
	mustExec(db, fmt.Sprintf(
		`INSERT INTO builds(id, name, job_id, team_id, status, scheduled, inputs_ready, create_time, pipeline_id, schema, private_plan, public_plan, drained, aborted, completed)
		 VALUES(%d, '%s', %d, %d, '%s', true, true, now(), %d, 'exec.v2', '{}', '{}', false, %t, %t)`,
		id, name, jobID, teamID, status, pipelineID, aborted, completed,
	))
}

func verifyBuildStatuses(db *sql.DB, expected map[string]int) {
	GinkgoHelper()

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
	GinkgoHelper()

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

	// signing_keys came with the idtoken credential manager and left with it
	// (migration 1789793165): a database migrated to HEAD has no such table.
	var signingKeysExists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'signing_keys')").Scan(&signingKeysExists)
	Expect(err).NotTo(HaveOccurred())
	Expect(signingKeysExists).To(BeFalse(), "signing_keys table should have been dropped by 1789793165")

	// Verify simplified triggers exist
	var triggerCount int
	err = db.QueryRow(`
		SELECT count(*) FROM information_schema.triggers
		WHERE trigger_name = 'workers_notify_trigger'
	`).Scan(&triggerCount)
	Expect(err).NotTo(HaveOccurred())
	// workers_notify_trigger fires on INSERT, UPDATE, DELETE (3 rows)
	Expect(triggerCount).To(Equal(3), "expected INSERT, UPDATE and DELETE worker trigger entries")

	// Verify old triggers are gone
	var oldTriggerCount int
	err = db.QueryRow(`
		SELECT count(*) FROM information_schema.triggers
		WHERE trigger_name IN ('workers_upsert_or_delete_trigger', 'containers_insert_or_delete_trigger', 'containers_notify_trigger')
	`).Scan(&oldTriggerCount)
	Expect(err).NotTo(HaveOccurred())
	Expect(oldTriggerCount).To(Equal(0), "old notify triggers should have been dropped")
}

func expectRowCount(db *sql.DB, table string, expected int) {
	GinkgoHelper()

	var count int
	err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
	Expect(err).NotTo(HaveOccurred())
	Expect(count).To(Equal(expected), "expected %d rows in %s, got %d", expected, table, count)
}

func mustExec(db *sql.DB, query string) {
	_, err := db.Exec(query)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to execute: %s", query)
}

// preflightTargetVersion reads JETBRIDGE_VERSION out of migrate-preflight.sh.
func preflightTargetVersion() int {
	GinkgoHelper()

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")

	body, err := os.ReadFile(filepath.Join(repoRoot, "docs", "migration", "migrate-preflight.sh"))
	Expect(err).NotTo(HaveOccurred())

	m := regexp.MustCompile(`(?m)^JETBRIDGE_VERSION=(\d+)$`).FindSubmatch(body)
	Expect(m).NotTo(BeNil(), "JETBRIDGE_VERSION= not found in migrate-preflight.sh")

	v, err := strconv.Atoi(string(m[1]))
	Expect(err).NotTo(HaveOccurred())

	return v
}

var _ = Describe("migrate-preflight.sh", func() {
	// The script runs on an operator's laptop before an upgrade, so it cannot
	// ask the binary what the head migration is and has to hardcode it. Nothing
	// syncs the two. Left to drift, the script tells operators their database is
	// "ahead of JetBridge" and refuses an upgrade that is in fact fine.
	//
	// Adding a migration means editing this constant. The failure message is the
	// documentation.
	It("targets the head migration", func() {
		migrator := migration.NewMigrator(nil, nil)

		head, err := migrator.SupportedVersion()
		Expect(err).NotTo(HaveOccurred())

		Expect(preflightTargetVersion()).To(Equal(head),
			"set JETBRIDGE_VERSION in docs/migration/migrate-preflight.sh to %d", head)
	})
})

// runPreflightAndExpectPass runs the migrate-preflight.sh script against the
// test database and asserts that it exits 0 (pass or pass-with-warnings).
func runPreflightAndExpectPass(dsn string) {
	GinkgoHelper()
	output, err := runPreflight(dsn)
	Expect(err).NotTo(HaveOccurred(), "pre-flight script failed with exit code != 0:\n%s", output)
	Expect(output).To(ContainSubstring("RESULT: Pre-flight PASSED"))
}

// runPreflight runs the migrate-preflight.sh script against the test database.
func runPreflight(dsn string) (string, error) {
	GinkgoHelper()
	// Resolve script path relative to this source file so it works
	// regardless of the working directory ginkgo uses.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	scriptPath := filepath.Join(repoRoot, "docs", "migration", "migrate-preflight.sh")
	_, err := os.Stat(scriptPath)
	Expect(err).NotTo(HaveOccurred())

	// The test postgres runner uses Unix sockets (host=/tmp); the script reads
	// the socket directory as its host.
	dsnFields := parseDSN(dsn)
	cmd := exec.Command("bash", scriptPath,
		"--host", dsnFields["host"],
		"--port", dsnFields["port"],
		"--dbname", dsnFields["dbname"],
		"--user", dsnFields["user"],
	)
	cmd.Env = append(os.Environ(), "PGPASSWORD=", "PGSSLMODE=disable")

	output, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("Pre-flight output:\n%s\n", string(output))
	return string(output), err
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
