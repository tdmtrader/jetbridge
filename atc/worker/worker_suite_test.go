package worker_test

import (
	"context"
	"database/sql"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/worker/workertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	postgresRunner postgresrunner.Runner
	dbConn         db.DbConn
	lockFactory    lock.LockFactory
)

var logger = lagertest.NewTestLogger("dummy")
var ctx = context.Background()

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

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

	ignore := func(logger lager.Logger, id lock.LockID) {}
	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := postgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	lockFactory = lock.NewLockFactory(lockConns, ignore, ignore)
})

func TestWorker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Suite")
}

func Setup(setup ...workertest.SetupFunc) *workertest.Scenario {
	return workertest.Setup(dbConn, lockFactory, setup...)
}

var Test = It
var FTest = FIt
var XTest = XIt
