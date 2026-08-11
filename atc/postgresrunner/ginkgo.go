package postgresrunner

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

type none struct{}

func GinkgoRunner(runner *Runner) none {
	var dbProcess ifrit.Process

	BeforeSuite(func() {
		InitializeRunnerForGinkgo(runner, &dbProcess)
	})

	AfterSuite(func() {
		dbProcess.Signal(os.Interrupt)
		Eventually(dbProcess.Wait(), 10*time.Second).Should(Receive())
	})

	return none{}
}

func InitializeRunnerForGinkgo(runner *Runner, dbProcess *ifrit.Process) {
	*runner = Runner{
		Port: PickPort(),
	}
	*dbProcess = ifrit.Invoke(*runner)
	runner.InitializeTestDBTemplate()
}

// portBase and portSpan bound the range PickPort scans.
const (
	portBase = 5433
	portSpan = 2000
)

// PickPort finds a port this postmaster can own.
//
// The port is not only a TCP port. Postgres names its unix socket
// /tmp/.s.PGSQL.<port>, and every consumer here connects through that socket
// rather than over TCP (see DataSourceName), so two postmasters sharing a port
// collide twice over: on the listener and on the socket file.
//
// This used to be `5433 + GinkgoParallelProcess()`, which is unique only
// *within* one `ginkgo -p` run. GinkgoParallelProcess() is 1 in every serially
// run suite, so plain `go test ./...` -- which runs package binaries
// concurrently -- handed 5434 to all 24 database-backed packages at once. That
// surfaced as `could not bind IPv4 address` and the long-blamed
// `database "testdb_template" already exists`: the second suite was reaching
// the first suite's postmaster through the shared socket.
//
// Seeding the scan with the PID separates concurrently running package
// binaries; probing then confirms the port is actually free, which also steps
// over a stale socket left by a killed run.
func PickPort() int {
	GinkgoHelper()

	start := GinkgoParallelProcess() + os.Getpid()

	for i := 0; i < portSpan; i++ {
		port := portBase + (start+i)%portSpan
		if portFree(port) {
			return port
		}
	}

	Fail(fmt.Sprintf("no free postgres port in %d-%d", portBase, portBase+portSpan))
	return 0
}

func portFree(port int) bool {
	if _, err := os.Stat(fmt.Sprintf("/tmp/.s.PGSQL.%d", port)); err == nil {
		return false
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	listener.Close()

	return true
}

func FinalizeRunnerForGinkgo(runner *Runner, dbProcess *ifrit.Process) {
	(*dbProcess).Signal(os.Interrupt)
	Eventually((*dbProcess).Wait(), 10*time.Second).Should(Receive())
}
