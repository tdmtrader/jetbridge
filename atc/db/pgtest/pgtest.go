// Package pgtest gives a plain Go test a real, migrated, isolated PostgreSQL
// database.
//
// atc/postgresrunner already does this for Ginkgo suites, but it cannot be
// called from a `func TestX(t *testing.T)`: every helper asserts through Gomega
// and panics outside a spec, its output goes to ginkgo.GinkgoWriter, and the
// postgres process is supervised by ginkgomon. It also hardcodes the database
// name "testdb", so there is exactly one test database at a time, handed out by
// a package-wide BeforeEach that every spec in the package pays for whether it
// touches the database or not.
//
// pgtest is additive: it shares no state with postgresrunner and changes
// nothing about it. It runs its own postmaster on its own ephemeral port, with
// its own jbt_-prefixed names, so the two cannot collide even in one process.
//
// The cost model is what makes this usable from a fast package: one postmaster
// and one migration pass per test *process*, then a cheap CREATE DATABASE ...
// TEMPLATE per test that asks for one. Tests that do not call OpenTestDB pay
// nothing at all.
//
// An adopting package must wire shutdown, or the postmaster outlives the test
// binary and squats its data directory — that is a real failure mode, not a
// hypothetical:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
package pgtest

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/db"
)

const (
	// templateName is migrated once per process and then only ever copied.
	templateName = "jbt_tpl"
	// readyTimeout bounds initdb + postmaster startup, not migrations.
	readyTimeout = 60 * time.Second
)

type instance struct {
	port    int
	dataDir string
	cmd     *exec.Cmd
	err     error
}

var (
	once     sync.Once
	shared   *instance
	dbSerial atomic.Int64
	// dsnByConn lets the seeding helpers open the extra raw connections a
	// lock.LockFactory needs, without widening OpenTestDB's signature.
	dsnByConn sync.Map
)

// Main runs the package's tests and then shuts the postmaster down. Without it
// a passing run still leaves a postgres process holding its port.
func Main(m *testing.M) int {
	code := m.Run()
	shutdown()
	return code
}

// OpenTestDB returns a migrated database private to this test. The database is
// dropped and the connection closed when the test finishes.
//
// The first call in a process starts a postmaster and runs the full migration
// set to build a template; later calls copy that template, which is cheap.
func OpenTestDB(t *testing.T) db.DbConn {
	t.Helper()

	inst := start(t)

	name := fmt.Sprintf("jbt_db_%d", dbSerial.Add(1))
	admin := adminConn(t, inst)
	defer admin.Close()

	// Copying a template fails outright if any backend is still attached to
	// it, so a leaked connection from an earlier test shows up here rather
	// than as a corrupt fixture.
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName)); err != nil {
		t.Fatalf("pgtest: create %s from template: %v", name, err)
	}

	conn, err := openConn(inst, name)
	if err != nil {
		t.Fatalf("pgtest: open %s: %v", name, err)
	}

	dsnByConn.Store(conn, dsn(inst, name))

	t.Cleanup(func() {
		dsnByConn.Delete(conn)
		_ = conn.Close()
		dropAdmin, err := sql.Open("pgx", dsn(inst, "postgres"))
		if err != nil {
			t.Logf("pgtest: reopen admin to drop %s: %v", name, err)
			return
		}
		defer dropAdmin.Close()
		// Terminate every backend, not just idle ones. postgresrunner filters
		// on state='idle' (postgresrunner.go), which leaves an active or
		// idle-in-transaction backend attached and makes the drop fail.
		if _, err := dropAdmin.Exec(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			 WHERE pid <> pg_backend_pid() AND datname = $1`, name,
		); err != nil {
			t.Logf("pgtest: terminate backends on %s: %v", name, err)
		}
		if _, err := dropAdmin.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
			t.Logf("pgtest: drop %s: %v", name, err)
		}
	})

	return conn
}

// DataSourceName is the DSN of a database private to this test, for the rare
// caller that needs the string rather than an open connection.
func DataSourceName(t *testing.T) string {
	t.Helper()
	OpenTestDB(t)
	return dsn(start(t), fmt.Sprintf("jbt_db_%d", dbSerial.Load()))
}

func start(t *testing.T) *instance {
	t.Helper()
	once.Do(func() { shared = launch() })
	if shared.err != nil {
		t.Fatalf("pgtest: %v", shared.err)
	}
	return shared
}

func launch() *instance {
	inst := &instance{}

	initdbPath, err := exec.LookPath("initdb")
	if err != nil {
		inst.err = fmt.Errorf("initdb not on PATH: %w", err)
		return inst
	}
	postgresPath, err := exec.LookPath("postgres")
	if err != nil {
		inst.err = fmt.Errorf("postgres not on PATH: %w", err)
		return inst
	}

	inst.port, err = freePort()
	if err != nil {
		inst.err = err
		return inst
	}

	// Under the OS temp root, but in our own subtree: postgresrunner's
	// concourse-pg-runner directory has accumulated orphaned data dirs from
	// crashed runs, and pgtest should not add to that pile.
	base := filepath.Join(os.TempDir(), "concourse-pgtest")
	if err := os.MkdirAll(base, 0755); err != nil {
		inst.err = err
		return inst
	}
	inst.dataDir, err = os.MkdirTemp(base, "pg")
	if err != nil {
		inst.err = err
		return inst
	}

	init := exec.Command(initdbPath, "-U", "postgres", "-D", inst.dataDir, "-E", "UTF8", "--no-locale")
	if out, err := init.CombinedOutput(); err != nil {
		inst.err = fmt.Errorf("initdb: %w\n%s", err, out)
		return inst
	}

	// Durability is pointless for a database that lives for one test binary.
	// Same settings postgresrunner uses.
	conf := filepath.Join(inst.dataDir, "postgresql.conf")
	f, err := os.OpenFile(conf, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		inst.err = err
		return inst
	}
	_, err = f.WriteString("\nfsync = off\nsynchronous_commit = off\nfull_page_writes = off\nrandom_page_cost = 1.1\n")
	f.Close()
	if err != nil {
		inst.err = err
		return inst
	}

	inst.cmd = exec.Command(postgresPath,
		"-k", inst.dataDir, "-D", inst.dataDir,
		"-h", "127.0.0.1", "-p", strconv.Itoa(inst.port),
	)
	if err := inst.cmd.Start(); err != nil {
		inst.err = fmt.Errorf("start postgres: %w", err)
		return inst
	}

	if err := waitReady(inst); err != nil {
		_ = inst.cmd.Process.Kill()
		inst.err = err
		return inst
	}

	if err := buildTemplate(inst); err != nil {
		_ = inst.cmd.Process.Kill()
		inst.err = err
		return inst
	}

	return inst
}

func waitReady(inst *instance) error {
	deadline := time.Now().Add(readyTimeout)
	var last error
	for time.Now().Before(deadline) {
		conn, err := sql.Open("pgx", dsn(inst, "postgres"))
		if err == nil {
			last = conn.Ping()
			conn.Close()
			if last == nil {
				return nil
			}
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("postgres not ready within %s: %w", readyTimeout, last)
}

// buildTemplate creates the template database and migrates it. Opening a
// db.DbConn is what runs the migrations — migration is a side effect of
// db.Open, not a separate step.
func buildTemplate(inst *instance) error {
	admin, err := sql.Open("pgx", dsn(inst, "postgres"))
	if err != nil {
		return err
	}
	defer admin.Close()

	if _, err := admin.Exec("CREATE DATABASE " + templateName); err != nil {
		return fmt.Errorf("create template: %w", err)
	}

	conn, err := openConn(inst, templateName)
	if err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	// postgresrunner additionally marks every table UNLOGGED here. That only
	// works if the tables are converted in foreign-key order — otherwise
	// PostgreSQL refuses, because an unlogged table may not reference a logged
	// one — and it buys little once fsync and synchronous_commit are already
	// off above. Not worth the ordering logic.
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close template connection: %w", err)
	}

	// CREATE DATABASE ... TEMPLATE refuses to run while anything is attached
	// to the source, so drain it before the first test asks for a copy.
	if _, err := admin.Exec(
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE pid <> pg_backend_pid() AND datname = $1`, templateName,
	); err != nil {
		return fmt.Errorf("drain template: %w", err)
	}
	return nil
}

func openConn(inst *instance, name string) (db.DbConn, error) {
	conn, err := db.Open(
		lagertest.NewTestLogger("pgtest"),
		"pgx",
		dsn(inst, name),
		nil, nil, "pgtest", nil,
	)
	if err != nil {
		return nil, err
	}
	// Matches postgresrunner: a single connection makes any code path that
	// needs two deadlock rather than pass by luck.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	return conn, nil
}

func adminConn(t *testing.T, inst *instance) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", dsn(inst, "postgres"))
	if err != nil {
		t.Fatalf("pgtest: open admin connection: %v", err)
	}
	return admin
}

func dsn(inst *instance, name string) string {
	return fmt.Sprintf(
		"host=%s user=postgres dbname=%s sslmode=disable port=%d",
		inst.dataDir, name, inst.port,
	)
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func shutdown() {
	if shared == nil || shared.cmd == nil || shared.cmd.Process == nil {
		return
	}
	// SIGQUIT is postgres' immediate shutdown. SIGTERM waits for clients to
	// disconnect, which a failed test may never do.
	_ = shared.cmd.Process.Signal(sigQuit)
	done := make(chan struct{})
	go func() { _, _ = shared.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = shared.cmd.Process.Kill()
	}
	if strings.HasPrefix(shared.dataDir, filepath.Join(os.TempDir(), "concourse-pgtest")) {
		_ = os.RemoveAll(shared.dataDir)
	}
}
