package runlifecycle_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunLifecycle(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RunLifecycle Suite")
}

type lifecycleDB struct {
	Conn         db.DbConn
	Team         db.Team
	Runs         db.PipelineRunFactory
	Templates    db.WorkflowRunTemplateFactory
	WorkflowRuns db.AgentWorkflowRunsFactory
}

var lifecyclePostgres postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&lifecyclePostgres)

func useLifecycleDB() *lifecycleDB {
	GinkgoHelper()

	lifecyclePostgres.CreateTestDBFromTemplate()
	// Ginkgo runs cleanups in LIFO order. Register the drop first so every
	// ordinary and singleton connection is closed before the clone is dropped.
	DeferCleanup(lifecyclePostgres.DropTestDB)

	conn := lifecyclePostgres.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for index := range lockConns {
		singleton := lifecyclePostgres.OpenSingleton()
		lockConns[index] = singleton
		connToClose := singleton
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}
	noOpLockLog := func(lager.Logger, lock.LockID) {}
	lockFactory := lock.NewLockFactory(lockConns, noOpLockLog, noOpLockLog)

	checkFactory := db.NewCheckFactory(
		conn,
		lockFactory,
		new(credsfakes.FakeSecrets),
		new(credsfakes.FakeVarSourcePool),
		make(chan db.Build, 16),
		util.NewSequenceGenerator(1),
	)
	runs := db.NewPipelineRunFactory(
		lagertest.NewTestLogger("runlifecycle-postgres"), conn, lockFactory, checkFactory,
	)
	teamFactory := db.NewTeamFactory(conn, lockFactory)
	team, err := teamFactory.CreateTeam(atc.Team{
		Name: fmt.Sprintf("runlifecycle-%d", time.Now().UnixNano()),
	})
	Expect(err).NotTo(HaveOccurred())

	return &lifecycleDB{
		Conn:         conn,
		Team:         team,
		Runs:         runs,
		Templates:    db.NewWorkflowRunTemplateFactory(conn, lockFactory),
		WorkflowRuns: db.NewAgentWorkflowRunsFactory(conn),
	}
}
