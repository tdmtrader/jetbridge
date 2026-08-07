package main_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/onsi/gomega/gbytes"
	"github.com/tedsuo/ifrit"
	ginkgomon "github.com/tedsuo/ifrit/ginkgomon_v2"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Web Command", func() {

	var (
		concourseCommand *exec.Cmd
		concourseProcess ifrit.Process
		concourseRunner  *ginkgomon.Runner
	)

	BeforeEach(func() {
		postgresRunner.CreateEmptyTestDB()
		DeferCleanup(postgresRunner.DropTestDB)
		info := postgresRunner.ConnectionInfo()

		concourseCommand = newConcourseWebCommand(concoursePath, info, GinkgoParallelProcess())

		database := commandFlagValue(concourseCommand.Args, "--postgres-database")
		Expect(database).To(Equal(postgresRunner.DatabaseName()))
		Expect(database).To(HavePrefix("cc_db_"))
		Expect(commandFlagValue(concourseCommand.Args, "--postgres-host")).To(Equal(info.Host))
		Expect(commandFlagValue(concourseCommand.Args, "--postgres-port")).To(Equal(strconv.Itoa(int(info.Port))))
	})

	JustBeforeEach(func() {
		concourseRunner = ginkgomon.New(ginkgomon.Config{
			Command:       concourseCommand,
			Name:          "web",
			AnsiColorCode: "32m",
		})

		concourseProcess = ifrit.Background(concourseRunner)
		process := concourseProcess
		DeferCleanup(func() {
			_, err := shutdownProcess(process, 10*time.Second, 10*time.Second)
			Expect(err).NotTo(HaveOccurred())
		})

		// workaround to avoid panic due to registering http handlers multiple times
		http.DefaultServeMux = new(http.ServeMux)
	})

	It("starts atc", func() {
		Eventually(concourseRunner.Buffer(), "30s", "2s").Should(gbytes.Say("atc.listening"))
	})

	Context("when CONCOURSE_CONCURRENT_REQUEST_LIMIT is invalid", func() {
		BeforeEach(func() {
			concourseCommand.Env = append(concourseCommand.Env, "CONCOURSE_CONCURRENT_REQUEST_LIMIT=InvalidAction:0")
		})

		It("prints an error and exits", func() {
			// Explicit timeout: Gomega's default is 1s, which is not enough for
			// a freshly-exec'd binary to reach its flag validation on a loaded
			// machine. It passes alone and fails under a full parallel
			// make test-unit, which reads as a real regression every time.
			// Matches the 30s the sibling spec above already uses.
			Eventually(concourseRunner.Err(), "30s", "1s").Should(gbytes.Say("'InvalidAction' is not a valid action"))
		})
	})
})

var _ = Describe("PostgreSQL child wiring", func() {
	It("preserves every supported connection property without exposing the password in argv", func() {
		info := postgresrunner.ConnectionInfo{
			Host:            "db.example.test",
			Port:            6432,
			Socket:          "/ignored-while-host-is-set",
			User:            "child-user",
			Password:        "child-secret",
			Database:        "cc_db_child",
			ApplicationName: "child-app",
			SSLMode:         "verify-full",
			SSLNegotiation:  "direct",
			SSLRootCert:     "/certs/root.pem",
			SSLCert:         "/certs/client.pem",
			SSLKey:          "/certs/client-key.pem",
			ConnectTimeout:  37 * time.Second,
		}

		command := newConcourseWebCommand("/usr/local/bin/concourse", info, 9)

		Expect(commandFlagValue(command.Args, "--postgres-host")).To(Equal(info.Host))
		Expect(commandFlagValue(command.Args, "--postgres-port")).To(Equal("6432"))
		Expect(commandFlagValue(command.Args, "--postgres-socket")).To(Equal(info.Socket))
		Expect(commandFlagValue(command.Args, "--postgres-user")).To(Equal(info.User))
		Expect(commandFlagValue(command.Args, "--postgres-database")).To(Equal(info.Database))
		Expect(commandFlagValue(command.Args, "--postgres-application-name")).To(Equal(info.ApplicationName))
		Expect(commandFlagValue(command.Args, "--postgres-sslmode")).To(Equal(info.SSLMode))
		Expect(commandFlagValue(command.Args, "--postgres-sslnegotiation")).To(Equal(info.SSLNegotiation))
		Expect(commandFlagValue(command.Args, "--postgres-ca-cert")).To(Equal(info.SSLRootCert))
		Expect(commandFlagValue(command.Args, "--postgres-client-cert")).To(Equal(info.SSLCert))
		Expect(commandFlagValue(command.Args, "--postgres-client-key")).To(Equal(info.SSLKey))
		Expect(commandFlagValue(command.Args, "--postgres-connect-timeout")).To(Equal("37s"))
		Expect(commandFlagValue(command.Args, "--postgres-password")).To(BeEmpty())
		Expect(strings.Join(command.Args, "\x00")).NotTo(ContainSubstring(info.Password))
		Expect(commandEnvironmentValue(command.Env, "CONCOURSE_POSTGRES_PASSWORD")).To(Equal(info.Password))
	})

	It("uses socket-only connections and preserves an unlimited connect timeout", func() {
		info := postgresrunner.ConnectionInfo{
			Socket:          "/private/tmp/postgres",
			Port:            5432,
			User:            "socket-user",
			Database:        "cc_db_socket",
			SSLMode:         "disable",
			SSLNegotiation:  "postgres",
			ConnectTimeout:  0,
			ApplicationName: "",
		}

		command := newConcourseWebCommand("/usr/local/bin/concourse", info, 1)

		Expect(commandFlagValue(command.Args, "--postgres-host")).To(BeEmpty())
		Expect(commandFlagValue(command.Args, "--postgres-socket")).To(Equal(info.Socket))
		Expect(commandFlagValue(command.Args, "--postgres-connect-timeout")).To(Equal("0s"))
	})
})

var _ = Describe("child process cleanup", func() {
	It("forces a child that does not stop within the grace period", func() {
		process := newSignalExitProcess(os.Kill, nil)

		exitErr, shutdownErr := shutdownProcess(process, time.Millisecond, time.Second)

		Expect(exitErr).NotTo(HaveOccurred())
		Expect(shutdownErr).NotTo(HaveOccurred())
		Expect(process.receivedSignals()).To(Equal([]os.Signal{os.Interrupt, os.Kill}))
	})

	It("does not force a child that exits during the grace period", func() {
		process := newSignalExitProcess(os.Interrupt, nil)

		exitErr, shutdownErr := shutdownProcess(process, time.Second, time.Second)

		Expect(exitErr).NotTo(HaveOccurred())
		Expect(shutdownErr).NotTo(HaveOccurred())
		Expect(process.receivedSignals()).To(Equal([]os.Signal{os.Interrupt}))
	})
})

func commandFlagValue(args []string, name string) string {
	for i := range args {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newConcourseWebCommand(path string, info postgresrunner.ConnectionInfo, parallelProcess int) *exec.Cmd {
	args := []string{
		"web",
		"--postgres-host", info.Host,
		"--postgres-port", strconv.Itoa(int(info.Port)),
		"--postgres-user", info.User,
		"--postgres-database", info.Database,
		"--postgres-application-name", info.ApplicationName,
		"--postgres-sslmode", info.SSLMode,
		"--postgres-sslnegotiation", info.SSLNegotiation,
		"--postgres-connect-timeout", info.ConnectTimeout.String(),
		"--main-team-local-user", "test",
		"--add-local-user", "test:test",
		"--debug-bind-port", strconv.Itoa(8000 + parallelProcess),
		"--bind-port", strconv.Itoa(8080 + parallelProcess),
		"--client-id", "client-id",
		"--client-secret", "client-secret",
	}
	for _, optional := range []struct {
		name, value string
	}{
		{"--postgres-socket", info.Socket},
		{"--postgres-ca-cert", info.SSLRootCert},
		{"--postgres-client-cert", info.SSLCert},
		{"--postgres-client-key", info.SSLKey},
	} {
		if optional.value != "" {
			args = append(args, optional.name, optional.value)
		}
	}

	command := exec.Command(path, args...)
	command.Env = environmentWithValue(command.Environ(), "CONCOURSE_POSTGRES_PASSWORD", info.Password)
	return command
}

func environmentWithValue(environment []string, name, value string) []string {
	prefix := name + "="
	updated := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			updated = append(updated, entry)
		}
	}
	return append(updated, prefix+value)
}

func commandEnvironmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func shutdownProcess(process ifrit.Process, gracePeriod, forcePeriod time.Duration) (error, error) {
	if process == nil {
		return nil, nil
	}

	wait := process.Wait()
	process.Signal(os.Interrupt)
	select {
	case exitErr := <-wait:
		return exitErr, nil
	case <-time.After(gracePeriod):
	}

	process.Signal(os.Kill)
	select {
	case exitErr := <-wait:
		return exitErr, nil
	case <-time.After(forcePeriod):
		return nil, fmt.Errorf("child process did not exit within %s after forced termination", forcePeriod)
	}
}

type signalExitProcess struct {
	ready     chan struct{}
	exited    chan struct{}
	exitOn    os.Signal
	exitErr   error
	exitOnce  sync.Once
	signalsMu sync.Mutex
	signals   []os.Signal
}

func newSignalExitProcess(exitOn os.Signal, exitErr error) *signalExitProcess {
	ready := make(chan struct{})
	close(ready)
	return &signalExitProcess{
		ready:   ready,
		exited:  make(chan struct{}),
		exitOn:  exitOn,
		exitErr: exitErr,
	}
}

func (process *signalExitProcess) Ready() <-chan struct{} {
	return process.ready
}

func (process *signalExitProcess) Wait() <-chan error {
	wait := make(chan error, 1)
	go func() {
		<-process.exited
		wait <- process.exitErr
	}()
	return wait
}

func (process *signalExitProcess) Signal(signal os.Signal) {
	process.signalsMu.Lock()
	process.signals = append(process.signals, signal)
	process.signalsMu.Unlock()
	if signal == process.exitOn {
		process.exitOnce.Do(func() { close(process.exited) })
	}
}

func (process *signalExitProcess) receivedSignals() []os.Signal {
	process.signalsMu.Lock()
	defer process.signalsMu.Unlock()
	return append([]os.Signal(nil), process.signals...)
}
