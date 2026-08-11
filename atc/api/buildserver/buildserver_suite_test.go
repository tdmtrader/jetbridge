package buildserver_test

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

var (
	postgresRunner postgresrunner.Runner

	dbConn       db.DbConn
	lockConns    [lock.FactoryCount]*sql.DB
	lockFactory  lock.LockFactory
	teamFactory  db.TeamFactory
	buildFactory db.BuildFactory
)

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
})

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

func createTeam(name string) db.Team {
	team, err := teamFactory.CreateTeam(atc.Team{Name: name})
	Expect(err).NotTo(HaveOccurred())
	return team
}

// createBuild returns a one-off build that has not been started, so it carries
// no plan. HasPlan() is false for it.
func createBuild(team db.Team) db.Build {
	build, err := team.CreateOneOffBuild()
	Expect(err).NotTo(HaveOccurred())
	return build
}

// startedBuildForAPI returns the BuildForAPI view of a build started with plan,
// which is what puts a public plan and a schema on it.
func startedBuildForAPI(team db.Team, plan atc.Plan) db.BuildForAPI {
	build := createBuild(team)

	started, err := build.Start(plan)
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())

	forAPI, found, err := buildFactory.BuildForAPI(build.ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return forAPI
}

func buildForAPI(build db.Build) db.BuildForAPI {
	forAPI, found, err := buildFactory.BuildForAPI(build.ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return forAPI
}

func TestHandler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Build Server Suite")
}
