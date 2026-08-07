package postgresrunner

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStandardTestRunnerClonesOneSharedDatabasePerTest(t *testing.T) {
	var runner StandardTestRunner
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Stop(context.Background()); err != nil {
			t.Errorf("stop standard test runner: %v", err)
		}
	})

	first := runner.OpenConn(t)
	if second := runner.OpenConn(t); second != first {
		t.Fatal("one test received more than one database connection")
	}

	var parentDatabase string
	if err := first.QueryRow(`SELECT current_database()`).Scan(&parentDatabase); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(parentDatabase, "cc_db_") {
		t.Fatalf("database = %q, want shared-runner clone", parentDatabase)
	}
	if _, err := first.Exec(`CREATE TABLE standard_runner_isolation (value text NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	var childDatabase string
	t.Run("another spec", func(t *testing.T) {
		connection := runner.OpenConn(t)
		if err := connection.QueryRow(`SELECT current_database()`).Scan(&childDatabase); err != nil {
			t.Fatal(err)
		}
		var inheritedTable *string
		if err := connection.QueryRow(`SELECT to_regclass('public.standard_runner_isolation')::text`).Scan(&inheritedTable); err != nil {
			t.Fatal(err)
		}
		if inheritedTable != nil {
			t.Fatalf("clone inherited parent test table %q", *inheritedTable)
		}
	})

	if childDatabase == parentDatabase {
		t.Fatalf("parent and child tests shared database %q", parentDatabase)
	}
	if databaseExists(t, runner.suite.AdminDSN, childDatabase) {
		t.Fatalf("child clone %q survived child test cleanup", childDatabase)
	}
}

func TestStandardTestRunnerIsolatesParallelTestsAndDropsTheirClones(t *testing.T) {
	var runner StandardTestRunner
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Stop(context.Background()); err != nil {
			t.Errorf("stop standard test runner: %v", err)
		}
	})

	databaseNames := make(chan string, 2)
	ready := make(chan struct{})
	var readyCount atomic.Int32
	t.Run("parallel specs", func(t *testing.T) {
		for spec := 1; spec <= 2; spec++ {
			spec := spec
			t.Run(fmt.Sprintf("spec %d", spec), func(t *testing.T) {
				t.Parallel()
				connection := runner.OpenConn(t)

				var databaseName string
				if err := connection.QueryRow(`SELECT current_database()`).Scan(&databaseName); err != nil {
					t.Fatal(err)
				}
				if readyCount.Add(1) == 2 {
					close(ready)
				}
				select {
				case <-ready:
				case <-time.After(10 * time.Second):
					t.Fatal("parallel database did not become ready")
				}

				if _, err := connection.Exec(`CREATE TABLE parallel_isolation (value integer NOT NULL)`); err != nil {
					t.Fatal(err)
				}
				if _, err := connection.Exec(`INSERT INTO parallel_isolation (value) VALUES ($1)`, spec); err != nil {
					t.Fatal(err)
				}

				var count, value int
				if err := connection.QueryRow(`SELECT count(*), min(value) FROM parallel_isolation`).Scan(&count, &value); err != nil {
					t.Fatal(err)
				}
				if count != 1 || value != spec {
					t.Fatalf("isolated rows = count:%d value:%d, want count:1 value:%d", count, value, spec)
				}
				databaseNames <- databaseName
			})
		}
	})
	close(databaseNames)

	seen := map[string]struct{}{}
	for databaseName := range databaseNames {
		if _, found := seen[databaseName]; found {
			t.Fatalf("parallel tests shared database %q", databaseName)
		}
		seen[databaseName] = struct{}{}
		if databaseExists(t, runner.suite.AdminDSN, databaseName) {
			t.Fatalf("parallel clone %q survived test cleanup", databaseName)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("observed %d parallel databases, want 2", len(seen))
	}
}
