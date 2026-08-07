package postgresrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/concourse/concourse/atc/db"
)

// StandardTestRunner gives a package built on testing.T the same shared
// PostgreSQL template and clone-per-test isolation as GinkgoRunner.
type StandardTestRunner struct {
	mu sync.Mutex

	owner     Runner
	suite     SuiteConfig
	nextNode  int
	databases map[*testing.T]*standardTestDatabase
}

type standardTestDatabase struct {
	runner *Runner
	conn   db.DbConn
}

// Main initializes one migrated suite template, runs the package, and removes
// the complete suite namespace afterward. It is intended to be called by a
// package's TestMain.
func (r *StandardTestRunner) Main(m *testing.M) int {
	if err := r.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "postgresrunner: start standard test runner: %v\n", err)
		return 1
	}

	code := m.Run()
	if err := r.Stop(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "postgresrunner: stop standard test runner: %v\n", err)
		return 1
	}
	return code
}

// Start creates the suite template used by OpenConn. Main is the usual entry
// point; Start and Stop are exposed for focused tests of this adapter.
func (r *StandardTestRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suite.RunID != "" {
		return fmt.Errorf("standard test runner is already started")
	}

	config, err := r.owner.CreateSuiteTemplate(ctx)
	if err != nil {
		return err
	}
	r.suite = config
	r.nextNode = 0
	r.databases = map[*testing.T]*standardTestDatabase{}
	return nil
}

// Stop removes all residual clones and the suite template.
func (r *StandardTestRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suite.RunID == "" {
		return nil
	}
	if err := r.owner.CleanupSuite(ctx); err != nil {
		return err
	}
	r.suite = SuiteConfig{}
	r.nextNode = 0
	r.databases = nil
	return nil
}

// OpenConn returns one migrated database clone per test. Repeated calls from
// the same test reuse its connection; a test cleanup closes the connection
// before dropping the clone.
func (r *StandardTestRunner) OpenConn(t *testing.T) db.DbConn {
	t.Helper()
	connection, err := r.openStandardConn(t)
	if err != nil {
		t.Fatalf("postgresrunner: open standard test database: %v", err)
	}
	return connection
}

func (r *StandardTestRunner) openStandardConn(t *testing.T) (db.DbConn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if database, found := r.databases[t]; found {
		return database.conn, nil
	}
	if r.suite.RunID == "" {
		return nil, fmt.Errorf("standard test runner is not started")
	}

	r.nextNode++
	clone := &Runner{}
	if err := clone.AdoptSuiteConfig(r.suite, r.nextNode); err != nil {
		return nil, err
	}
	if err := clone.createTestDBFromTemplate(t.Context()); err != nil {
		return nil, err
	}
	connection, err := clone.openConn()
	if err != nil {
		return nil, errors.Join(err, clone.dropTestDB(context.Background()))
	}

	database := &standardTestDatabase{runner: clone, conn: connection}
	r.databases[t] = database
	t.Cleanup(func() {
		r.cleanupTestDatabase(t, database)
	})
	return connection, nil
}

func (r *StandardTestRunner) cleanupTestDatabase(t *testing.T, database *standardTestDatabase) {
	r.mu.Lock()
	if r.databases[t] == database {
		delete(r.databases, t)
	}
	r.mu.Unlock()

	closeErr := database.conn.Close()
	dropErr := database.runner.dropTestDB(context.Background())
	if err := errors.Join(closeErr, dropErr); err != nil {
		t.Errorf("postgresrunner: clean up standard test database: %v", err)
	}
}
