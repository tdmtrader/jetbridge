package algorithm_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/exporters/jaeger"

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

	exporter *jaeger.Exporter
)

func TestAlgorithm(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Algorithm Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	return postgresrunner.InitializeRunnerForGinkgo(&postgresRunner)
}, func(data []byte) {
	prepareTracingForGinkgo()
	postgresrunner.SynchronizeRunnerForGinkgo(&postgresRunner, data)
})

func prepareTracingForGinkgo() {
	jaegerURL := os.Getenv("JAEGER_URL")

	if jaegerURL != "" {
		c := tracing.Config{
			Jaeger: tracing.Jaeger{
				Endpoint: jaegerURL + "/api/traces",
				Service:  "algorithm_test",
			},
		}

		err := c.Prepare()
		Expect(err).ToNot(HaveOccurred())
	}
}

var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(func() {
		postgresRunner.DropTestDB()
	})

	dbConn = postgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(dbConn.Close()).To(Succeed())
	})
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := postgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	lockFactory = lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)
	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
})

var _ = SynchronizedAfterSuite(func() {
	if exporter != nil {
		exporter.Shutdown(context.Background())
	}
	postgresrunner.CleanupRunnerForGinkgo(&postgresRunner)
}, func() {
	postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner)
})
