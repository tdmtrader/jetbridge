package api_test

import (
	"database/sql"
	"net/http/httptest"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	postgresRunner  postgresrunner.Runner
	postgresProcess ifrit.Process
	apiDB           *realDB
)

var _ = BeforeSuite(func() {
	postgresrunner.InitializeRunnerForGinkgo(&postgresRunner, &postgresProcess)
	postgresRunner.CreateTestDBFromTemplate()
})

var _ = AfterSuite(func() {
	postgresRunner.DropTestDB()
	postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner, &postgresProcess)
})

// realDB is what a converted Describe holds: the connection, the factories the
// handler was built from, and the default team. Specs write fixtures through
// the same objects the handlers read through, which is the whole point --
// asserting that a handler called FindTeam with the right argument passes just
// as happily against a handler that ignores the argument entirely.
type realDB struct {
	Conn               db.DbConn
	WorkerConn         db.DbConn
	HealthConn         db.DbConn
	AccessTokenFactory db.AccessTokenFactory
	LockFactory        lock.LockFactory
	Deps               apiDBDeps
	Main               db.Team

	disconnectOnce       sync.Once
	disconnectWorkerOnce sync.Once
	disconnectHealthOnce sync.Once
}

func (r *realDB) disconnect() {
	GinkgoHelper()
	r.disconnectOnce.Do(func() {
		Expect(r.Conn.Close()).To(Succeed())
	})
}

func (r *realDB) disconnectWorker() {
	GinkgoHelper()
	r.disconnectWorkerOnce.Do(func() {
		Expect(r.WorkerConn.Close()).To(Succeed())
	})
}

func (r *realDB) disconnectHealth() {
	GinkgoHelper()
	r.disconnectHealthOnce.Do(func() {
		Expect(r.HealthConn.Close()).To(Succeed())
	})
}

// openRealDB opens independent request, worker, and health connections over
// the suite database. The database itself is created once in BeforeSuite and
// truncated before each spec.
func openRealDB() *realDB {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	workerConn := postgresRunner.OpenConn()
	healthConn := postgresRunner.OpenConn()
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
		lockConn := lockConns[i]
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory := lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory := db.NewTeamFactory(conn, lockFactory)
	accessTokenFactory := db.NewAccessTokenFactory(conn)

	main, err := teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
	Expect(err).NotTo(HaveOccurred())

	// Buffered so a check enqueued by a handler cannot block the request: the
	// production consumer (the lidar checker) is not running here.
	checkBuildChan := make(chan db.Build, 64)

	checkFactory := db.NewCheckFactory(
		conn, lockFactory, checkBuildChan, util.NewSequenceGenerator(1),
	)

	// The real clock, matching production wiring in atccmd. The suite's
	// fakeclock does not satisfy db.Clock -- it has Now but not Until.
	dbClock := db.NewClock()

	workerFactory := db.NewWorkerFactory(
		workerConn,
		db.NewStaticWorkerCache(logger, workerConn, 0),
	)

	deps := apiDBDeps{
		teamFactory:           teamFactory,
		pipelineFactory:       db.NewPipelineFactory(conn, lockFactory),
		jobFactory:            db.NewJobFactory(conn, lockFactory),
		resourceFactory:       db.NewResourceFactory(conn, lockFactory),
		workerFactory:         workerFactory,
		workerTeamFactory:     teamFactory,
		volumeRepository:      db.NewVolumeRepository(conn),
		buildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		checkFactory:          checkFactory,
		resourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		userFactory:           db.NewUserFactory(conn),
		accessTokenFactory:    accessTokenFactory,
		dbPinger:              healthConn,

		wall:              db.NewWall(conn, &dbClock),
		signingKeyFactory: db.NewSigningKeyFactory(conn),
	}

	realdb := &realDB{
		Conn:               conn,
		WorkerConn:         workerConn,
		HealthConn:         healthConn,
		AccessTokenFactory: accessTokenFactory,
		LockFactory:        lockFactory,
		Deps:               deps,
		Main:               main,
	}
	DeferCleanup(realdb.disconnect)
	DeferCleanup(realdb.disconnectWorker)
	DeferCleanup(realdb.disconnectHealth)

	return realdb
}

// useRealDB returns the current spec's shared real fixture. Existing endpoint
// fixtures can keep calling it without creating another database clone.
func useRealDB() *realDB {
	GinkgoHelper()
	Expect(apiDB).NotTo(BeNil())
	return apiDB
}

// Serve builds a server over these deps for the duration of the spec. Shadowing
// `server` in the Describe's own var block is the pattern
// team_scoped_handler_factory_test.go already uses.
func (r *realDB) Serve() *httptest.Server {
	GinkgoHelper()

	srv := newAPIServer(r.Deps)
	DeferCleanup(srv.Close)
	return srv
}

// SavePipeline stores a pipeline owned by team. Note that pipeline visibility
// is NOT a field on atc.Config -- it is a column flipped by Expose()/Hide().
func (r *realDB) SavePipeline(team db.Team, name string, config atc.Config) db.Pipeline {
	GinkgoHelper()

	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false,
	)
	Expect(err).NotTo(HaveOccurred())
	return pipeline
}
