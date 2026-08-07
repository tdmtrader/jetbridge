package wrappa_test

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	postgresRunner postgresrunner.Runner
	dbConn         db.DbConn
	lockConns      [lock.FactoryCount]*sql.DB
	lockFactory    lock.LockFactory
	teamFactory    db.TeamFactory
	buildFactory   db.BuildFactory
	workerFactory  db.WorkerFactory
)

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	dbConn = nil
	lockConns = [lock.FactoryCount]*sql.DB{}
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	dbConn = postgresRunner.OpenConn()
	conn := dbConn
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})
	db.CleanupBaseResourceTypesCache()

	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
		lockConn := lockConns[i]
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
	buildFactory = db.NewBuildFactory(dbConn, lockFactory, 0, time.Hour)
	workerFactory = db.NewWorkerFactory(
		dbConn,
		db.NewStaticWorkerCache(lager.NewLogger("wrappa-test"), dbConn, 0),
	)
})

func TestWrappa(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Wrappa Suite")
}

type stupidHandler struct{}

func (stupidHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
}

type descriptiveRoute struct {
	route   string
	handler http.Handler
}
