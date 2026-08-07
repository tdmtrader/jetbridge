package mcpserver_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMCPServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MCP Server Suite")
}

var mcpToolsPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&mcpToolsPostgresRunner)

type mcpToolDeps struct {
	TeamFactory        db.TeamFactory
	BuildFactory       db.BuildFactory
	WorkflowsFactory   db.AgentWorkflowsFactory
	CostLedgerFactory  db.AgentCostLedgerFactory
	PipelineRunFactory db.PipelineRunFactory
}

type mcpToolsDB struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	ResourceConfigFactory db.ResourceConfigFactory
	Main                  db.Team
	Deps                  mcpToolDeps
}

func useMCPToolsDB() *mcpToolsDB {
	GinkgoHelper()

	mcpToolsPostgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(mcpToolsPostgresRunner.DropTestDB)

	conn := mcpToolsPostgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := range lockConns {
		lockConns[i] = mcpToolsPostgresRunner.OpenSingleton()
	}
	DeferCleanup(func() error {
		var closeErrs []error
		for _, lockConn := range lockConns {
			if err := lockConn.Close(); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		return errors.Join(closeErrs...)
	})
	ignoreLockEvent := func(lager.Logger, lock.LockID) {}
	lockFactory := lock.NewLockFactory(lockConns, ignoreLockEvent, ignoreLockEvent)

	resourceConfigFactory := db.NewResourceConfigFactory(conn, lockFactory)
	checkFactory := db.NewCheckFactory(
		conn,
		lockFactory,
		new(credsfakes.FakeSecrets),
		new(credsfakes.FakeVarSourcePool),
		make(chan db.Build, 64),
		util.NewSequenceGenerator(1),
	)

	teamFactory := db.NewTeamFactory(conn, lockFactory)
	main, err := teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
	Expect(err).NotTo(HaveOccurred())

	workflowValidator := workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	deps := mcpToolDeps{
		TeamFactory:       teamFactory,
		BuildFactory:      db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		WorkflowsFactory:  db.NewAgentWorkflowsFactory(conn, workflowValidator),
		CostLedgerFactory: db.NewAgentCostLedgerFactory(conn),
		PipelineRunFactory: db.NewPipelineRunFactory(
			lagertest.NewTestLogger("mcp-tools-postgres"),
			conn,
			lockFactory,
			checkFactory,
		),
	}

	return &mcpToolsDB{
		Conn:                  conn,
		LockFactory:           lockFactory,
		ResourceConfigFactory: resourceConfigFactory,
		Main:                  main,
		Deps:                  deps,
	}
}

func newMCPToolsServer(deps mcpToolDeps) *mcpserver.Server {
	server := mcpserver.NewServer()
	mcpserver.RegisterTools(
		server,
		deps.TeamFactory,
		deps.BuildFactory,
		deps.WorkflowsFactory,
		deps.CostLedgerFactory,
		deps.PipelineRunFactory,
		"https://concourse.example.com",
		"1.0.0",
	)
	return server
}
