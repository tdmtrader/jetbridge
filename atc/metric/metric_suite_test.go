package metric_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func TestMetric(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Metric Suite")
}

var testLogger = lager.NewLogger("test")

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

// useEmptyTestDB gives the calling spec a database with no schema in it. The
// metric package reads no Concourse table, so a clone of the migrated template
// would only be a slower way to reach the same postmaster.
func useEmptyTestDB() {
	GinkgoHelper()

	postgresRunner.CreateEmptyTestDB()
	DeferCleanup(postgresRunner.DropTestDB)
}

// openTestConn opens a connection that is closed when the spec ends.
func openTestConn(name string) db.DbConn {
	GinkgoHelper()

	conn := newTestConn(name)
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})

	return conn
}

// closedTestConn is a connection that has already been closed, which is how a
// spec makes the underlying connection fail.
func closedTestConn() db.DbConn {
	GinkgoHelper()

	conn := newTestConn("closed")
	Expect(conn.Close()).To(Succeed())

	return conn
}

// newTestConn uses db.NewConn rather than db.Open because Open runs the
// migrations and nothing here needs a schema.
func newTestConn(name string) db.DbConn {
	GinkgoHelper()

	dsn := postgresRunner.DataSourceName()

	sqlDB, err := sql.Open("pgx", dsn)
	Expect(err).NotTo(HaveOccurred())

	conn, err := db.NewConn(name, sqlDB, dsn, nil, nil)
	Expect(err).NotTo(HaveOccurred())

	return conn
}
