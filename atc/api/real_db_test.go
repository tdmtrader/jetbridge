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

var (
	postgresRunner postgresrunner.Runner
	realWallClock  = db.NewClock()
)

// Registers BeforeSuite/AfterSuite only: one postmaster and one migrated
// template per parallel process. A spec that never calls useRealDB pays
// nothing beyond that -- no database is created and no connection opened, so
// the ~950 specs still running against fakes are unaffected.
var _ = postgresrunner.GinkgoRunner(&postgresRunner)

// realDB is what a converted Describe holds: the connection, the factories the
// handler was built from, and the team those handlers trust. Specs write
// fixtures through the same objects the handlers read through.
type realDB struct {
	Conn        db.DbConn
	LockFactory lock.LockFactory
	Deps        apiDBDeps
	Main        db.Team
}

// useRealDB gives the calling Describe a migrated database and a server built
// over real factories. Call it from a BeforeEach; cleanup is registered here.
//
// The credential manager stays fake: creds.Secrets is a third-party seam, not a
// database one, and db.NewCheckFactory requires one either way.
func useRealDB() *realDB {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()

	var (
		conn      db.DbConn
		lockConns [lock.FactoryCount]*sql.DB
	)
	// Register cleanup as soon as the clone exists. The closure observes the
	// pools assigned below, so a failure partway through setup still closes
	// everything opened so far before attempting to drop the database.
	DeferCleanup(func() {
		for _, lockConn := range lockConns {
			if lockConn != nil {
				Expect(lockConn.Close()).To(Succeed())
			}
		}
		if conn != nil {
			Expect(conn.Close()).To(Succeed())
		}
		postgresRunner.DropTestDB()
	})

	conn = postgresRunner.OpenConn()
	db.CleanupBaseResourceTypesCache()

	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory := lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory := db.NewTeamFactory(conn, lockFactory)

	// The agent handlers are configured with a trusted team id. Against fakes
	// that could be the literal 1; against real rows it has to be a team that
	// exists, or every agent route 404s on a team lookup.
	main, err := teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
	Expect(err).NotTo(HaveOccurred())

	// Buffered so a check enqueued by a handler cannot block the request: the
	// production consumer (the lidar checker) is not running here.
	checkBuildChan := make(chan db.Build, 64)

	checkFactory := db.NewCheckFactory(
		conn, lockFactory, fakeSecretManager, fakeVarSourcePool,
		checkBuildChan, util.NewSequenceGenerator(1),
	)

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
		pipelineRunFactory:    db.NewPipelineRunFactory(logger, conn, lockFactory, checkFactory),
		resourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		userFactory:           db.NewUserFactory(conn),

		// The real clock, matching production wiring (command.go:1132). The
		// suite's fakeclock does not satisfy db.Clock -- it has Now but not Until.
		wall:              db.NewWall(conn, &realWallClock),
		signingKeyFactory: db.NewSigningKeyFactory(conn),
		transcripts:       db.NewAgentRunTranscriptFactory(conn),
		workflowRuns:      db.NewAgentWorkflowRunsFactory(conn),
		experiments:       db.NewAgentExperimentsFactory(conn, nil),

		trustedTeamID:   main.ID(),
		trustedTeamName: main.Name(),
	}

	return &realDB{Conn: conn, LockFactory: lockFactory, Deps: deps, Main: main}
}

// Serve replaces the package-level server with one built over these deps, for
// the duration of the spec. Shadowing `server` in the Describe's own var block
// is the pattern team_scoped_handler_factory_test.go already uses.
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
