package jetbridge_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner
var _ = postgresrunner.GinkgoRunner(&postgresRunner)

type jetbridgeDB struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	WorkerFactory         db.WorkerFactory
	VolumeRepository      db.VolumeRepository
	ContainerRepository   db.ContainerRepository
	BuildFactory          db.BuildFactory
	ResourceConfigFactory db.ResourceConfigFactory
	ResourceCacheFactory  db.ResourceCacheFactory
}

func useJetbridgeDB() jetbridgeDB {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	conn := postgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConn := postgresRunner.OpenSingleton()
		lockConns[i] = lockConn
		connToClose := lockConn
		DeferCleanup(func() { Expect(connToClose.Close()).To(Succeed()) })
	}

	lockFactory := lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)

	return jetbridgeDB{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		WorkerFactory:         db.NewWorkerFactory(conn, db.NewStaticWorkerCache(lagertest.NewTestLogger("jetbridge-postgres-fixture"), conn, 0)),
		VolumeRepository:      db.NewVolumeRepository(conn),
		ContainerRepository:   db.NewContainerRepository(conn),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		ResourceCacheFactory:  db.NewResourceCacheFactory(conn, lockFactory),
	}
}

func closedJetbridgeCloneConn() db.DbConn {
	GinkgoHelper()
	conn := postgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}

func persistNamedWorker(database jetbridgeDB, name string) (db.Worker, error) {
	_, err := database.WorkerFactory.SaveWorker(atc.Worker{
		Name: name, Platform: "linux", Version: "1.2.3",
		State: string(db.WorkerStateRunning),
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("save worker %q: %w", name, err)
	}
	foundWorker, found, err := database.WorkerFactory.GetWorker(name)
	if err != nil {
		return nil, fmt.Errorf("get worker %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("worker %q not found after save", name)
	}
	return foundWorker, nil
}

func TestJetbridge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Jetbridge Suite")
}

// noopDelegate satisfies runtime.BuildStepDelegate for tests that don't
// need volume streaming or build timing.
type noopDelegate struct{}

func (d *noopDelegate) BuildStartTime() time.Time { return time.Time{} }

// expectSupervisedExec asserts that a task exec command was wrapped in the
// in-pod task supervisor, embedding the original command as quoted words.
// quotedCommand is the shell-quoted form, e.g. `'/bin/sh' '-c' 'npm test'`.
func expectSupervisedExec(command []string, quotedCommand string) {
	ExpectWithOffset(1, command).To(HaveLen(3))
	ExpectWithOffset(1, command[0]).To(Equal("sh"))
	ExpectWithOffset(1, command[1]).To(Equal("-c"))
	ExpectWithOffset(1, command[2]).To(ContainSubstring(quotedCommand))
	ExpectWithOffset(1, command[2]).To(ContainSubstring(`trap '' HUP`))
}
