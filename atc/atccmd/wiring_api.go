// The API members: constructAPIMembers builds the HTTP-facing half of the web
// node -- the API, web, auth and sky handlers, the MCP mount, the TLS and
// debug servers -- and the helpers it needs: the sky HTTP client and TLS
// config, the default team's auth, the token verifier and the TLS redirect.

package atccmd

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/dexserver"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/skyserver"
	"github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/concourse/concourse/web"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"golang.org/x/oauth2"
)

func (cmd *RunCommand) constructAPIMembers(
	logger lager.Logger,
	dbConn db.DbConn,
	workerConn db.DbConn,
	storage storage.Storage,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
	workerCache *db.WorkerCache,
	checkBuildsChan chan db.Build,
) ([]grouper.Member, error) {

	httpClient, err := cmd.skyHttpClient()
	if err != nil {
		return nil, err
	}

	teamFactory := db.NewTeamFactory(dbConn, lockFactory)

	_, err = teamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, err
	}

	err = cmd.configureAuthForDefaultTeam(teamFactory)
	if err != nil {
		return nil, err
	}

	userFactory := db.NewUserFactory(dbConn)

	dbResourceConfigFactory := db.NewResourceConfigFactory(dbConn, lockFactory)

	pool, err := cmd.constructPool(logger, dbConn, lockFactory, workerCache)
	if err != nil {
		return nil, err
	}

	// The worker factory has its own connection pool (for worker registration)
	dbWorkerFactory := db.NewWorkerFactory(workerConn, workerCache)

	credsManagers := cmd.CredentialManagers
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbPipelineRunFactory := db.NewPipelineRunFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbResourceFactory := db.NewResourceFactory(dbConn, lockFactory)
	dbContainerRepository := db.NewContainerRepository(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	gcContainerDestroyer := gc.NewDestroyer(logger, dbContainerRepository, dbVolumeRepository)
	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, checkBuildsChan, nil)
	dbClock := db.NewClock()
	dbWall := db.NewWall(dbConn, &dbClock)

	tokenVerifier := cmd.constructTokenVerifier()

	teamsCacher := accessor.NewTeamsCacher(
		logger,
		dbConn.Bus(),
		teamFactory,
		time.Minute,
		time.Minute,
	)

	displayUserIdGenerator, err := skycmd.NewSkyDisplayUserIdGenerator(cmd.DisplayUserIdPerConnector)
	if err != nil {
		return nil, err
	}

	accessFactory := accessor.NewAccessFactory(
		tokenVerifier,
		teamsCacher,
		cmd.SystemClaimKey,
		cmd.SystemClaimValues,
		displayUserIdGenerator,
	)

	middleware := token.NewMiddleware(cmd.secureCookies())

	apiHandler, err := cmd.constructAPIHandler(
		logger,
		teamFactory,
		dbPipelineFactory,
		dbPipelineRunFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		dbContainerRepository,
		gcContainerDestroyer,
		dbBuildFactory,
		dbCheckFactory,
		dbResourceConfigFactory,
		userFactory,
		pool,
		secretManager,
		credsManagers,
		accessFactory,
		dbWall,
		dbConn,
	)
	if err != nil {
		return nil, err
	}

	webHandler, err := cmd.constructWebHandler(logger)
	if err != nil {
		return nil, err
	}

	authHandler, err := cmd.constructAuthHandler(
		logger,
		storage,
		userFactory,
		displayUserIdGenerator,
	)
	if err != nil {
		return nil, err
	}

	skyHandler, err := cmd.constructSkyHandler(
		logger,
		httpClient,
		middleware,
		storage,
	)
	if err != nil {
		return nil, err
	}

	if err := cmd.constructMCPHandler(logger, dbConn, httpClient, apiHandler, accessFactory); err != nil {
		return nil, err
	}

	var httpHandler, httpsHandler http.Handler
	if cmd.isTLSEnabled() {
		httpHandler = cmd.constructHTTPHandler(
			logger,

			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   webHandler,
			},

			// note: intentionally not wrapping API; redirecting is more trouble than
			// it's worth.

			// we're mainly interested in having the web UI consistently https:// -
			// API requests will likely not respect the redirected https:// URI upon
			// the next request, plus the payload will have already been sent in
			// plaintext
			apiHandler,

			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   authHandler,
			},
			tlsRedirectHandler{
				matchHostname: cmd.ExternalURL.URL.Hostname(),
				externalHost:  cmd.ExternalURL.URL.Host,
				baseHandler:   skyHandler,
			},
			middleware,
		)

		httpsHandler = cmd.constructHTTPHandler(
			logger,
			webHandler,
			apiHandler,
			authHandler,
			skyHandler,
			middleware,
		)
	} else {
		httpHandler = cmd.constructHTTPHandler(
			logger,
			webHandler,
			apiHandler,
			authHandler,
			skyHandler,
			middleware,
		)
	}

	members := []grouper.Member{
		{Name: "debug", Runner: http_server.New(
			cmd.debugBindAddr(),
			http.DefaultServeMux,
		)},
		{Name: "web", Runner: http_server.New(
			cmd.nonTLSBindAddr(),
			httpHandler,
		)},
	}

	if httpsHandler != nil {
		tlsConfig, err := cmd.tlsConfig(logger)
		if err != nil {
			return nil, err
		}
		members = append(members, grouper.Member{Name: "web-tls", Runner: http_server.NewTLSServer(
			cmd.tlsBindAddr(),
			httpsHandler,
			tlsConfig,
		)})
	}

	if cmd.mcpCleanup != nil {
		members = append(members, grouper.Member{Name: "mcp-auth-cleanup", Runner: cmd.mcpCleanup})
	}
	return members, nil
}

func (cmd *RunCommand) constructWebHandler(logger lager.Logger) (http.Handler, error) {
	webHandler, err := web.NewHandler(logger, cmd.WebPublicDir.Path())
	if err != nil {
		return nil, err
	}
	return metric.WrapHandler("web", webHandler), nil
}

func (cmd *RunCommand) skyHttpClient() (*http.Client, error) {
	httpClient := http.DefaultClient

	if cmd.isTLSEnabled() {
		certpool, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}

		cert, err := tls.LoadX509KeyPair(string(cmd.TLSCert), string(cmd.TLSKey))
		if err != nil {
			return nil, err
		}

		x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, err
		}

		certpool.AddCert(x509Cert)

		httpClient.Transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{
				RootCAs: certpool,
			},
		}
	} else {
		httpClient.Transport = http.DefaultTransport
	}

	httpClient.Transport = mitmRoundTripper{
		RoundTripper: httpClient.Transport,

		SourceHost: cmd.ExternalURL.URL.Host,
		TargetURL:  cmd.DefaultURL().URL,
	}

	return httpClient, nil
}

type mitmRoundTripper struct {
	http.RoundTripper

	SourceHost string
	TargetURL  *url.URL
}

func (tripper mitmRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == tripper.SourceHost {
		req.URL.Scheme = tripper.TargetURL.Scheme
		req.URL.Host = tripper.TargetURL.Host
	}

	return tripper.RoundTripper.RoundTrip(req)
}

func (cmd *RunCommand) tlsConfig(logger lager.Logger) (*tls.Config, error) {
	tlsConfig := atc.DefaultTLSConfig()

	if cmd.isTLSEnabled() {
		tlsLogger := logger.Session("tls-enabled")

		if cmd.isMTLSEnabled() {
			tlsLogger.Debug("mTLS-Enabled")
			clientCACert, err := os.ReadFile(string(cmd.TLSCaCert))
			if err != nil {
				return nil, err
			}
			clientCertPool := x509.NewCertPool()
			clientCertPool.AppendCertsFromPEM(clientCACert)

			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
			tlsConfig.ClientCAs = clientCertPool
		}

		tlsLogger.Debug("loading-tls-certs")
		cert, err := tls.LoadX509KeyPair(string(cmd.TLSCert), string(cmd.TLSKey))
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return tlsConfig, nil
}

// secureCookies says whether web marks its cookies Secure: always when it is
// reached over https, which is the one right answer for a cookie served over
// TLS, and otherwise only when --cookie-secure forces it (a TLS-terminating
// proxy in front of an http external URL). MCP's authorization cookies
// already followed the issuer's scheme; session cookies now do too.
func (cmd *RunCommand) secureCookies() bool {
	return cmd.Auth.AuthFlags.SecureCookies ||
		(cmd.ExternalURL.URL != nil && cmd.ExternalURL.URL.Scheme == "https")
}

func (cmd *RunCommand) configureAuthForDefaultTeam(teamFactory db.TeamFactory) error {
	team, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
	if err != nil {
		return err
	}

	if !found {
		return errors.New("default team not found")
	}

	auth, err := cmd.Auth.MainTeamFlags.Format()
	if err != nil {
		return fmt.Errorf("default team auth not configured: %v", err)
	}

	return team.UpdateProviderAuth(auth)
}

func (cmd *RunCommand) constructHTTPHandler(
	logger lager.Logger,
	webHandler http.Handler,
	apiHandler http.Handler,
	authHandler http.Handler,
	skyHandler http.Handler,
	middleware token.Middleware,
) http.Handler {

	csrfHandler := auth.CSRFValidationHandler(
		apiHandler,
		middleware,
	)

	webMux := http.NewServeMux()
	webMux.Handle("/api/v1/", csrfHandler)
	webMux.Handle("/api/v2/", csrfHandler)
	webMux.Handle("/sky/issuer/", authHandler)
	webMux.Handle("/sky/", skyHandler)
	webMux.Handle("/", webHandler)

	// MCP uses its own bearer credentials and browser consent cookies. In
	// particular an MCP 401 must not clear an unrelated web login's cookies.
	routes := http.NewServeMux()
	routes.Handle("/", auth.WebAuthHandler{Handler: webMux, Middleware: middleware})

	if cmd.mcpHandler != nil {
		for _, path := range []string{"/api/v1/mcp", "/mcp/oauth/", "/.well-known/oauth-authorization-server/mcp/oauth", "/.well-known/oauth-protected-resource/api/v1/mcp"} {
			routes.Handle(path, cmd.mcpHandler)
		}
	}

	return wrappa.LoggerHandler{
		Logger: logger,

		Handler: wrappa.SecurityHandler{
			XFrameOptions:           cmd.Server.XFrameOptions,
			ContentSecurityPolicy:   cmd.Server.ContentSecurityPolicy,
			StrictTransportSecurity: cmd.Server.StrictTransportSecurity,

			Handler: routes,
		},
	}
}

func (cmd *RunCommand) constructAuthHandler(
	logger lager.Logger,
	storage storage.Storage,
	userFactory db.UserFactory,
	displayUserIdGenerator atc.DisplayUserIdGenerator,
) (http.Handler, error) {

	issuerPath, _ := url.Parse("/sky/issuer")
	redirectPath, _ := url.Parse("/sky/callback")

	issuerURL := cmd.ExternalURL.URL.ResolveReference(issuerPath)
	redirectURL := cmd.ExternalURL.URL.ResolveReference(redirectPath)

	// Add public fly client
	cmd.Auth.AuthFlags.Clients[flyClientID] = flyClientSecret

	dexServer, err := dexserver.NewDexServer(&dexserver.DexConfig{
		Logger:                      logger.Session("dex"),
		PasswordConnector:           cmd.Auth.AuthFlags.PasswordConnector,
		Users:                       cmd.Auth.AuthFlags.LocalUsers,
		Clients:                     cmd.Auth.AuthFlags.Clients,
		Expiration:                  cmd.Auth.AuthFlags.Expiration,
		IssuerURL:                   issuerURL.String(),
		RedirectURL:                 redirectURL.String(),
		SigningKey:                  cmd.Auth.AuthFlags.SigningKey.PrivateKey,
		Storage:                     storage,
		RefreshTokenIdleTimeout:     cmd.Auth.AuthFlags.RefreshTokenIdleTimeout,
		RefreshTokenAbsoluteTimeout: cmd.Auth.AuthFlags.RefreshTokenAbsoluteTimeout,
		RefreshTokenReuseInterval:   cmd.Auth.AuthFlags.RefreshTokenReuseInterval,
		ExtraClients:                cmd.mcpIdentityClients(),
	})
	if err != nil {
		return nil, err
	}

	// Dex serves /sky/issuer/* endpoints directly — no token interception.
	// JWT validation happens in the JWKS verifier on API requests.
	return token.EnsureUser(
		logger.Session("dex-server"),
		dexserver.RequireDesktopPKCE(dexServer),
		token.NewClaimsParser(),
		userFactory,
		displayUserIdGenerator,
	), nil
}

func (cmd *RunCommand) constructSkyHandler(
	logger lager.Logger,
	httpClient *http.Client,
	middleware token.Middleware,
	store storage.Storage,
) (http.Handler, error) {

	authPath, _ := url.Parse("/sky/issuer/auth")
	tokenPath, _ := url.Parse("/sky/issuer/token")
	redirectPath, _ := url.Parse("/sky/callback")

	authURL := cmd.ExternalURL.URL.ResolveReference(authPath)
	tokenURL := cmd.ExternalURL.URL.ResolveReference(tokenPath)
	redirectURL := cmd.ExternalURL.URL.ResolveReference(redirectPath)

	endpoint := oauth2.Endpoint{
		AuthURL:   authURL.String(),
		TokenURL:  tokenURL.String(),
		AuthStyle: oauth2.AuthStyleInHeader,
	}

	oauth2Config := &oauth2.Config{
		Endpoint:     endpoint,
		ClientID:     cmd.Server.ClientID,
		ClientSecret: cmd.Server.ClientSecret,
		RedirectURL:  redirectURL.String(),
		Scopes:       []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"},
	}

	skyServer, err := skyserver.NewSkyServer(&skyserver.SkyConfig{
		Logger:               logger.Session("sky"),
		TokenMiddleware:      middleware,
		OAuthConfig:          oauth2Config,
		HTTPClient:           httpClient,
		RevokeRefresh:        newRefreshRevoker(store, logger.Session("revoke-refresh")),
		RefreshTokenLifetime: cmd.Auth.AuthFlags.RefreshTokenIdleTimeout,
		StateSigningKey: deriveStateSigningKey(
			oauth2Config.ClientID,
			oauth2Config.ClientSecret,
			cmd.Postgres.User,
			cmd.Postgres.Password),
	})
	if err != nil {
		return nil, err
	}

	return skyserver.NewSkyHandler(skyServer), nil
}

func deriveStateSigningKey(clientID, clientSecret, dbUser, dbPassword string) []byte {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(clientID))
	mac.Write([]byte(dbUser))
	mac.Write([]byte(dbPassword))
	return mac.Sum(nil)
}

func (cmd *RunCommand) constructTokenVerifier() accessor.TokenVerifier {
	validClients := []string{flyClientID, "fly-browser"}
	for clientId := range cmd.Auth.AuthFlags.Clients {
		validClients = append(validClients, clientId)
	}

	issuerPath, _ := url.Parse("/sky/issuer/keys")
	jwksURL := cmd.ExternalURL.URL.ResolveReference(issuerPath)

	return accessor.NewTrustedTokenVerifier(accessor.NewJWKSVerifier(jwksURL.String(), validClients))
}

func (cmd *RunCommand) constructAPIHandler(
	logger lager.Logger,
	teamFactory db.TeamFactory,
	dbPipelineFactory db.PipelineFactory,
	dbPipelineRunFactory db.PipelineRunFactory,
	dbJobFactory db.JobFactory,
	dbResourceFactory db.ResourceFactory,
	dbWorkerFactory db.WorkerFactory,
	dbContainerRepository db.ContainerRepository,
	gcContainerDestroyer gc.Destroyer,
	dbBuildFactory db.BuildFactory,
	dbCheckFactory db.CheckFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	dbUserFactory db.UserFactory,
	workerPool worker.Pool,
	secretManager creds.Secrets,
	credsManagers creds.Managers,
	accessFactory accessor.AccessFactory,
	dbWall db.Wall,
	dbConn db.DbConn,
) (http.Handler, error) {

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(teamFactory)
	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(dbBuildFactory)
	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(dbBuildFactory)

	rejectArchivedHandlerFactory := pipelineserver.NewRejectArchivedHandlerFactory(teamFactory)

	customRoles, err := cmd.loadCustomRoles()
	if err != nil {
		return nil, err
	}

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewAPIMetricsWrappa(),
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
		),
		wrappa.NewRejectArchivedWrappa(rejectArchivedHandlerFactory),
		wrappa.NewConcourseVersionWrappa(concourse.Version),
		wrappa.NewAccessorWrappa(
			logger,
			accessFactory,
			customRoles,
		),
		wrappa.NewCompressionWrappa(logger),
	}

	return api.NewHandler(
		logger,
		cmd.ExternalURL.String(),
		cmd.Server.ClusterName,
		apiWrapper,

		teamFactory,
		dbPipelineFactory,
		dbPipelineRunFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		dbBuildFactory,
		dbCheckFactory,
		resourceConfigFactory,
		dbUserFactory,
		db.NewLandingQueueFactory(dbConn),

		buildserver.NewEventHandler,

		workerPool,

		cmd.isTLSEnabled(),

		cmd.CLIArtifactsDir.Path(),
		concourse.Version,
		concourse.WorkerVersion,
		concourse.JetBridgeVersion,
		concourse.ConcourseVersion,
		secretManager,
		cmd.varSourcePool,
		credsManagers,
		containerserver.NewInterceptTimeoutFactory(cmd.InterceptIdleTimeout),
		time.Minute,
		dbWall,
		clock.NewClock(),
		dbConn,
		cmd.hangarStatusSource(dbConn),
		cmd.pipelineRunServices(dbConn, dbPipelineRunFactory, teamFactory),
	)
}

type tlsRedirectHandler struct {
	matchHostname string
	externalHost  string
	baseHandler   http.Handler
}

func (h tlsRedirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Host, h.matchHostname) && (r.Method == "GET" || r.Method == "HEAD") {
		u := url.URL{
			Scheme:   "https",
			Host:     h.externalHost,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}

		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	} else {
		h.baseHandler.ServeHTTP(w, r)
	}
}

func (cmd *RunCommand) isTLSEnabled() bool {
	return cmd.TLSBindPort != 0
}

func (cmd *RunCommand) isMTLSEnabled() bool {
	return string(cmd.TLSCaCert) != ""
}
