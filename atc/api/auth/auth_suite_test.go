package auth_test

import (
	"database/sql"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAuth(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Suite")
}

var (
	logger lager.Logger

	postgresRunner postgresrunner.Runner
	dbConn         db.DbConn
	lockFactory    lock.LockFactory
	teamFactory    db.TeamFactory
	buildFactory   db.BuildFactory
	workerFactory  db.WorkerFactory
)

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	logger = lager.NewLogger("test")

	postgresRunner.CreateTestDBFromTemplate()
	dbConn = postgresRunner.OpenConn()
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
	buildFactory = db.NewBuildFactory(dbConn, lockFactory, 0, time.Hour)
	workerFactory = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))
})

var _ = AfterEach(func() {
	Expect(dbConn.Close()).To(Succeed())
	postgresRunner.DropTestDB()
})

func createTeam(name string) db.Team {
	team, err := teamFactory.CreateTeam(atc.Team{Name: name})
	Expect(err).NotTo(HaveOccurred())
	return team
}

// createPipeline saves a pipeline owned by team. Visibility is deliberately not
// a parameter: it is not a field on atc.Config but a column, flipped by
// Expose()/Hide() afterwards.
func createPipeline(team db.Team, name string) db.Pipeline {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: name},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	return pipeline
}

// createJobBuildWithConfig is createJobBuild with control over the job's
// config, so a spec can make the job public -- atc.JobConfig.Public is real and
// lands in jobs.public, unlike pipeline visibility which is Expose()/Hide().
func createJobBuildWithConfig(team db.Team, pipelineName string, job atc.JobConfig) (db.Pipeline, db.Build) {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		atc.Config{Jobs: atc.JobConfigs{job}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	dbJob, found, err := pipeline.Job(job.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	build, err := dbJob.CreateBuild("some-user")
	Expect(err).NotTo(HaveOccurred())
	return pipeline, build
}

// createJobBuild gives a build that belongs to a job in a pipeline, which is
// what the build-access handlers scope against.
func createJobBuild(team db.Team, pipelineName, jobName string) db.Build {
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		atc.Config{Jobs: atc.JobConfigs{{Name: jobName}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	job, found, err := pipeline.Job(jobName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	build, err := job.CreateBuild("some-user")
	Expect(err).NotTo(HaveOccurred())
	return build
}

// doomedWorkerFactory is the worker-side counterpart of doomedTeamFactory.
func doomedWorkerFactory() db.WorkerFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewWorkerFactory(doomed, db.NewStaticWorkerCache(logger, doomed, 0))
	Expect(doomed.Close()).To(Succeed())
	return factory
}

// doomedBuildFactory is the build-side counterpart of doomedTeamFactory.
func doomedBuildFactory() db.BuildFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewBuildFactory(doomed, lockFactory, 0, time.Hour)
	Expect(doomed.Close()).To(Succeed())
	return factory
}

// doomedTeamFactory returns a factory whose connection is already closed, so
// every lookup through it fails the way a database outage would. It opens its
// own connection: AfterEach asserts the suite's closes cleanly.
func doomedTeamFactory() db.TeamFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewTeamFactory(doomed, lockFactory)
	Expect(doomed.Close()).To(Succeed())
	return factory
}
