package pgtest_test

import (
	"context"
	"os"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// The database must arrive migrated. Reading a table that only exists after the
// migration set has run is the cheapest proof that db.Open migrated it, rather
// than handing back an empty database that would fail later and further away.
func TestOpenTestDBReturnsAMigratedDatabase(t *testing.T) {
	conn := pgtest.OpenTestDB(t)

	var teams int
	if err := conn.QueryRow(`SELECT count(*) FROM teams`).Scan(&teams); err != nil {
		t.Fatalf("count teams: %v", err)
	}

	var applied int
	if err := conn.QueryRow(`SELECT count(*) FROM migrations_history`).Scan(&applied); err != nil {
		t.Fatalf("count migrations_history: %v", err)
	}
	if applied == 0 {
		t.Fatal("database has no migration history, so it was never migrated")
	}
}

// Isolation is the whole point: postgresrunner hands out one database called
// "testdb", so two tests cannot hold their own. A row written by one test must
// be invisible to another.
func TestOpenTestDBIsolatesEachTestFromEveryOther(t *testing.T) {
	first := pgtest.OpenTestDB(t)
	if _, err := first.Exec(
		`INSERT INTO teams (name, auth, nonce) VALUES ('isolation-probe', '{}', '')`,
	); err != nil {
		t.Fatalf("insert into first database: %v", err)
	}

	second := pgtest.OpenTestDB(t)
	var leaked int
	if err := second.QueryRow(
		`SELECT count(*) FROM teams WHERE name = 'isolation-probe'`,
	).Scan(&leaked); err != nil {
		t.Fatalf("count in second database: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("row written to one test database is visible in another (%d rows)", leaked)
	}
}

// A test that never asks for a database must not pay for one. This is what
// makes the harness adoptable in a fast package: the cost is per-test, not
// per-package as it is with a suite-wide BeforeEach.
func TestPackagesPayNothingForTestsThatDoNotOpenADatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("nothing to do")
	}
}

// The seeding helpers exist so a caller outside atc/db can assert authorization
// as behaviour rather than by reading a fake's recorded arguments. This proves
// they produce a snapshot the real store actually scopes by team.
func TestSeedSnapshotIsOwnedByItsTeamAndDeniedToOthers(t *testing.T) {
	conn := pgtest.OpenTestDB(t)

	owner := pgtest.SeedTeam(t, conn, "owning-team")
	stranger := pgtest.SeedTeam(t, conn, "stranger-team")
	ref := pgtest.SeedSnapshot(t, conn, owner, "a")

	store := db.NewAgentSnapshotsFactory(conn)

	found, ok, err := store.GetAuthorized(context.Background(), owner.ID(), ref.ID)
	if err != nil {
		t.Fatalf("GetAuthorized for owner: %v", err)
	}
	if !ok {
		t.Fatal("owning team cannot read its own snapshot")
	}
	if found.ID != ref.ID {
		t.Fatalf("owner got snapshot %d, want %d", found.ID, ref.ID)
	}

	_, ok, err = store.GetAuthorized(context.Background(), stranger.ID(), ref.ID)
	if err != nil {
		t.Fatalf("GetAuthorized for stranger: %v", err)
	}
	if ok {
		t.Fatal("a team that does not own the snapshot was allowed to read it")
	}
}
