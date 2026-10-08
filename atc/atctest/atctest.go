// Package atctest runs a real JetBridge platform inside a test process, so a
// client of the Run API is tested against the platform rather than against an
// imitation of its routes.
//
// The web node is the production API handler, wrapper chain, Run admission
// port, result reader and Dex issuer over a migrated PostgreSQL. The output
// node is the real artifact-daemon binary, its output plane mounted, over a GCS
// emulator, brought into service by the in-service row the web sets. What a runtime does to a
// Run -- start a result producer, publish what it wrote, finish its builds --
// is driven through the database, daemon and coordinator calls the runtime
// makes (see Producer).
//
// Only the Kubernetes half of the one output node is stood in for, because a
// test process has no cluster: which node serves the plane, that the producing
// Pod has terminated, and the kubelet exec that carries credentials into it.
// See node.go.
//
// One platform serves a whole test binary, started by Run from TestMain.
// Tests isolate themselves by team (NewTeam), not by platform.
package atctest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/pipelinerunserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/dexserver"
	"github.com/concourse/concourse/skymarshal/skycmd"
	skystorage "github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/concourse/flag/v2"
	"github.com/google/uuid"
	"github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
	"golang.org/x/oauth2"
)

// Epoch is both the Run activation epoch and the Hangar control-key generation
// the platform speaks for.
const Epoch = 7

// Pin is the one credential worker image the platform delivers credentials
// into. A template's credential-receiving producer declares
// rootfs_uri docker:///<Pin>.
const Pin = "registry.example/jb-review-worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// User is the local user every test acts as.
const User = "owner"

const password = "atctest-password"

var (
	running *Platform
	failure error
)

// Run starts the platform, runs the package's tests and stops it. Call it from
// TestMain: os.Exit(atctest.Run(m)).
func Run(m *testing.M) int {
	// postgresrunner asserts with gomega in non-test code; outside a ginkgo
	// suite the first assertion panics unexplained without a handler.
	gomega.RegisterFailHandler(func(message string, _ ...int) {
		panic("atctest: postgresrunner: " + message)
	})
	var postmaster postgresrunner.Runner
	var process ifrit.Process
	postgresrunner.InitializeRunnerForGinkgo(&postmaster, &process)
	defer func() {
		process.Signal(os.Interrupt)
		select {
		case <-process.Wait():
		case <-time.After(10 * time.Second):
		}
	}()
	postmaster.CreateTestDBFromTemplate()
	var stop func()
	running, stop, failure = start(&postmaster)
	code := m.Run()
	if stop != nil {
		stop()
	}
	if daemonBinary != "" {
		_ = os.RemoveAll(filepath.Dir(daemonBinary))
	}
	postmaster.DropTestDB()
	return code
}

// Get returns the running platform, failing t if Run did not start one.
func Get(t testing.TB) *Platform {
	t.Helper()
	if failure != nil {
		t.Fatalf("the platform did not start: %v", failure)
	}
	if running == nil {
		t.Fatal("atctest.Run was not called from TestMain")
	}
	return running
}

// Platform is one web node and its one output node.
type Platform struct {
	// URL is the web node's external URL: loopback HTTP, which the Run client
	// accepts for credential handoff.
	URL string

	conn    db.DbConn
	locks   lock.LockFactory
	teams   db.TeamFactory
	runs    db.PipelineRunFactory
	builds  db.BuildFactory
	node    *node
	teamSeq atomic.Int64
	tokenMu sync.Mutex
	token   string
	// producers holds each started producer by Run ID and result, so the
	// runtime never starts one twice.
	producers sync.Map
	startMu   sync.Mutex

	refusedMu    sync.Mutex
	refusedTeams map[string]bool
}

func start(postmaster *postgresrunner.Runner) (p *Platform, stop func(), err error) {
	var cleanups []func()
	stop = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			stop()
			stop = nil
		}
	}()
	logger := lager.NewLogger("atctest")

	var lockConns [lock.FactoryCount]*sql.DB
	for i := range lockConns {
		lockConns[i] = postmaster.OpenSingleton()
		conn := lockConns[i]
		cleanups = append(cleanups, func() { _ = conn.Close() })
	}
	ignore := func(lager.Logger, lock.LockID) {}
	locks := lock.NewLockFactory(lockConns, ignore, ignore)
	conn, err := db.Open(logger, "pgx", postmaster.DataSourceName(), nil, nil, "atctest", locks)
	if err != nil {
		return nil, stop, err
	}
	cleanups = append(cleanups, func() { _ = conn.Close() })
	// The API, the runtime driver and the result reader are concurrent callers,
	// as they are on a web node.
	conn.SetMaxOpenConns(16)
	db.CleanupBaseResourceTypesCache()

	p = &Platform{conn: conn, locks: locks, teams: db.NewTeamFactory(conn, locks),
		runs: db.NewPipelineRunFactory(conn, locks), builds: db.NewBuildFactory(conn, locks, 0, time.Hour), refusedTeams: map[string]bool{}}

	previous := atc.PipelineRunActivationEpoch
	atc.PipelineRunActivationEpoch = Epoch
	cleanups = append(cleanups, func() { atc.PipelineRunActivationEpoch = previous })
	if _, err = db.ReconcilePipelineRunActivation(context.Background(), conn, Epoch); err != nil {
		return nil, stop, err
	}

	p.node, err = startNode(conn)
	if err != nil {
		return nil, stop, err
	}
	cleanups = append(cleanups, p.node.stop)
	p.node.refused = p.deliveryRefused

	server := httptest.NewUnstartedServer(nil)
	p.URL = "http://" + server.Listener.Addr().String()
	handler, closeWeb, err := p.web(logger, postmaster)
	if err != nil {
		server.Listener.Close()
		return nil, stop, err
	}
	cleanups = append(cleanups, closeWeb)
	server.Config.Handler = handler
	server.Start()
	cleanups = append(cleanups, server.Close)
	return p, stop, nil
}

// web composes the node as atccmd does: Dex under /sky/issuer, the API under
// /api behind CSRF validation, and all of it behind web auth.
func (p *Platform) web(logger lager.Logger, postmaster *postgresrunner.Runner) (http.Handler, func(), error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	store, err := skystorage.NewPostgresStorage(logger, flag.PostgresConfig{
		Host: "127.0.0.1", Port: uint16(postmaster.Port), User: "postgres", Database: "testdb", SSLMode: "disable"})
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (http.Handler, func(), error) { _ = store.Close(); return nil, nil, err }
	dex, err := dexserver.NewDexServer(&dexserver.DexConfig{
		Logger: logger.Session("dex"), PasswordConnector: "local", Users: map[string]string{User: password},
		Clients: map[string]string{"fly": "Zmx5"}, Expiration: 24 * time.Hour,
		IssuerURL: p.URL + "/sky/issuer", RedirectURL: p.URL + "/sky/callback", SigningKey: key, Storage: store,
		RefreshTokenIdleTimeout: time.Hour, RefreshTokenAbsoluteTimeout: 24 * time.Hour, RefreshTokenReuseInterval: time.Second,
	})
	if err != nil {
		return fail(err)
	}
	displayUserID, err := skycmd.NewSkyDisplayUserIdGenerator(nil)
	if err != nil {
		return fail(err)
	}
	users := db.NewUserFactory(p.conn)
	issuer := token.EnsureUser(logger, dexserver.RequireDesktopPKCE(dex), token.NewClaimsParser(), users, displayUserID)
	api, err := p.api(logger, displayUserID, users)
	if err != nil {
		return fail(err)
	}
	middleware := token.NewMiddleware(false)
	csrf := auth.CSRFValidationHandler(api, middleware)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", csrf)
	mux.Handle("/api/v2/", csrf)
	mux.Handle("/sky/issuer/", issuer)
	return wrappa.LoggerHandler{Logger: logger, Handler: auth.WebAuthHandler{Handler: mux, Middleware: middleware}}, func() { _ = store.Close() }, nil
}

// api is atccmd's API handler: every route, the production wrapper chain and
// the Run services of a node with an output plane.
func (p *Platform) api(logger lager.Logger, displayUserID atc.DisplayUserIdGenerator, users db.UserFactory) (http.Handler, error) {
	verifier := accessor.NewTrustedTokenVerifier(accessor.NewJWKSVerifier(p.URL+"/sky/issuer/keys", []string{"fly", "fly-browser"}))
	access := accessor.NewAccessFactory(verifier, p.teams, "", nil, displayUserID)
	workers := db.NewWorkerFactory(p.conn, db.NewStaticWorkerCache(logger, p.conn, 0))
	wrapper := wrappa.MultiWrappa{
		wrappa.NewConcurrentRequestLimitsWrappa(logger, wrappa.NewConcurrentRequestPolicy(nil)),
		wrappa.NewAPIMetricsWrappa(logger),
		wrappa.NewPolicyCheckWrappa(logger, policychecker.NewApiPolicyChecker(policy.NoopChecker{})),
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(p.teams), auth.NewCheckBuildReadAccessHandlerFactory(p.builds),
			auth.NewCheckBuildWriteAccessHandlerFactory(p.builds), auth.NewCheckWorkerTeamAccessHandlerFactory(workers)),
		wrappa.NewRejectArchivedWrappa(pipelineserver.NewRejectArchivedHandlerFactory(p.teams)),
		wrappa.NewConcourseVersionWrappa(concourse.Version),
		wrappa.NewAccessorWrappa(logger, access, auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger), nil),
		wrappa.NewCompressionWrappa(logger),
	}
	services, err := p.runServices(displayUserID)
	if err != nil {
		return nil, err
	}
	dbClock := db.NewClock()
	return api.NewHandler(logger, p.URL, "", "atctest", wrapper,
		p.teams, db.NewPipelineFactory(p.conn, p.locks), p.runs, db.NewJobFactory(p.conn, p.locks),
		db.NewResourceFactory(p.conn, p.locks), workers, p.teams, db.NewVolumeRepository(p.conn), p.builds,
		db.NewCheckFactory(p.conn, p.locks, make(chan db.Build, 64), nil), db.NewResourceConfigFactory(p.conn, p.locks), users, db.NewLandingQueueFactory(p.conn),
		buildserver.NewEventHandler, nil, lager.NewReconfigurableSink(lager.NewWriterSink(os.Stderr, lager.ERROR), lager.ERROR), false, os.TempDir(),
		concourse.Version, concourse.WorkerVersion, concourse.JetBridgeVersion, concourse.ConcourseVersion,
		noop.Noop{}, creds.NewVarSourcePool(logger, creds.CredentialManagementConfig{}, time.Minute, time.Minute, clock.NewClock()), creds.Managers{},
		containerserver.NewInterceptTimeoutFactory(time.Minute), time.Minute, db.NewWall(p.conn, &dbClock), clock.NewClock(),
		db.NewSigningKeyFactory(p.conn), p.conn, nil, services)
}

// runServices is atccmd's configureRunInputUploads, credentialHandoffConfig and
// configureOutputReads over the one output node.
func (p *Platform) runServices(displayUserID atc.DisplayUserIdGenerator) (pipelinerunserver.Services, error) {
	signing := make([]byte, 32)
	if _, err := rand.Read(signing); err != nil {
		return pipelinerunserver.Services{}, err
	}
	authority, err := runinput.NewAuthority(signing, time.Now)
	if err != nil {
		return pipelinerunserver.Services{}, err
	}
	admitter := runs.NewAdmitter(p.conn, p.runs, p.teams, displayUserID, nil)
	admitter.SetOutputEpoch(Epoch)
	admitter.SetSealedInputAuthority(authority)
	admitter.SetCredentialHandoffConfig(runs.CredentialHandoffConfig{
		Source: p.node, Helper: "/usr/local/bin/jb-review-worker", Socket: "/dev/shm/jb-review/auth.sock",
		Lifetime: 32 * time.Minute, WorkerImages: []string{Pin},
	})
	admitter.SetInputUploadConfig(runs.InputUploadConfig{Source: func(context.Context, int64) (runs.InputUploadNode, error) {
		return runs.InputUploadNode{UID: p.node.uid, Publisher: p.node.client}, nil
	}})
	reader := &runs.ResultReader{Conn: p.conn, Minter: p.node.warrants, Scratch: p.node.resultScratch,
		Source: func(context.Context, executioncontrol.ActivationEpoch) (runs.ResultSource, error) {
			return p.node.client, nil
		}}
	return pipelinerunserver.Services{Admitter: admitter, Results: reader, Epoch: Epoch}, nil
}

// NewTeam creates a team the user owns, so a test's templates and Runs are its
// own.
func (p *Platform) NewTeam(t testing.TB) string {
	t.Helper()
	name := fmt.Sprintf("team-%d", p.teamSeq.Add(1))
	if _, err := p.teams.CreateTeam(atc.Team{Name: name, Auth: atc.TeamAuth{"owner": {"users": {"local:" + User}}}}); err != nil {
		t.Fatal(err)
	}
	return name
}

// Install saves config as an unpaused template of team. Each task_id is
// replaced with a fresh one: the platform lets one template own a task ID, and
// tests install the same template into many teams.
func (p *Platform) Install(t testing.TB, team, name string, config atc.Config) {
	t.Helper()
	config.Template = true
	for _, job := range config.Jobs {
		_ = job.StepConfig().Visit(atc.StepRecursor{OnTask: func(task *atc.TaskStep) error {
			if task.TaskID != "" {
				task.TaskID = uuid.NewString()
			}
			return nil
		}})
	}
	found, ok, err := p.teams.FindTeam(team)
	if err != nil || !ok {
		t.Fatalf("team %s: %v", team, err)
	}
	pipeline, _, err := found.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Unpause(); err != nil {
		t.Fatal(err)
	}
}

// Token is the user's token from a password grant, as fly login obtains it.
func (p *Platform) Token(t testing.TB) string {
	t.Helper()
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if p.token != "" {
		return p.token
	}
	config := oauth2.Config{ClientID: "fly", ClientSecret: "Zmx5", Endpoint: oauth2.Endpoint{TokenURL: p.URL + "/sky/issuer/token"},
		Scopes: []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	granted, err := config.PasswordCredentialsToken(ctx, User, password)
	if err != nil {
		t.Fatalf("password grant: %v", err)
	}
	p.token, _ = granted.Extra("id_token").(string)
	if p.token == "" {
		p.token = granted.AccessToken
	}
	return p.token
}

// Client is an HTTP client presenting the user's token.
func (p *Platform) Client(t testing.TB) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 5 * time.Minute, Transport: bearer{token: p.Token(t), next: http.DefaultTransport}}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Admit admits a Run of a template that takes no inputs through the public v2
// route, under a fresh invocation key.
func (p *Platform) Admit(t testing.TB, team, template string) atc.PipelineRun {
	t.Helper()
	body, _ := json.Marshal(atc.CreatePipelineRunV2Request{InvocationKey: fmt.Sprintf("atctest-%d", time.Now().UnixNano())})
	request, err := http.NewRequest(http.MethodPost, p.URL+"/api/v2/teams/"+team+"/pipelines/"+template+"/runs", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client(t).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var run atc.PipelineRun
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&run) != nil {
		t.Fatalf("admitting a Run of %s/%s: HTTP %d", team, template, response.StatusCode)
	}
	return run
}

// Uploads counts the sealed inputs uploaded for template, from the row each
// upload registers.
func (p *Platform) Uploads(t testing.TB, team, template string) int {
	t.Helper()
	return p.count(t, `SELECT count(*) FROM pipeline_run_input_uploads u JOIN pipelines p ON p.id=u.template_pipeline_id
 JOIN teams t ON t.id=p.team_id WHERE t.name=$1 AND p.name=$2`, team, template)
}

// Runs counts the Runs admitted from template.
func (p *Platform) Runs(t testing.TB, team, template string) int {
	t.Helper()
	return p.count(t, `SELECT count(*) FROM pipeline_runs r JOIN pipelines p ON p.id=r.template_pipeline_id
 JOIN teams t ON t.id=p.team_id WHERE t.name=$1 AND p.name=$2`, team, template)
}

func (p *Platform) count(t testing.TB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := p.conn.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
