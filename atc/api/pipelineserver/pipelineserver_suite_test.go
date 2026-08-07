package pipelineserver_test

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

var (
	postgresRunner postgresrunner.Runner

	dbConn      db.DbConn
	lockConns   [lock.FactoryCount]*sql.DB
	lockFactory lock.LockFactory
	teamFactory db.TeamFactory
)

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	dbConn = nil
	lockConns = [lock.FactoryCount]*sql.DB{}
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(func() {
		for _, lockConn := range lockConns {
			if lockConn != nil {
				Expect(lockConn.Close()).To(Succeed())
			}
		}
		if dbConn != nil {
			Expect(dbConn.Close()).To(Succeed())
		}
		postgresRunner.DropTestDB()
	})

	dbConn = postgresRunner.OpenConn()
	db.CleanupBaseResourceTypesCache()

	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(lockConns, ignore, ignore)

	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
})

// createTeam registers a team the handlers can actually look up by name.
func createTeam(name string) db.Team {
	team, err := teamFactory.CreateTeam(atc.Team{Name: name})
	Expect(err).NotTo(HaveOccurred())
	return team
}

// createPipeline saves a minimal but real pipeline owned by team. The handlers
// under test only read its identity or flip its paused/archived state, so one
// job is enough to make it a valid config.
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

func TestPipelineserver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pipelineserver Suite")
}
