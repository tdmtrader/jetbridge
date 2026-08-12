package builds_test

import (
	"context"
	"database/sql"
	"testing"
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

func TestBuilds(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Builds Suite")
}

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var (
	dbConn       db.DbConn
	buildFactory db.BuildFactory
	job          db.Job
	resource     db.Resource
)

var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	dbConn = postgresRunner.OpenConn()
	DeferCleanup(func() { Expect(dbConn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := postgresRunner.OpenSingleton()
		connToClose := lockConn
		lockConns[i] = lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}
	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)

	buildFactory = db.NewBuildFactory(dbConn, lockFactory, 0, time.Hour)

	team, err := db.NewTeamFactory(dbConn, lockFactory).CreateTeam(atc.Team{Name: "tracker-team"})
	Expect(err).NotTo(HaveOccurred())

	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "tracker-pipeline"},
		atc.Config{
			Jobs:      atc.JobConfigs{{Name: "some-job"}},
			Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "some-base-type"}},
		},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	var found bool
	job, found, err = pipeline.Job("some-job")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	resource, found, err = pipeline.Resource("some-resource")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
})

// startedJobBuild persists a job build in the started state, which is what
// GetAllStartedBuilds hands the tracker.
func startedJobBuild(name string) db.Build {
	GinkgoHelper()
	build, err := job.CreateBuild("tracker-test")
	Expect(err).NotTo(HaveOccurred())
	started, err := build.Start(atc.Plan{ID: atc.PlanID(name)})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	return build
}

// startedCheckBuild persists a real check build. Its name comes from the
// database, not from the test, so the tracker's CheckBuildName branch is
// exercised against the name checks actually carry.
func startedCheckBuild() db.Build {
	GinkgoHelper()
	build, created, err := resource.CreateBuild(context.Background(), true, atc.Plan{ID: "check-plan"})
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	return build
}

func inMemoryCheckBuild() db.Build {
	GinkgoHelper()
	build, err := resource.CreateInMemoryBuild(
		context.Background(),
		atc.Plan{ID: "in-memory-plan"},
		util.NewSequenceGenerator(1),
	)
	Expect(err).NotTo(HaveOccurred())
	return build
}

// statusOf reads the persisted status rather than the in-memory build object,
// which the tracker's goroutine owns for the duration of the run.
func statusOf(buildID int) db.BuildStatus {
	GinkgoHelper()
	var status string
	Expect(dbConn.QueryRow("SELECT status FROM builds WHERE id = $1", buildID).Scan(&status)).To(Succeed())
	return db.BuildStatus(status)
}

func inMemoryStatusOf(resourceID int) string {
	GinkgoHelper()
	var status sql.NullString
	Expect(dbConn.QueryRow("SELECT in_memory_build_status FROM resources WHERE id = $1", resourceID).Scan(&status)).To(Succeed())
	return status.String
}
