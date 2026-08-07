package exec_test

import (
	"database/sql"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/policyfakes"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
)

func init() {
	util.PanicSink = GinkgoWriter

	fakePolicyAgentFactory = new(policyfakes.FakeAgentFactory)
	fakePolicyAgentFactory.IsConfiguredReturns(true)
	fakePolicyAgentFactory.DescriptionReturns("fakeAgent")
	policy.RegisterAgent(fakePolicyAgentFactory)
}

func TestExec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Exec Suite")
}

var (
	testLogger = lagertest.NewTestLogger("test")

	fakePolicyAgentFactory *policyfakes.FakeAgentFactory
)

type execDBFixture struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	WorkerFactory         db.WorkerFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

var execPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&execPostgresRunner)

func useExecDB() *execDBFixture {
	GinkgoHelper()

	execPostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(execPostgresRunner.DropTestDB)

	conn := execPostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := execPostgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
	logger := lagertest.NewTestLogger("exec-postgres-fixture")

	return &execDBFixture{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		WorkerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		ResourceCacheFactory:  db.NewResourceCacheFactory(conn, lockFactory),
	}
}

func closedExecCloneConn() db.DbConn {
	GinkgoHelper()
	conn := execPostgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}

func createExecJobBuild(
	fixture *execDBFixture,
	teamName string,
	ref atc.PipelineRef,
	config atc.Config,
	createdBy string,
) (db.Team, db.Pipeline, db.Job, db.Build) {
	GinkgoHelper()
	_, configured := config.Jobs.Lookup("some-job")
	Expect(configured).To(BeTrue())

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, 0, false)
	Expect(err).NotTo(HaveOccurred())
	scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
	job := scenario.Job("some-job")
	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline, job, build
}

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}
