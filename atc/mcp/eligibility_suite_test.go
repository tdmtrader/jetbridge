package mcp_test

import (
	"code.cloudfoundry.org/lager/v3"
	"database/sql"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

func TestEligibility(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MCP real account eligibility")
}

var postgresRunner postgresrunner.Runner
var dbConn db.DbConn
var teamFactory db.TeamFactory
var lockFactory lock.LockFactory
var _ = postgresrunner.GinkgoRunner(&postgresRunner)
var _ = BeforeEach(func() {
	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)
	dbConn = postgresRunner.OpenConn()
	conn := dbConn
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	var locks [lock.FactoryCount]*sql.DB
	for i := range locks {
		locks[i] = postgresRunner.OpenSingleton()
		c := locks[i]
		DeferCleanup(func() { Expect(c.Close()).To(Succeed()) })
	}
	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory = lock.NewLockFactory(locks, ignore, ignore)
	teamFactory = db.NewTeamFactory(dbConn, lockFactory)
})

func createTeam(name string) db.Team {
	team, err := teamFactory.CreateTeam(atc.Team{Name: name})
	Expect(err).NotTo(HaveOccurred())
	return team
}
func grantRole(team db.Team, role string) {
	Expect(team.UpdateProviderAuth(atc.TeamAuth{role: {"users": {"test:some-user"}}})).To(Succeed())
}
func makeAdmin(team db.Team) {
	grantRole(team, accessor.OwnerRole)
	_, err := dbConn.Exec("UPDATE teams SET admin=true WHERE id=$1", team.ID())
	Expect(err).NotTo(HaveOccurred())
}
func doomedTeamFactory() db.TeamFactory {
	conn := postgresRunner.OpenConn()
	factory := db.NewTeamFactory(conn, lockFactory)
	Expect(conn.Close()).To(Succeed())
	return factory
}
