package algorithm_test

import (
	"database/sql"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/tracing"
)

var (
	postgresRunner postgresrunner.Runner

	lockFactory lock.LockFactory
	teamFactory db.TeamFactory

	dbConn db.DbConn
)

func TestAlgorithm(t *testing.T) {
	if otlpAddress := os.Getenv("OTLP_ADDRESS"); otlpAddress != "" {
		c := tracing.Config{
			ServiceName: "algorithm_test",
			OTLP:        tracing.OTLP{Address: otlpAddress},
		}

		if err := c.Prepare(); err != nil {
			t.Fatal(err)
		}
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "Algorithm Suite")
}

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()

	dbConn = postgresRunner.OpenConn()
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
	}
	lockFactory = lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)
	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
})

var _ = AfterEach(func() {
	err := dbConn.Close()
	Expect(err).NotTo(HaveOccurred())

	postgresRunner.DropTestDB()
})
