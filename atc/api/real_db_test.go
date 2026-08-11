package api_test

import (
	"database/sql"
	"net/http/httptest"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner

// Registers the suite-level postmaster. Specs that never call useRealDB pay
// only the one-time startup; they keep running against the package-level fakes.
var _ = postgresrunner.GinkgoRunner(&postgresRunner)

// realDB is what a converted Describe holds: the connection, the factories the
// handler was built from, and the default team. Specs write fixtures through
// the same objects the handlers read through, which is the whole point --
// asserting that a handler called FindTeam with the right argument passes just
// as happily against a handler that ignores the argument entirely.
type realDB struct {
	Conn        db.DbConn
	LockFactory lock.LockFactory
	Deps        apiDBDeps
	Main        db.Team
}

// useRealDB gives the calling Describe a migrated database and deps built over
// real factories. Call it from a BeforeEach; cleanup is registered here.
//
// The credential manager stays fake: creds.Secrets is a third-party seam, not a
// database one, and db.NewCheckFactory requires one either way.
func useRealDB() *realDB {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	// Register the drop first so Ginkgo's LIFO cleanup closes every connection
	// below before dropping the clone. Keeping it as its own cleanup node also
	// means a failed Close expectation cannot suppress the drop.
	DeferCleanup(postgresRunner.DropTestDB)

	conn := postgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})
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

	main, err := teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
	Expect(err).NotTo(HaveOccurred())

	// Buffered so a check enqueued by a handler cannot block the request: the
	// production consumer (the lidar checker) is not running here.
	checkBuildChan := make(chan db.Build, 64)

	checkFactory := db.NewCheckFactory(
		conn, lockFactory, fakeSecretManager, fakeVarSourcePool,
		checkBuildChan, util.NewSequenceGenerator(1),
	)

	// The real clock, matching production wiring in atccmd. The suite's
	// fakeclock does not satisfy db.Clock -- it has Now but not Until.
	dbClock := db.NewClock()

	deps := apiDBDeps{
		teamFactory:           teamFactory,
		pipelineFactory:       db.NewPipelineFactory(conn, lockFactory),
		jobFactory:            db.NewJobFactory(conn, lockFactory),
		resourceFactory:       db.NewResourceFactory(conn, lockFactory),
		workerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
		workerTeamFactory:     teamFactory,
		volumeRepository:      db.NewVolumeRepository(conn),
		buildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		checkFactory:          checkFactory,
		resourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		userFactory:           db.NewUserFactory(conn),

		wall:              db.NewWall(conn, &dbClock),
		signingKeyFactory: db.NewSigningKeyFactory(conn),
	}

	return &realDB{Conn: conn, LockFactory: lockFactory, Deps: deps, Main: main}
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
