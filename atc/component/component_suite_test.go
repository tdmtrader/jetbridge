package component_test

import (
	"database/sql"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestComponent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Component Suite")
}

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

func newLockConns() [lock.FactoryCount]*sql.DB {
	var conns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		conn := postgresRunner.OpenSingleton()
		conns[i] = conn
		DeferCleanup(func() {
			Expect(conn.Close()).To(Succeed())
		})
	}
	return conns
}

func newLockFactory(conns [lock.FactoryCount]*sql.DB) lock.LockFactory {
	noopLog := func(lager.Logger, lock.LockID) {}
	return lock.NewLockFactory(conns, noopLog, noopLog)
}
