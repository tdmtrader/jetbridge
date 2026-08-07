package atccmd

import (
	"os"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"
)

var testPostgresRunner postgresrunner.StandardTestRunner

func TestMain(m *testing.M) {
	os.Exit(testPostgresRunner.Main(m))
}

func openTestDB(t *testing.T) db.DbConn {
	t.Helper()
	return testPostgresRunner.OpenConn(t)
}
