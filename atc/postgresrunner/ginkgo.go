package postgresrunner

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// PickPort finds a port this postmaster can own, and reserves it.
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
// Seeding the scan with the PID separated concurrent package binaries but did
// not make a claim atomic: portFree probed a port, closed its listener, and
// returned -- and a second scanner probed the same port free in the window
// before this suite's postgres bound it. Both then started a postmaster on it;
// the loser inherited the winner's socket and its `drop testdb_template`.
// It also read a leftover socket file as "taken" forever, so one killed run
// permanently retired a port and slowly starved the pool.
//
// reservePort closes both holes: it claims a port with an exclusive lock file
// before probing, so concurrent scanners cannot claim the same port, and it
// judges a port by whether a process is actually listening -- a socket or lock
// left by a dead run is reclaimed, not obeyed. ReleasePort drops the lock when
// the runner stops.
func PickPort() int {
	GinkgoHelper()

	start := GinkgoParallelProcess() + os.Getpid()

	for i := 0; i < portSpan; i++ {
		port := portBase + (start+i)%portSpan
		if reservePort(port) {
			return port
		}
	}

	Fail(fmt.Sprintf("no free postgres port in %d-%d", portBase, portBase+portSpan))
	return 0
}

// ReleasePort drops the reservation PickPort took. Postgres removes its own
// socket on a clean stop; this removes the lock that fenced the port off from
// concurrent scanners for the runner's lifetime.
func ReleasePort(port int) {
	os.Remove(lockPath(port))
}

func lockPath(port int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("pgrunner-port-%d.lock", port))
}

func socketPath(port int) string {
	return fmt.Sprintf("/tmp/.s.PGSQL.%d", port)
}

// reservePort atomically claims a port, or returns false if it is genuinely in
// use. On success it holds an exclusive lock file (released by ReleasePort) and
// has cleared any stale socket so `postgres -k /tmp` can bind cleanly.
func reservePort(port int) bool {
	lock, ok := acquireLock(port)
	if !ok {
		return false
	}

	// A live postmaster fails this bind; a bindable port has no listener, so
	// any socket or lock left on it belongs to a dead run and is ours to clear.
	if !bindable(port) {
		os.Remove(lock)
		return false
	}

	os.Remove(socketPath(port))
	return true
}

// acquireLock creates the port's lock file exclusively, reclaiming one whose
// recorded owner has died. It returns the lock path on success.
func acquireLock(port int) (string, bool) {
	path := lockPath(port)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		if !reclaimStaleLock(path) {
			return "", false
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	if err != nil {
		return "", false
	}

	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	return path, true
}

// reclaimStaleLock removes a lock file only when its recorded owner is gone. A
// lock we cannot read an owner from is left alone rather than stomped.
func reclaimStaleLock(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || processAlive(pid) {
		return false
	}

	return os.Remove(path) == nil
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Signal 0 delivers nothing but still reports whether the process exists.
	return proc.Signal(syscall.Signal(0)) == nil
}

func bindable(port int) bool {
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
