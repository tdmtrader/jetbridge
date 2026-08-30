package steps

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

// This file is the resource-plane replacement for the ginkgo suite fixture in
// jetbridge_suite_test.go. The mapping, proven by Track 0 of the
// brine_spec_migration proposal:
//
//	ginkgo                                  brine resource plane
//	------------------------------------    --------------------------------
//	BeforeSuite -> InitializeRunner...      "postgres"     (ScopeSuite)
//	AfterSuite  -> signal the postmaster    its Disposer
//	CreateTestDBFromTemplate() per spec     "jetbridge-db" (ScopeScenario)
//	DeferCleanup(DropTestDB)                its Disposer, LIFO
//	useJetbridgeDB() in a BeforeEach        DefineMapUsing(..., "jetbridge-db")
//
// The machinery owns disposal, so teardown survives cancellation — which
// DeferCleanup does not.

// postmaster is the suite-scoped resource value: one running postgres.
type postmaster struct {
	runner  *postgresrunner.Runner
	process ifrit.Process
}

// JetbridgeDB is the scenario-scoped resource value: a fresh database plus the
// factory surface the jetbridge worker needs. It mirrors the ginkgo suite's
// unexported jetbridgeDB struct, which is unreachable from here because Go
// cannot import identifiers from another package's _test.go files.
type JetbridgeDB struct {
	Conn                db.DbConn
	LockFactory         lock.LockFactory
	Builder             dbtest.Builder
	TeamFactory         db.TeamFactory
	WorkerFactory       db.WorkerFactory
	VolumeRepository    db.VolumeRepository
	ContainerRepository db.ContainerRepository
	BuildFactory        db.BuildFactory

	lockConns [lock.FactoryCount]*sql.DB
	runner    *postgresrunner.Runner
}

// PersistNamedWorker mirrors the ginkgo suite helper of the same name.
func (j JetbridgeDB) PersistNamedWorker(name string) (db.Worker, error) {
	_, err := j.WorkerFactory.SaveWorker(atc.Worker{
		Name: name, Platform: "linux", Version: "1.2.3",
		State: string(db.WorkerStateRunning),
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("save worker %q: %w", name, err)
	}
	worker, found, err := j.WorkerFactory.GetWorker(name)
	if err != nil {
		return nil, fmt.Errorf("get worker %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("worker %q not found after save", name)
	}
	return worker, nil
}

// ClosedConn opens a second connection to the same database and closes it, so
// every statement issued over it fails the way a lost connection does. It
// mirrors the ginkgo suite's closedJetbridgeCloneConn helper, which lives in a
// _test.go file and so cannot be imported.
func (j JetbridgeDB) ClosedConn() (db.DbConn, error) {
	conn := j.runner.OpenConn()
	if err := conn.Close(); err != nil {
		return nil, fmt.Errorf("close the cloned connection: %w", err)
	}
	return conn, nil
}

// RegisterGomegaFailHandler is the one-line cost Track 0 identified.
// atc/postgresrunner uses gomega Expect() in non-test code; outside a ginkgo
// suite the fail handler is ours, and without one gomega panics on the FIRST
// assertion rather than on a failing one.
func RegisterGomegaFailHandler() {
	gomega.RegisterFailHandler(func(message string, _ ...int) {
		panic("postgresrunner assertion failed outside a suite: " + message)
	})
}

// ResourceDefinitions is the adapter's resource plan.
func ResourceDefinitions() []brine.ResourceDefinition {
	return append([]brine.ResourceDefinition{
		TracingResourceDefinition(),
		TaskWorkspaceResourceDefinition(),
		RealClusterResourceDefinition(),
		RealDaemonResourceDefinition(),
	}, []brine.ResourceDefinition{
		{
			Name:  "postgres",
			Scope: brine.ScopeSuite,
			Factory: func(map[string]any) (any, error) {
				var runner postgresrunner.Runner
				var process ifrit.Process
				postgresrunner.InitializeRunnerForGinkgo(&runner, &process)
				return &postmaster{runner: &runner, process: process}, nil
			},
			Disposer: func(value any) error {
				pm, ok := value.(*postmaster)
				if !ok {
					return fmt.Errorf("postgres disposer got %T", value)
				}
				pm.process.Signal(os.Interrupt)
				select {
				case <-pm.process.Wait():
					return nil
				case <-time.After(10 * time.Second):
					return fmt.Errorf("postmaster did not exit within 10s")
				}
			},
		},
		{
			Name:      "jetbridge-db",
			Scope:     brine.ScopeScenario,
			DependsOn: []string{"postgres"},
			Factory: func(deps map[string]any) (any, error) {
				pm, ok := deps["postgres"].(*postmaster)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db dependency postgres is %T", deps["postgres"])
				}
				runner := pm.runner

				runner.CreateTestDBFromTemplate()
				conn := runner.OpenConn()
				db.CleanupBaseResourceTypesCache()

				var lockConns [lock.FactoryCount]*sql.DB
				for i := 0; i < lock.FactoryCount; i++ {
					lockConns[i] = runner.OpenSingleton()
				}
				lockFactory := lock.NewLockFactory(
					lockConns,
					func(lager.Logger, lock.LockID) {},
					func(lager.Logger, lock.LockID) {},
				)

				logger := lagertest.NewTestLogger("brine-jetbridge")
				return JetbridgeDB{
					Conn:                conn,
					LockFactory:         lockFactory,
					Builder:             dbtest.NewBuilder(conn, lockFactory),
					TeamFactory:         db.NewTeamFactory(conn, lockFactory),
					WorkerFactory:       db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0)),
					VolumeRepository:    db.NewVolumeRepository(conn),
					ContainerRepository: db.NewContainerRepository(conn),
					BuildFactory:        db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
					lockConns:           lockConns,
					runner:              runner,
				}, nil
			},
			Disposer: func(value any) error {
				j, ok := value.(JetbridgeDB)
				if !ok {
					return fmt.Errorf("jetbridge-db disposer got %T", value)
				}
				for i := 0; i < lock.FactoryCount; i++ {
					if j.lockConns[i] != nil {
						_ = j.lockConns[i].Close()
					}
				}
				if err := j.Conn.Close(); err != nil {
					return fmt.Errorf("close conn: %w", err)
				}
				j.runner.DropTestDB()
				return nil
			},
		},
	}...)
}
