package postgresrunner

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestNewRunIDIsSafeBoundedAndCarriesCreationTime(t *testing.T) {
	got, err := newRunID(time.Unix(1_786_000_000, 0), 4242, bytes.NewReader([]byte{0xaa, 0xbb, 0xcc, 0xdd}))
	if err != nil {
		t.Fatal(err)
	}
	if got != "t1786000000_p4242_aabbccdd" {
		t.Fatalf("run ID = %q", got)
	}
	if !identifierPattern.MatchString("cc_tpl_" + got) {
		t.Fatalf("unsafe identifier")
	}
	if len("cc_db_"+got+"_n99_s999999") > 63 {
		t.Fatalf("identifier too long")
	}
}

func TestDSNForDatabasePreservesKeywordAndURLConfiguration(t *testing.T) {
	tests := []struct{ input, name, want string }{
		{
			"host=db port=5432 user=u dbname=postgres sslmode=require connect_timeout=5",
			"cc_db_x_n1_s1",
			"host=db port=5432 user=u dbname=cc_db_x_n1_s1 sslmode=require connect_timeout=5",
		},
		{
			"postgres://u:p@db:5432/postgres?sslmode=require&connect_timeout=5",
			"cc_db_x_n1_s1",
			"postgres://u:p@db:5432/cc_db_x_n1_s1?connect_timeout=5&sslmode=require",
		},
	}
	for _, tt := range tests {
		got, err := dsnForDatabase(tt.input, tt.name)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Fatalf("dsn = %q, want %q", got, tt.want)
		}
	}
}

func TestDSNForDatabaseReplacesExactlyOneKeywordDatabaseAndPreservesEscapes(t *testing.T) {
	input := `host='db host' password=escaped\ value dbname='old db' sslmode=verify-full`
	got, err := dsnForDatabase(input, "cc_db_x_n1_s1")
	if err != nil {
		t.Fatal(err)
	}
	if got != `host='db host' password=escaped\ value dbname=cc_db_x_n1_s1 sslmode=verify-full` {
		t.Fatalf("dsn = %q", got)
	}
	if strings.Count(got, "dbname=") != 1 {
		t.Fatalf("expected exactly one dbname token in %q", got)
	}
}

func TestDSNForDatabaseAcceptsLibpqWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"spaces around equals",
			"host = db dbname = postgres sslmode = disable",
			"host = db dbname = cc_db_x_n1_s1 sslmode = disable",
		},
		{
			"quoted values and spaces around equals",
			"host = 'db host' dbname = 'old db' sslmode = disable",
			"host = 'db host' dbname = cc_db_x_n1_s1 sslmode = disable",
		},
		{
			"vertical tabs",
			"host\v=\vdb\vdbname\v=\vpostgres\vsslmode\v=\vdisable",
			"host\v=\vdb\vdbname\v=\vcc_db_x_n1_s1\vsslmode\v=\vdisable",
		},
		{
			"form feeds",
			"host\f=\fdb\fdbname\f=\fpostgres\fsslmode\f=\fdisable",
			"host\f=\fdb\fdbname\f=\fcc_db_x_n1_s1\fsslmode\f=\fdisable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := pgx.ParseConfig(tt.input); err != nil {
				t.Fatalf("test input is not accepted by pgx: %v", err)
			}
			got, err := dsnForDatabase(tt.input, "cc_db_x_n1_s1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("dsn = %q, want %q", got, tt.want)
			}
			config, err := pgx.ParseConfig(got)
			if err != nil {
				t.Fatalf("rewritten DSN is not accepted by pgx: %v", err)
			}
			if config.Database != "cc_db_x_n1_s1" {
				t.Fatalf("rewritten database = %q", config.Database)
			}
		})
	}
}

func TestAdoptSuiteConfigValidatesNamesNodeAndState(t *testing.T) {
	valid := SuiteConfig{
		AdminDSN:     DefaultAdminDSN,
		RunID:        "t1786000000_p4242_aabbccdd",
		TemplateName: "cc_tpl_t1786000000_p4242_aabbccdd",
		CreatedUnix:  1_786_000_000,
	}

	tests := []struct {
		name   string
		config SuiteConfig
		node   int
	}{
		{"unsafe run ID", func() SuiteConfig { c := valid; c.RunID = "../bad"; return c }(), 1},
		{"unsafe template", func() SuiteConfig { c := valid; c.TemplateName = "cc_tpl_bad-name"; return c }(), 1},
		{"mismatched template", func() SuiteConfig { c := valid; c.TemplateName = "cc_tpl_t1786000000_p4242_eeeeeeee"; return c }(), 1},
		{"zero node", valid, 0},
		{"negative node", valid, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runner Runner
			if err := runner.AdoptSuiteConfig(tt.config, tt.node); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	var runner Runner
	if err := runner.AdoptSuiteConfig(valid, 7); err != nil {
		t.Fatal(err)
	}
	if runner.Port != 15432 {
		t.Fatalf("port = %d, want 15432", runner.Port)
	}
	if runner.DatabaseName() != "" {
		t.Fatalf("database name before allocation = %q", runner.DatabaseName())
	}
	if _, err := runner.activeDSN(); err == nil {
		t.Fatal("expected no-active-database error")
	}

	runner.state.currentDB = "cc_db_t1786000000_p4242_aabbccdd_n7_s1"
	if _, err := runner.reserveDatabase(); err == nil {
		t.Fatal("expected second-active-database error")
	}
}

func requireSharedPostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CONCOURSE_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = DefaultAdminDSN
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatalf("shared PostgreSQL unavailable: %v; run make test-postgres-up", err)
	}
	return dsn
}

func createSharedSuite(t *testing.T) (*Runner, SuiteConfig) {
	t.Helper()
	requireSharedPostgres(t)
	runner := &Runner{}
	config, err := runner.CreateSuiteTemplate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.CleanupSuite(context.Background()); err != nil {
			t.Errorf("cleanup suite: %v", err)
		}
	})
	return runner, config
}

func openPGX(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func databaseExists(t *testing.T, adminDSN, name string) bool {
	t.Helper()
	admin := openPGX(t, adminDSN)
	var exists bool
	if err := admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func createDatabase(t *testing.T, adminDSN, name string) {
	t.Helper()
	if err := validateIdentifier(name); err != nil {
		t.Fatal(err)
	}
	admin := openPGX(t, adminDSN)
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
}

func dropDatabaseForTest(t *testing.T, adminDSN, name string) {
	t.Helper()
	if err := validateIdentifier(name); err != nil {
		t.Fatal(err)
	}
	admin := openPGX(t, adminDSN)
	_, _ = admin.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = $1`, name)
	if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Errorf("drop test database %s: %v", name, err)
	}
}

func TestRunnerClonesAnIsolatedDatabaseForEveryCycle(t *testing.T) {
	runner, config := createSharedSuite(t)

	templateDSN, err := dsnForDatabase(config.AdminDSN, config.TemplateName)
	if err != nil {
		t.Fatal(err)
	}
	template := openPGX(t, templateDSN)
	if _, err := template.Exec(context.Background(), `CREATE TABLE isolation_probe (value text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := template.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := runner.createTestDBFromTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dbA := openPGX(t, mustActiveDSN(t, runner))
	if _, err := dbA.Exec(context.Background(), `INSERT INTO isolation_probe (value) VALUES ('only-a')`); err != nil {
		t.Fatal(err)
	}
	if err := dbA.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.dropTestDB(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := runner.createTestDBFromTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dbB := openPGX(t, mustActiveDSN(t, runner))
	var count int
	if err := dbB.QueryRow(context.Background(), `SELECT count(*) FROM isolation_probe`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("clone B inherited %d rows from clone A", count)
	}
}

func TestTwoNodesAdoptOneTemplateAndCreateDistinctClones(t *testing.T) {
	node1, config := createSharedSuite(t)
	node2 := &Runner{}
	if err := node2.AdoptSuiteConfig(config, 2); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, runner := range []*Runner{node1, node2} {
		go func(r *Runner) {
			<-start
			errs <- r.createTestDBFromTemplate(context.Background())
		}(runner)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	name1, name2 := node1.DatabaseName(), node2.DatabaseName()
	if !strings.Contains(name1, "cc_db_"+config.RunID+"_n1_") {
		t.Fatalf("node 1 database = %q", name1)
	}
	if !strings.Contains(name2, "cc_db_"+config.RunID+"_n2_") {
		t.Fatalf("node 2 database = %q", name2)
	}
	if name1 == name2 {
		t.Fatalf("nodes share database %q", name1)
	}
}

func TestRunnerRejectsASecondActiveDatabase(t *testing.T) {
	runner, _ := createSharedSuite(t)
	if err := runner.createTestDBFromTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.createTestDBFromTemplate(context.Background()); err == nil {
		t.Fatal("expected second active database to be rejected")
	}
}

func TestRunnerSerializesConcurrentCreateRequestsWithoutLeakingAClone(t *testing.T) {
	runner, config := createSharedSuite(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- runner.createTestDBFromTemplate(context.Background())
		}()
	}
	close(start)
	var successes, failures int
	for range 2 {
		if err := <-errs; err != nil {
			failures++
		} else {
			successes++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("create results: %d successes, %d failures", successes, failures)
	}
	if err := runner.dropTestDB(context.Background()); err != nil {
		t.Fatal(err)
	}

	admin := openPGX(t, config.AdminDSN)
	prefix := "cc_db_" + config.RunID + "_"
	var clones int
	if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_database WHERE left(datname, length($1)) = $1`, prefix).Scan(&clones); err != nil {
		t.Fatal(err)
	}
	if clones != 0 {
		t.Fatalf("found %d leaked clones", clones)
	}
}

func TestDropTestDBTerminatesActiveAndIdleInTransactionBackends(t *testing.T) {
	runner, _ := createSharedSuite(t)
	if err := runner.createEmptyTestDB(context.Background()); err != nil {
		t.Fatal(err)
	}
	dsn := mustActiveDSN(t, runner)
	idle := openPGX(t, dsn)
	tx, err := idle.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	active := openPGX(t, dsn)
	sleepDone := make(chan error, 1)
	go func() {
		_, err := active.Exec(context.Background(), `SELECT pg_sleep(30)`)
		sleepDone <- err
	}()

	admin := openPGX(t, configAdminDSN(t, runner))
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname = $1`, runner.DatabaseName()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backends did not become visible")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := runner.dropTestDB(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-sleepDone; err == nil {
		t.Fatal("active backend remained usable")
	}
	if _, err := tx.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("idle-in-transaction backend remained usable")
	}
}

func TestCleanupSuiteDiscoversResidualClonesBeforeDroppingTemplate(t *testing.T) {
	runner, config := createSharedSuite(t)
	if err := runner.createEmptyTestDB(context.Background()); err != nil {
		t.Fatal(err)
	}
	clone := runner.DatabaseName()
	runner.state.mu.Lock()
	runner.state.currentDB = ""
	runner.state.ownedDBs = map[string]struct{}{}
	runner.state.mu.Unlock()

	if err := runner.CleanupSuite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if databaseExists(t, config.AdminDSN, clone) {
		t.Fatalf("residual clone %q still exists", clone)
	}
	if databaseExists(t, config.AdminDSN, config.TemplateName) {
		t.Fatalf("template %q still exists", config.TemplateName)
	}
}

func TestReapExpiredRunsDropsOnlyOwnedPrefixesOlderThan24Hours(t *testing.T) {
	adminDSN := requireSharedPostgres(t)
	admin := openPGX(t, adminDSN)
	if _, err := admin.Exec(context.Background(), `SELECT pg_advisory_lock($1)`, reaperAdvisoryLockID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, reaperAdvisoryLockID); err != nil {
			t.Errorf("unlock production reaper fixture: %v", err)
		}
	})

	now := time.Unix(1_786_000_000, 0)
	pid := os.Getpid()
	oldRun, err := newRunID(now.Add(-25*time.Hour), pid, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := newRunID(now.Add(-23*time.Hour), pid, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	processSuffix := oldRun[len(oldRun)-8:]
	oldTemplate := "cc_tpl_" + oldRun
	oldClone := "cc_db_" + oldRun + "_n1_s1"
	newTemplate := "cc_tpl_" + newRun
	unrelated := fmt.Sprintf("cc_unrelated_database_%d", pid)
	overflowEpoch := fmt.Sprintf("cc_tpl_t9999999999999999999_p%d_%s", pid, processSuffix)
	zeroPID := fmt.Sprintf("cc_tpl_t%d_p0_%s", now.Add(-25*time.Hour).Unix(), processSuffix)
	overflowNode := "cc_db_" + oldRun + "_n9223372036854775808_s1"
	overflowSerial := "cc_db_" + oldRun + "_n1_s18446744073709551616"
	for _, name := range []string{oldTemplate, oldClone, newTemplate, unrelated, overflowEpoch, zeroPID, overflowNode, overflowSerial} {
		createDatabase(t, adminDSN, name)
		name := name
		t.Cleanup(func() { dropDatabaseForTest(t, adminDSN, name) })
	}

	if err := reapExpiredRunsOnConn(context.Background(), admin, now); err != nil {
		t.Fatal(err)
	}
	if databaseExists(t, adminDSN, oldClone) || databaseExists(t, adminDSN, oldTemplate) {
		t.Fatal("expired run databases remain")
	}
	if !databaseExists(t, adminDSN, newTemplate) {
		t.Fatal("new run template was reaped")
	}
	if !databaseExists(t, adminDSN, unrelated) {
		t.Fatal("unrelated database was reaped")
	}
	if !databaseExists(t, adminDSN, overflowEpoch) || !databaseExists(t, adminDSN, zeroPID) {
		t.Fatal("a name the runner could not generate was reaped")
	}
	if !databaseExists(t, adminDSN, overflowNode) || !databaseExists(t, adminDSN, overflowSerial) {
		t.Fatal("a clone with an unrepresentable node or serial was reaped")
	}
}

func TestCreateSuiteTemplateReportsSetupAndRollbackFailures(t *testing.T) {
	adminDSN := requireSharedPostgres(t)
	admin := openPGX(t, adminDSN)
	adminConfig, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("cc_rollback_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := validateIdentifier(role); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), "CREATE ROLE "+role+" LOGIN CREATEDB"); err != nil {
		t.Fatal(err)
	}
	var templateName string
	t.Cleanup(func() {
		if templateName != "" {
			dropDatabaseForTest(t, adminDSN, templateName)
		}
		if _, err := admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+role); err != nil {
			t.Errorf("drop rollback test role: %v", err)
		}
	})

	restrictedDSN := fmt.Sprintf(
		"host=%s port=%d user=%s dbname=postgres sslmode=disable",
		adminConfig.Host,
		adminConfig.Port,
		role,
	)
	t.Setenv("CONCOURSE_TEST_POSTGRES_DSN", restrictedDSN)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := (&Runner{}).CreateSuiteTemplate(ctx)
		errCh <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for templateName == "" {
		select {
		case err := <-errCh:
			t.Fatalf("suite setup returned before rollback failure was installed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("timed out waiting for suite template owned by %s", role)
		}
		err := admin.QueryRow(context.Background(), `
			SELECT datname
			FROM pg_database
			WHERE datdba = (SELECT oid FROM pg_roles WHERE rolname = $1)
			  AND left(datname, length('cc_tpl_')) = 'cc_tpl_'
			ORDER BY datname
			LIMIT 1`, role).Scan(&templateName)
		if err != nil && err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		if templateName == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := admin.Exec(context.Background(), "ALTER DATABASE "+templateName+" OWNER TO postgres"); err != nil {
		t.Fatal(err)
	}
	cancel()
	err = <-errCh
	if err == nil {
		t.Fatal("expected suite setup and rollback to fail")
	}
	if !strings.Contains(err.Error(), "migrate suite template") || !strings.Contains(err.Error(), "permission denied to create extension") {
		t.Fatalf("missing setup failure in %v", err)
	}
	if !strings.Contains(err.Error(), "rollback suite template") || !strings.Contains(err.Error(), "must be owner of database") {
		t.Fatalf("missing rollback failure in %v", err)
	}
}

func TestCleanupRefusesIdentifiersOutsideTheRunNamespace(t *testing.T) {
	runner, config := createSharedSuite(t)
	runner.state.mu.Lock()
	runner.state.ownedDBs["postgres"] = struct{}{}
	runner.state.mu.Unlock()
	if err := runner.CleanupProcess(context.Background()); err == nil {
		t.Fatal("expected cleanup to reject postgres")
	}
	runner.state.mu.Lock()
	delete(runner.state.ownedDBs, "postgres")
	runner.state.mu.Unlock()
	if !databaseExists(t, config.AdminDSN, "postgres") {
		t.Fatal("cleanup dropped the admin database")
	}
}

func TestCleanupDoesNotTreatUnderscoresInRunIDAsWildcards(t *testing.T) {
	runner, config := createSharedSuite(t)
	prefix := "cc_db_" + config.RunID + "_"
	wildcardAt := strings.Index(prefix, "_p")
	if wildcardAt < 0 {
		t.Fatalf("run prefix %q has no underscore before pid", prefix)
	}
	unrelated := prefix[:wildcardAt] + "x" + prefix[wildcardAt+1:] + "n9_s9"
	createDatabase(t, config.AdminDSN, unrelated)
	t.Cleanup(func() { dropDatabaseForTest(t, config.AdminDSN, unrelated) })

	if err := runner.CleanupSuite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !databaseExists(t, config.AdminDSN, unrelated) {
		t.Fatalf("cleanup treated underscores as wildcards and dropped %q", unrelated)
	}
}

func TestReaperWaitsForTheMachineWideAdvisoryLock(t *testing.T) {
	adminDSN := requireSharedPostgres(t)
	locker := openPGX(t, adminDSN)
	if _, err := locker.Exec(context.Background(), `SELECT pg_advisory_lock($1)`, reaperAdvisoryLockID); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := reapExpiredRuns(short, adminDSN, time.Now()); err == nil {
		t.Fatal("reaper entered while advisory lock was held")
	}
	if _, err := locker.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, reaperAdvisoryLockID); err != nil {
		t.Fatal(err)
	}
	if err := reapExpiredRuns(context.Background(), adminDSN, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func mustActiveDSN(t *testing.T, runner *Runner) string {
	t.Helper()
	dsn, err := runner.activeDSN()
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func configAdminDSN(t *testing.T, runner *Runner) string {
	t.Helper()
	if runner.state == nil {
		t.Fatal("runner is not configured")
	}
	return runner.state.suite.AdminDSN
}
