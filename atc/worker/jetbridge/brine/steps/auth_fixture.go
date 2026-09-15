package steps

// Authentication fixtures compose the production issuer and HTTP handlers.
// They use no worker or Kubernetes apparatus: the protected resource is a
// real private pipeline in the scenario's migrated PostgreSQL database.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/configserver"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/api/usersserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/dexserver"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/skyserver"
	skyStorage "github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/concourse/skymarshal/token"
	dex "github.com/concourse/dex/server"
	dexstorage "github.com/concourse/dex/storage"
	"github.com/concourse/flag/v2"
	"github.com/tedsuo/rata"
	"golang.org/x/net/html"
	"golang.org/x/oauth2"
)

const authPassword = "brine-local-password"

type authBinaries struct {
	Root, Fly string
	Key       *rsa.PrivateKey
}

type AuthFixture struct {
	DB          JetbridgeDB
	Bin         authBinaries
	Home        string
	URL         string
	Server      *httptest.Server
	Store       skyStorage.Storage
	API         http.Handler
	Client      *http.Client
	Cancel      context.CancelFunc
	Dex         *dex.Server
	Verifier    accessor.TokenVerifier
	MCPConn     db.DbConn
	CustomRoles map[string]string
	Policy      policy.Checker

	mu     sync.RWMutex
	extra  http.Handler
	issuer http.Handler
	sky    http.Handler
}

func AuthenticationResourceDefinitions() []brine.ResourceDefinition {
	return []brine.ResourceDefinition{
		{Name: "auth-binaries", Scope: brine.ScopeSuite, Factory: func(map[string]any) (any, error) {
			root, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			for {
				if _, err := os.Stat(filepath.Join(root, "skymarshal/dexserver/dexserver.go")); err == nil {
					break
				}
				parent := filepath.Dir(root)
				if parent == root {
					return nil, errors.New("cannot locate authentication source root")
				}
				root = parent
			}
			dir, err := os.MkdirTemp("", "brine-auth-binaries-")
			if err != nil {
				return nil, err
			}
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				_ = os.RemoveAll(dir)
				return nil, err
			}
			bin := authBinaries{Root: dir, Fly: filepath.Join(dir, "fly"), Key: key}
			cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin.Fly, "./fly")
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				_ = os.RemoveAll(dir)
				return nil, fmt.Errorf("build fly: %w: %s", err, out)
			}
			return bin, nil
		}, Disposer: func(v any) error { return os.RemoveAll(v.(authBinaries).Root) }},
		{Name: "auth-server", Scope: brine.ScopeScenario, DependsOn: []string{"jetbridge-db", "auth-binaries"},
			Factory: func(deps map[string]any) (any, error) {
				return newAuthFixture(deps["jetbridge-db"].(JetbridgeDB), deps["auth-binaries"].(authBinaries))
			}, Disposer: func(v any) error { return v.(*AuthFixture).Close() }},
	}
}

func newAuthFixture(database JetbridgeDB, bin authBinaries) (_ *AuthFixture, err error) {
	f := &AuthFixture{DB: database, Bin: bin}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	f.Home, err = os.MkdirTemp("", "brine-auth-home-")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Cancel = cancel
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	f.Client = &http.Client{Jar: jar, Timeout: 15 * time.Second}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(f.serveHTTP))
	f.URL = "http://" + f.Server.Listener.Addr().String()
	logger := lager.NewLogger("brine-auth")
	keyBytes := make([]byte, 32)
	if _, err = rand.Read(keyBytes); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	f.MCPConn, err = db.NewConn("brine-mcp", database.runner.OpenSingleton(), database.runner.DataSourceName(), nil, encryption.NewKey(gcm))
	if err != nil {
		return nil, err
	}
	f.Store, err = skyStorage.NewPostgresStorage(logger, flag.PostgresConfig{
		Host: "127.0.0.1", User: "postgres", Database: "testdb", Port: uint16(database.runner.Port), SSLMode: "disable",
	})
	if err != nil {
		return nil, err
	}
	config, err := dexserver.NewDexServerConfig(&dexserver.DexConfig{
		Logger: logger, IssuerURL: f.URL + "/sky/issuer", RedirectURL: f.URL + "/sky/callback",
		SigningKey: bin.Key, Expiration: 30 * time.Second,
		Clients:      map[string]string{"brine-web": "brine-web-secret", "fly": "Zmx5"},
		ExtraClients: []dexstorage.Client{{ID: "brine-mcp", Secret: "brine-mcp-secret", RedirectURIs: []string{f.URL + "/mcp/oauth/callback"}}},
		Users:        map[string]string{"owner": authPassword, "viewer": authPassword}, PasswordConnector: "local", Storage: f.Store,
		RefreshTokenIdleTimeout: 30 * time.Minute, RefreshTokenAbsoluteTimeout: time.Hour, RefreshTokenReuseInterval: time.Nanosecond,
	})
	if err != nil {
		return nil, err
	}
	f.Dex, err = dex.NewServerWithKey(ctx, config, bin.Key)
	if err != nil {
		return nil, err
	}
	f.issuer = dexserver.RequireDesktopPKCE(f.Dex)
	stateKey := make([]byte, 32)
	if _, err = rand.Read(stateKey); err != nil {
		return nil, err
	}
	skyConfig := &skyserver.SkyConfig{
		Logger: logger, TokenMiddleware: token.NewMiddleware(false), StateSigningKey: stateKey, HTTPClient: f.Client,
		OAuthConfig: &oauth2.Config{ClientID: "brine-web", ClientSecret: "brine-web-secret", RedirectURL: f.URL + "/sky/callback",
			Endpoint: oauth2.Endpoint{AuthURL: f.URL + "/sky/issuer/auth", TokenURL: f.URL + "/sky/issuer/token"},
			Scopes:   []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"}},
	}
	configureAuthRevocation(f, skyConfig)
	sky, err := skyserver.NewSkyServer(skyConfig)
	if err != nil {
		return nil, err
	}
	f.sky = skyserver.NewSkyHandler(sky)
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "auth-team", Auth: atc.TeamAuth{
		"owner": {"users": {"local:owner"}}, "viewer": {"users": {"local:viewer"}},
	}})
	if err != nil {
		return nil, err
	}
	if _, _, err = team.SavePipeline(atc.PipelineRef{Name: "private"}, atc.Config{Jobs: atc.JobConfigs{}}, 0, false); err != nil {
		return nil, err
	}
	f.Verifier = accessor.NewJWKSVerifier(f.URL+"/sky/issuer/keys", []string{"brine-web", "fly", "fly-browser", "brine-mcp"})
	f.API, err = f.apiHandler(f.Verifier)
	if err != nil {
		return nil, err
	}
	f.Server.Start()
	return f, nil
}

func (f *AuthFixture) apiHandler(verifier accessor.TokenVerifier) (http.Handler, error) {
	logger := lagertest.NewTestLogger("brine-auth-api")
	displayUserID, err := skycmd.NewSkyDisplayUserIdGenerator(nil)
	if err != nil {
		return nil, err
	}
	pipelines := pipelineserver.NewServer(logger, f.DB.TeamFactory, db.NewPipelineFactory(f.DB.Conn, f.DB.LockFactory), f.URL)
	scoped := pipelineserver.NewScopedHandlerFactory(f.DB.TeamFactory)
	builds := buildserver.NewServer(logger, f.URL, f.DB.TeamFactory, f.DB.BuildFactory, buildserver.NewEventHandler)
	buildScoped := buildserver.NewScopedHandlerFactory(logger)
	jobs := jobserver.NewServer(logger, f.URL, noop.Noop{}, db.NewJobFactory(f.DB.Conn, f.DB.LockFactory), db.NewCheckFactory(f.DB.Conn, f.DB.LockFactory, nil, util.NewSequenceGenerator(0)))
	configs := configserver.NewServer(logger, f.DB.TeamFactory, noop.Noop{})
	teams := teamserver.NewServer(logger, f.DB.TeamFactory, f.URL)
	users := usersserver.NewServer(logger, db.NewUserFactory(f.DB.Conn))
	checker := f.Policy
	if checker == nil {
		checker = policy.NoopChecker{}
	}
	handlers := rata.Handlers{
		atc.GetPipeline:           scoped.HandlerFor(pipelines.GetPipeline),
		atc.UnpausePipeline:       scoped.HandlerFor(pipelines.UnpausePipeline),
		atc.ListAllPipelines:      http.HandlerFunc(pipelines.ListAllPipelines),
		atc.ListPipelineBuilds:    scoped.HandlerFor(pipelines.ListPipelineBuilds),
		atc.SaveConfigConditional: http.HandlerFunc(configs.SaveConfigConditional),
		atc.GetBuild:              buildScoped.HandlerFor(builds.GetBuild),
		atc.BuildEvents:           buildScoped.HandlerFor(builds.BuildEvents),
		atc.AbortBuild:            buildScoped.HandlerFor(builds.AbortBuild),
		atc.CreateJobBuild:        scoped.HandlerFor(jobs.CreateJobBuild),
		atc.PausePipeline:         scoped.HandlerFor(pipelines.PausePipeline),
		atc.ListPipelines:         http.HandlerFunc(pipelines.ListPipelines),
		atc.SaveConfig:            http.HandlerFunc(configs.SaveConfig),
		atc.GetConfig:             http.HandlerFunc(configs.GetConfig),
		atc.ListTeams:             http.HandlerFunc(teams.ListTeams),
		atc.GetUser:               http.HandlerFunc(users.GetUser),
		atc.GetInfo: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(atc.Info{Version: concourse.Version, WorkerVersion: "1.2.3"})
		}),
	}
	wrapper := wrappa.MultiWrappa{
		wrappa.NewPolicyCheckWrappa(logger, policychecker.NewApiPolicyChecker(checker)),
		wrappa.NewRejectArchivedWrappa(pipelineserver.NewRejectArchivedHandlerFactory(f.DB.TeamFactory)),
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(f.DB.TeamFactory), auth.NewCheckBuildReadAccessHandlerFactory(f.DB.BuildFactory), auth.NewCheckBuildWriteAccessHandlerFactory(f.DB.BuildFactory), nil),
		wrappa.NewAccessorWrappa(logger, accessor.NewAccessFactory(verifier, f.DB.TeamFactory, "", nil, displayUserID),
			auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger), f.CustomRoles),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	return rata.NewRouter(routes, wrapper.Wrap(handlers))
}

func (f *AuthFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	extra, api := f.extra, f.API
	f.mu.RUnlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/sky/issuer"):
		f.issuer.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/sky/"):
		f.sky.ServeHTTP(w, r)
	case extra != nil && (strings.HasPrefix(r.URL.Path, "/mcp/") || strings.HasPrefix(r.URL.Path, "/.well-known/") || r.URL.Path == "/api/v1/mcp"):
		extra.ServeHTTP(w, r)
	default:
		middleware := token.NewMiddleware(false)
		wrappa.LoggerHandler{Logger: lager.NewLogger("brine-auth-http"), Handler: auth.WebAuthHandler{
			Middleware: middleware, Handler: auth.CSRFValidationHandler(api, middleware),
		}}.ServeHTTP(w, r)
	}
}

func (f *AuthFixture) Close() error {
	if f.Cancel != nil {
		f.Cancel()
	}
	if f.Server != nil {
		f.Server.Close()
	}
	var err error
	if f.MCPConn != nil {
		err = f.MCPConn.Close()
	}
	if f.Store != nil {
		err = errors.Join(err, f.Store.Close())
	}
	if f.Home != "" {
		err = errors.Join(err, os.RemoveAll(f.Home))
	}
	return err
}

func authReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
}

// authForm reads the actual HTML supplied by the service. Hidden state/CSRF
// fields are copied; the caller supplies user-entered fields and checkboxes.
func authForm(body []byte, base *url.URL) (string, url.Values, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", nil, err
	}
	var form *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if form == nil && n.Type == html.ElementNode && n.Data == "form" {
			form = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if form == nil {
		return "", nil, errors.New("response has no HTML form")
	}
	attrs := func(n *html.Node, name string) string {
		for _, a := range n.Attr {
			if a.Key == name {
				return a.Val
			}
		}
		return ""
	}
	action, err := base.Parse(attrs(form, "action"))
	if err != nil {
		return "", nil, err
	}
	values := url.Values{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" {
			name, typ := attrs(n, "name"), attrs(n, "type")
			if name != "" && typ != "checkbox" && typ != "radio" {
				values.Add(name, attrs(n, "value"))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return action.String(), values, nil
}

func (f *AuthFixture) loginForm(client *http.Client, start, user string) (*http.Response, error) {
	resp, err := client.Get(start)
	if err != nil {
		return nil, err
	}
	body, err := authReadBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login page returned HTTP %d", resp.StatusCode)
	}
	action, values, err := authForm(body, resp.Request.URL)
	if err != nil {
		return nil, fmt.Errorf("issuer login page: %w", err)
	}
	values.Set("login", user)
	values.Set("password", authPassword)
	return client.PostForm(action, values)
}
