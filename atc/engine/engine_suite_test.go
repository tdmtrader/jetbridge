package engine_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func init() {
	util.PanicSink = GinkgoWriter
}

func TestEngine(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Engine Suite")
}

type engineDBFixture struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	WorkerFactory         db.WorkerFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

var enginePostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&enginePostgresRunner)

func useEngineDB() *engineDBFixture {
	GinkgoHelper()

	enginePostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(enginePostgresRunner.DropTestDB)

	conn := enginePostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := enginePostgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
	logger := lagertest.NewTestLogger("engine-postgres-fixture")

	return &engineDBFixture{
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

func closedEngineCloneConn() db.DbConn {
	GinkgoHelper()
	conn := enginePostgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}

func createEngineJobBuild(
	fixture *engineDBFixture,
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

func consumeEngineBuildEvent(build db.Build, from uint) atc.Event {
	GinkgoHelper()
	source, err := build.Events(from)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(source.Close()).To(Succeed()) }()

	envelope, err := source.Next()
	Expect(err).NotTo(HaveOccurred())
	encoded, err := json.Marshal(envelope)
	Expect(err).NotTo(HaveOccurred())
	var message event.Message
	Expect(json.Unmarshal(encoded, &message)).To(Succeed())
	return message.Event
}

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}
