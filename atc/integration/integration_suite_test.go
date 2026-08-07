package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atccmd"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/flag/v2"
	"github.com/jessevdk/go-flags"
	"github.com/tedsuo/ifrit"
	"golang.org/x/oauth2"
)

var (
	cmd            *atccmd.RunCommand
	postgresRunner postgresrunner.Runner
	atcProcess     ifrit.Process
	atcURL         string
)

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

var _ = BeforeEach(func() {
	cmd = &atccmd.RunCommand{}

	// call parseArgs to populate flag defaults but ignore errors so that we can
	// use the required:"true" field annotation
	//
	// use flags.None so that we don't print errors
	parser := flags.NewParser(cmd, flags.None)
	_, _ = parser.ParseArgs([]string{})

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)
	cmd.Postgres = runnerPostgresConfig(&postgresRunner)
	info := postgresRunner.ConnectionInfo()
	Expect(cmd.Postgres.Database).To(Equal(postgresRunner.DatabaseName()))
	Expect(cmd.Postgres.Database).To(HavePrefix("cc_db_"))
	Expect(cmd.Postgres.Host).To(Equal(info.Host))
	Expect(cmd.Postgres.Port).To(Equal(info.Port))
	cmd.Auth.MainTeamFlags.LocalUsers = []string{"test"}
	cmd.Auth.AuthFlags.LocalUsers = map[string]string{
		"test":    "test",
		"v-user":  "v-user",
		"po-user": "po-user",
		"m-user":  "m-user",
		"o-user":  "o-user",
	}
	cmd.Auth.AuthFlags.Clients = map[string]string{
		"client-id": "client-secret",
	}
	cmd.Server.ClientID = "client-id"
	cmd.Server.ClientSecret = "client-secret"
	cmd.Logger.LogLevel = "debug"
	cmd.Logger.SetWriterSink(GinkgoWriter)
	cmd.BindPort = 9090 + uint16(GinkgoParallelProcess())
	cmd.DebugBindPort = 0

	signingKey, err := rsa.GenerateKey(rand.Reader, 1024)
	Expect(err).ToNot(HaveOccurred())

	cmd.Auth.AuthFlags.SigningKey = &flag.PrivateKey{PrivateKey: signingKey}

	// workaround to avoid panic due to registering http handlers multiple times
	http.DefaultServeMux = new(http.ServeMux)
})

var _ = JustBeforeEach(func() {
	atcURL = fmt.Sprintf("http://localhost:%v", cmd.BindPort)

	runner, err := cmd.Runner([]string{})
	Expect(err).NotTo(HaveOccurred())

	atcProcess = ifrit.Invoke(runner)
	process := atcProcess
	DeferCleanup(func() {
		exitErr, shutdownErr := shutdownProcess(process, 10*time.Second, 10*time.Second)
		Expect(shutdownErr).NotTo(HaveOccurred())
		Expect(exitErr).NotTo(HaveOccurred())
	})

	Eventually(func() error {
		_, err := http.Get(atcURL + "/api/v1/info")
		return err
	}, 20*time.Second).ShouldNot(HaveOccurred())
})

func runnerPostgresConfig(r *postgresrunner.Runner) flag.PostgresConfig {
	return postgresConfigFromConnectionInfo(r.ConnectionInfo())
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

func postgresConfigFromConnectionInfo(info postgresrunner.ConnectionInfo) flag.PostgresConfig {
	return flag.PostgresConfig{
		Host:            info.Host,
		Port:            info.Port,
		Socket:          info.Socket,
		User:            info.User,
		Password:        info.Password,
		Database:        info.Database,
		ApplicationName: info.ApplicationName,
		SSLMode:         info.SSLMode,
		SSLNegotiation:  info.SSLNegotiation,
		CACert:          flag.File(info.SSLRootCert),
		ClientCert:      flag.File(info.SSLCert),
		ClientKey:       flag.File(info.SSLKey),
		ConnectTimeout:  info.ConnectTimeout,
	}
}

func TestPostgresConfigFromConnectionInfoPreservesSupportedProperties(t *testing.T) {
	info := postgresrunner.ConnectionInfo{
		Host:            "db.example.test",
		Port:            6432,
		Socket:          "/private/tmp/postgres",
		User:            "integration-user",
		Password:        "integration-password",
		Database:        "cc_db_integration",
		ApplicationName: "integration-app",
		SSLMode:         "verify-full",
		SSLNegotiation:  "direct",
		SSLRootCert:     "/certs/root.pem",
		SSLCert:         "/certs/client.pem",
		SSLKey:          "/certs/client-key.pem",
		ConnectTimeout:  41 * time.Second,
	}

	got := postgresConfigFromConnectionInfo(info)
	if got.Host != info.Host || got.Port != info.Port || got.Socket != info.Socket {
		t.Fatalf("network config = host %q port %d socket %q", got.Host, got.Port, got.Socket)
	}
	if got.User != info.User || got.Password != info.Password || got.Database != info.Database {
		t.Fatalf("credential/database config = user %q password %q database %q", got.User, got.Password, got.Database)
	}
	if got.ApplicationName != info.ApplicationName || got.SSLMode != info.SSLMode || got.SSLNegotiation != info.SSLNegotiation {
		t.Fatalf("runtime/TLS config = application %q sslmode %q sslnegotiation %q", got.ApplicationName, got.SSLMode, got.SSLNegotiation)
	}
	if got.CACert.Path() != info.SSLRootCert || got.ClientCert.Path() != info.SSLCert || got.ClientKey.Path() != info.SSLKey {
		t.Fatalf("certificate config = CA %q cert %q key %q", got.CACert.Path(), got.ClientCert.Path(), got.ClientKey.Path())
	}
	if got.ConnectTimeout != info.ConnectTimeout {
		t.Fatalf("connect timeout = %s, want %s", got.ConnectTimeout, info.ConnectTimeout)
	}
}

func TestShutdownProcessForcesAChildAfterTheGracePeriod(t *testing.T) {
	process := newForcedExitProcess()

	exitErr, shutdownErr := shutdownProcess(process, time.Millisecond, time.Second)

	if exitErr != nil || shutdownErr != nil {
		t.Fatalf("shutdown returned exit error %v and shutdown error %v", exitErr, shutdownErr)
	}
	want := []os.Signal{os.Interrupt, os.Kill}
	got := process.receivedSignals()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

type forcedExitProcess struct {
	ready     chan struct{}
	exited    chan struct{}
	exitOnce  sync.Once
	signalsMu sync.Mutex
	signals   []os.Signal
}

func newForcedExitProcess() *forcedExitProcess {
	ready := make(chan struct{})
	close(ready)
	return &forcedExitProcess{ready: ready, exited: make(chan struct{})}
}

func (process *forcedExitProcess) Ready() <-chan struct{} {
	return process.ready
}

func (process *forcedExitProcess) Wait() <-chan error {
	wait := make(chan error, 1)
	go func() {
		<-process.exited
		wait <- nil
	}()
	return wait
}

func (process *forcedExitProcess) Signal(signal os.Signal) {
	process.signalsMu.Lock()
	process.signals = append(process.signals, signal)
	process.signalsMu.Unlock()
	if signal == os.Kill {
		process.exitOnce.Do(func() { close(process.exited) })
	}
}

func (process *forcedExitProcess) receivedSignals() []os.Signal {
	process.signalsMu.Lock()
	defer process.signalsMu.Unlock()
	return append([]os.Signal(nil), process.signals...)
}

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

func login(atcURL, username, password string) concourse.Client {
	oauth2Config := oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: atcURL + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "federated:id"},
	}

	ctx := context.Background()
	oauthToken, err := oauth2Config.PasswordCredentialsToken(ctx, username, password)
	Expect(err).NotTo(HaveOccurred())

	tokenSource := oauth2.StaticTokenSource(oauthToken)
	httpClient := oauth2.NewClient(ctx, tokenSource)

	return concourse.NewClient(atcURL, httpClient, false)
}

func setupTeam(atcURL string, team atc.Team) {
	ccClient := login(atcURL, "test", "test")
	createdTeam, _, _, _, err := ccClient.Team(team.Name).CreateOrUpdate(team)

	Expect(err).ToNot(HaveOccurred())
	Expect(createdTeam.Name).To(Equal(team.Name))
	Expect(createdTeam.Auth).To(Equal(team.Auth))
}

func setupPipeline(atcURL, teamName string, config []byte) {
	ccClient := login(atcURL, "test", "test")
	_, _, _, err := ccClient.Team(teamName).CreateOrUpdatePipelineConfig(atc.PipelineRef{Name: "pipeline-name"}, "0", config, false)
	Expect(err).ToNot(HaveOccurred())
}
