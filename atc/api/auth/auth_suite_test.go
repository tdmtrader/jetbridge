package auth_test

import (
	"database/sql"
	"testing"

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

// doomedTeamFactory returns a factory whose connection is already closed, so
// every lookup through it fails the way a database outage would. It opens its
// own connection: AfterEach asserts the suite's closes cleanly.
func doomedTeamFactory() db.TeamFactory {
	doomed := postgresRunner.OpenConn()
	factory := db.NewTeamFactory(doomed, lockFactory)
	Expect(doomed.Close()).To(Succeed())
	return factory
}
