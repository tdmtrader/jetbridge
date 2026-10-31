package atccmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/artifactcap"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/pauser"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/dexserver"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/skyserver"
	"github.com/concourse/concourse/skymarshal/storage"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/concourse/concourse/web"
	"github.com/go-jose/go-jose/v4/jwt"
	gocache "github.com/patrickmn/go-cache"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
	"go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	// dynamically registered metric emitters
	_ "github.com/concourse/concourse/atc/metric/emitter"

	// dynamically registered policy checkers

	// dynamically registered credential managers
	_ "github.com/concourse/concourse/atc/creds/kubernetes"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const algorithmLimitRows = 100

var schedulerCache = gocache.New(10*time.Second, 10*time.Second)

var defaultDriverName = "pgx"
var retryingDriverName = "retrying"

var flyClientID = "fly"
var flyClientSecret = "Zmx5"

func (cmd *RunCommand) Execute(args []string) error {
	runner, err := cmd.Runner(args)
	if err != nil {
		return err
	}

	return <-ifrit.Invoke(sigmon.New(runner)).Wait()
}

// publishCheckIntervals sets the check intervals the checker reads. In case a
// user configures resource-checking-interval but forgets to configure
// resource-with-webhook-checking-interval or resource-type-checking-interval,
// both are raised to it first: there is no reason webhooked resources or
// resource types take a shorter checking interval than normal resources. The
// raise must happen before the globals are set, because nothing reads the
// flag fields afterwards.
func (cmd *RunCommand) publishCheckIntervals(logger lager.Logger) {
	if cmd.ResourceWithWebhookCheckingInterval < cmd.ResourceCheckingInterval {
		logger.Info("update-resource-with-webhook-checking-interval",
			lager.Data{
				"oldValue": cmd.ResourceWithWebhookCheckingInterval,
				"newValue": cmd.ResourceCheckingInterval,
			})
		cmd.ResourceWithWebhookCheckingInterval = cmd.ResourceCheckingInterval
	}

	if cmd.ResourceTypeCheckingInterval < cmd.ResourceCheckingInterval {
		logger.Info("update-resource-type-checking-interval",
			lager.Data{
				"oldValue": cmd.ResourceTypeCheckingInterval,
				"newValue": cmd.ResourceCheckingInterval,
			})
		cmd.ResourceTypeCheckingInterval = cmd.ResourceCheckingInterval
	}

	atc.DefaultCheckInterval = cmd.ResourceCheckingInterval
	atc.DefaultWebhookInterval = cmd.ResourceWithWebhookCheckingInterval
	atc.DefaultResourceTypeInterval = cmd.ResourceTypeCheckingInterval
}

func (cmd *RunCommand) Runner(positionalArguments []string) (ifrit.Runner, error) {
	if cmd.ExternalURL.URL == nil {
		cmd.ExternalURL = cmd.DefaultURL()
	}

	if len(positionalArguments) != 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %v", positionalArguments)
	}

	err := cmd.validate()
	if err != nil {
		return nil, err
	}

	logger, _ := cmd.Logger.Logger("atc")
	if cmd.LogClusterName {
		logger = logger.WithData(lager.Data{
			"cluster": cmd.Server.ClusterName,
		})
	}

	commandSession := logger.Session("cmd")
	startTime := time.Now()

	commandSession.Info("start")
	defer func() {
		commandSession.Info("finish", lager.Data{
			"duration": time.Since(startTime),
		})
	}()

	cmd.publishCheckIntervals(logger)
	atc.DisableRedactSecrets = cmd.DisableRedactSecrets

	if cmd.BaseResourceTypeDefaults.Path() != "" {
		content, err := os.ReadFile(cmd.BaseResourceTypeDefaults.Path())
		if err != nil {
			return nil, err
		}

		defaults := map[string]atc.Source{}
		err = yaml.Unmarshal(content, &defaults)
		if err != nil {
			return nil, err
		}

		atc.LoadBaseResourceTypeDefaults(defaults)
	}

	db.SetupConnectionRetryingDriver(
		defaultDriverName,
		cmd.Postgres.ConnectionString(),
		retryingDriverName,
	)

	// Register the sink that collects error metrics
	if cmd.Metrics.CaptureErrorMetrics {
		errorSinkCollector := metric.NewErrorSinkCollector(
			logger,
			metric.Metrics,
		)
		logger.RegisterSink(&errorSinkCollector)
	}

	err = cmd.Tracing.Prepare()
	if err != nil {
		return nil, err
	}

	// Connection tracker is off by default. Can be turned on/ff at runtime.
	http.HandleFunc("/debug/connections", func(w http.ResponseWriter, r *http.Request) {
		for _, stack := range db.GlobalConnectionTracker.Current() {
			fmt.Fprintln(w, stack)
		}
	})
	http.HandleFunc("/debug/connections/on", func(w http.ResponseWriter, r *http.Request) {
		db.InitConnectionTracker(true)
	})
	http.HandleFunc("/debug/connections/off", func(w http.ResponseWriter, r *http.Request) {
		db.InitConnectionTracker(false)
	})

	if err := cmd.configureMetrics(logger); err != nil {
		return nil, err
	}

	lockConns, err := constructLockConns(retryingDriverName, cmd.Postgres.ConnectionString())
	if err != nil {
		return nil, err
	}

	lockFactory := lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)

	apiConn, err := cmd.constructDBConn(retryingDriverName, logger, cmd.APIMaxOpenConnections, cmd.APIMaxOpenConnections/2, "api", lockFactory)
	if err != nil {
		return nil, err
	}

	backendConn, err := cmd.constructDBConn(retryingDriverName, logger, cmd.BackendMaxOpenConnections, cmd.BackendMaxOpenConnections/2, "backend", lockFactory)
	if err != nil {
		return nil, err
	}

	gcConn, err := cmd.constructDBConn(retryingDriverName, logger, 5, 2, "gc", lockFactory)
	if err != nil {
		return nil, err
	}

	workerConn, err := cmd.constructDBConn(retryingDriverName, logger, 1, 1, "worker", lockFactory)
	if err != nil {
		return nil, err
	}

	err = db.CacheWarmUp(backendConn)
	if err != nil {
		return nil, err
	}

	if err := cmd.reconcileHangarEnabled(logger, backendConn); err != nil {
		for _, conn := range []Closer{apiConn, backendConn, gcConn, workerConn} {
			_ = conn.Close()
		}
		return nil, err
	}

	storage, err := storage.NewPostgresStorage(logger, cmd.Postgres)
	if err != nil {
		return nil, err
	}

	secretManager, err := cmd.secretManager(logger)
	if err != nil {
		return nil, err
	}

	members, err := cmd.constructMembers(logger, apiConn, workerConn, backendConn, gcConn, storage, lockFactory, secretManager)
	if err != nil {
		return nil, err
	}

	members = append(members, grouper.Member{
		Name: "periodic-metrics",
		Runner: metric.PeriodicallyEmit(
			logger.Session("periodic-metrics"),
			metric.Metrics,
			10*time.Second,
		),
	})

	onReady := func() {
		logData := lager.Data{
			"http":  cmd.nonTLSBindAddr(),
			"debug": cmd.debugBindAddr(),
		}

		if cmd.isTLSEnabled() {
			logData["https"] = cmd.tlsBindAddr()
		}

		logger.Info("listening", logData)
	}

	onExit := func() {
		for _, closer := range []Closer{apiConn, backendConn, gcConn, storage, workerConn} {
			closer.Close()
		}
		for _, closer := range lockConns {
			closer.Close()
		}
		if cmd.hangarOutputStoreClose != nil {
			_ = cmd.hangarOutputStoreClose()
		}
	}

	return run(grouper.NewParallel(os.Interrupt, members), onReady, onExit), nil
}

func (cmd *RunCommand) constructMembers(
	logger lager.Logger,
	apiConn db.DbConn,
	workerConn db.DbConn,
	backendConn db.DbConn,
	gcConn db.DbConn,
	storage storage.Storage,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
) ([]grouper.Member, error) {
	workerCache, err := db.NewWorkerCache(logger.Session("worker-cache"), backendConn, 1*time.Minute)
	if err != nil {
		return nil, err
	}
	checkBuildsChan := make(chan db.Build, 2000)
	apiMembers, err := cmd.constructAPIMembers(logger, apiConn, workerConn, storage, lockFactory, secretManager, workerCache, checkBuildsChan)
	if err != nil {
		return nil, err
	}

	backendComponents, err := cmd.backendComponents(logger, backendConn, lockFactory, secretManager, workerCache, checkBuildsChan)
	if err != nil {
		return nil, err
	}

	gcComponents, err := cmd.gcComponents(logger, gcConn, lockFactory)
	if err != nil {
		return nil, err
	}

	// use backendConn so that the Component objects created by the factory uses
	// the backend connection pool when reloading.
	componentFactory := db.NewComponentFactory(backendConn)
	bus := backendConn.Bus()

	// Default polling interval for components that don't specify one.
	// Components with NOTIFY triggers will wake immediately on signals;
	// this interval is a safety net so that components without triggers
	// (scheduler, GC collectors, k8s reaper, etc.) still run periodically.
	const defaultComponentInterval = 10 * time.Second

	members := apiMembers
	components := append(backendComponents, gcComponents...)
	for _, c := range components {
		dbComponent, err := componentFactory.CreateOrUpdate(c.Component)
		if err != nil {
			return nil, err
		}

		componentLogger := logger.Session(c.Component.Name)

		interval := c.Interval
		if interval == 0 {
			interval = defaultComponentInterval
		}

		members = append(members, grouper.Member{
			Name: c.Component.Name,
			Runner: &component.Runner{
				Logger:    componentLogger,
				Interval:  interval,
				Component: dbComponent,
				Bus:       bus,
				Schedulable: &component.Coordinator{
					Locker:    lockFactory,
					Component: dbComponent,
					Runnable:  c.Runnable,
				},
			},
		})

		if drainable, ok := c.Runnable.(component.Drainable); ok {
			members = append(members, grouper.Member{
				Name: c.Component.Name + "-drainer",
				Runner: drainRunner{
					logger:  componentLogger.Session("drain"),
					drainer: drainable,
				},
			})
		}
	}

	return members, nil
}

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

func (cmd *RunCommand) backendComponents(
	logger lager.Logger,
	dbConn db.DbConn,
	lockFactory lock.LockFactory,
	secretManager creds.Secrets,
	workerCache *db.WorkerCache,
	checkBuildsChan chan db.Build,
) ([]RunnableComponent, error) {

	teamFactory := db.NewTeamFactory(dbConn, lockFactory)

	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbResourceConfigFactory := db.NewResourceConfigFactory(dbConn, lockFactory)

	dbBuildFactory := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbCheckFactory := db.NewCheckFactory(dbConn, lockFactory, checkBuildsChan, util.NewSequenceGenerator(1))
	dbPipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
	dbJobFactory := db.NewJobFactory(dbConn, lockFactory)
	dbPipelineLifecycle := db.NewPipelineLifecycle(dbConn, lockFactory)
	dbPipelinePauser := db.NewPipelinePauser(dbConn, lockFactory)

	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)

	alg := algorithm.New(db.NewVersionsDB(dbConn, algorithmLimitRows, schedulerCache))

	pool, err := cmd.constructPool(logger, dbConn, lockFactory, workerCache)
	if err != nil {
		return nil, err
	}

	defaultLimits, err := cmd.parseDefaultLimits()
	if err != nil {
		return nil, err
	}

	defaultRequests, err := cmd.parseDefaultRequests()
	if err != nil {
		return nil, err
	}

	rateLimiter := db.NewResourceCheckRateLimiter(
		rate.Limit(cmd.MaxChecksPerSecond),
		rate.Limit(1),
		cmd.ResourceCheckingInterval,
		dbConn,
		time.Minute,
		clock.NewClock(),
	)

	imgResolver := imageresolver.NewResolver(nil)

	childRunAdmitter, err := cmd.constructChildRunAdmitter(dbConn, lockFactory, teamFactory)
	if err != nil {
		return nil, err
	}

	engine := cmd.constructEngine(
		pool,
		dbWorkerFactory,
		teamFactory,
		dbBuildFactory,
		dbResourceCacheFactory,
		dbResourceConfigFactory,
		secretManager,
		defaultLimits,
		defaultRequests,
		lockFactory,
		rateLimiter,
		imgResolver,
		childRunAdmitter,
	)

	components := []RunnableComponent{
		{
			Component: atc.Component{
				Name: atc.ComponentLidarScanner,
			},
			Runnable: lidar.NewScanner(
				dbCheckFactory,
				atc.NewPlanFactory(time.Now().Unix()),
				1000,
				imgResolver,
				dbResourceConfigFactory,
				secretManager,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentPipelinePauser,
			},
			Runnable: pauser.NewPipelinePauser(
				dbPipelinePauser,
				cmd.PausePipelinesAfter,
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentScheduler,
			},
			Runnable: scheduler.NewRunner(
				logger.Session("scheduler"),
				dbJobFactory,
				&scheduler.Scheduler{
					Algorithm: alg,
					BuildStarter: scheduler.NewBuildStarter(
						builds.NewPlanner(atc.NewPlanFactory(time.Now().Unix())),
						alg),
				},
				cmd.JobSchedulingMaxInFlight,
				dbConn.Bus(),
			),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentBuildTracker,
			},
			Runnable: builds.NewTracker(logger, dbBuildFactory, engine, checkBuildsChan),
		},
		{
			Component: atc.Component{
				Name: atc.ComponentBuildReaper,
			},
			Runnable: gc.NewBuildLogCollector(
				dbPipelineFactory,
				dbPipelineLifecycle,
				500,
				gc.NewBuildLogRetentionCalculator(
					cmd.DefaultBuildLogsToRetain,
					cmd.MaxBuildLogsToRetain,
					cmd.DefaultDaysToRetainBuildLogs,
					cmd.MaxDaysToRetainBuildLogs,
				),
			),
		},
	}

	k8sComponents, err := cmd.jetbridgeComponents(logger, dbConn, dbWorkerFactory, dbBuildFactory)
	if err != nil {
		return nil, err
	}
	components = append(components, k8sComponents...)

	components = append(components, cmd.hangarOutputComponents(dbConn)...)
	deletes, err := cmd.hangarOutputDeleteComponents(dbConn, lockFactory)
	if err != nil {
		return nil, err
	}
	components = append(components, deletes...)
	components = append(components, cmd.runComponents(dbConn)...)
	if landingQueue, ok := cmd.landingQueueComponent(logger, dbConn); ok {
		components = append(components, landingQueue)
	}

	return components, err
}

// jetbridgeComponents builds the JetBridge runtime's own components: the
// registrar that heartbeats the synthetic worker and the Reaper. None when the
// runtime is off.
func (cmd *RunCommand) jetbridgeComponents(logger lager.Logger, dbConn db.DbConn, dbWorkerFactory db.WorkerFactory, dbBuildFactory db.BuildFactory) ([]RunnableComponent, error) {
	var components []RunnableComponent
	if cmd.Kubernetes.Namespace != "" {
		rt, err := cmd.jetbridgeConfig()
		if err != nil {
			return nil, err
		}
		k8sCfg, k8sClientset := rt.config, rt.clientset

		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentK8sWorkerRegistrar,
			},
			Runnable: jetbridge.NewRegistrar(logger.Session(atc.ComponentK8sWorkerRegistrar), k8sClientset, k8sCfg, dbWorkerFactory),
			Interval: 15 * time.Second, // heartbeat TTL is 30s, so re-register every 15s
		})

		k8sContainerRepo := db.NewContainerRepository(dbConn)
		k8sVolumeRepo := db.NewVolumeRepository(dbConn)
		k8sDestroyer := gc.NewDestroyer(logger, k8sContainerRepo, k8sVolumeRepo)
		k8sReaper := jetbridge.NewReaper(logger.Session(atc.ComponentK8sWorkerReaper), k8sClientset, k8sCfg, k8sContainerRepo, k8sDestroyer)
		// The workers' locator, never a fresh one: the Reaper can only drop,
		// and forget, the keys the workers recorded.
		k8sReaper.SetArtifactLocator(cmd.artifactLocator())
		// The reaper reaps a completed step's pod only once its build is no
		// longer running: until then the pod's exit-status annotation is the
		// only thing that lets a restarted web resume the plan instead of
		// re-executing the step.
		k8sReaper.SetBuildLookup(dbBuildFactory)
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentK8sWorkerReaper,
			},
			Runnable: k8sReaper,
		})
	}

	return components, nil
}

func (cmd *RunCommand) streamer() worker.Streamer {
	return worker.NewStreamer(compression.NewGzipCompression())
}

// The shared clientset's client-side rate limit: three times client-go's
// default (5 QPS, burst 10), one share for each clientset it replaced.
const (
	jetbridgeClientQPS   = 15
	jetbridgeClientBurst = 30
)

// jetbridgeRuntime is the JetBridge runtime configuration, assembled once from
// the --kubernetes-* flags, with the one clientset built from it.
type jetbridgeRuntime struct {
	config     jetbridge.Config
	clientset  kubernetes.Interface
	restConfig *rest.Config
}

// jetbridgeConfig returns the runtime configuration every JetBridge consumer
// shares: both pools' worker factories, the registrar and the Reaper. It is
// assembled, checked and given its clientset on the first call; later calls
// get the same result, a failure included. Each caller gets its own copy of
// the Config value.
func (cmd *RunCommand) jetbridgeConfig() (jetbridgeRuntime, error) {
	cmd.k8sRuntimeOnce.Do(func() {
		cmd.k8sRuntime, cmd.k8sRuntimeErr = cmd.assembleJetbridgeRuntime()
	})
	return cmd.k8sRuntime, cmd.k8sRuntimeErr
}

func (cmd *RunCommand) assembleJetbridgeRuntime() (jetbridgeRuntime, error) {
	cfg, err := cmd.assembleJetbridgeConfig()
	if err != nil {
		return jetbridgeRuntime{}, err
	}
	restConfig, err := jetbridge.RestConfig(cfg)
	if err != nil {
		return jetbridgeRuntime{}, fmt.Errorf("creating k8s rest config: %w", err)
	}
	// The ATC used to build three clientsets (one per pool, one for the
	// registrar and Reaper), each with client-go's default rate limit of 5 QPS
	// and burst 10. They are now one, so it carries their combined budget
	// rather than a third of it.
	restConfig.QPS = jetbridgeClientQPS
	restConfig.Burst = jetbridgeClientBurst
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return jetbridgeRuntime{}, fmt.Errorf("creating k8s clientset: %w", err)
	}
	return jetbridgeRuntime{config: cfg, clientset: clientset, restConfig: restConfig}, nil
}

// assembleJetbridgeConfig maps the flags onto a jetbridge.Config and runs the
// startup guards that need the assembled value.
func (cmd *RunCommand) assembleJetbridgeConfig() (jetbridge.Config, error) {
	if cmd.Kubernetes.CacheStore != "" && !jetbridge.ValidCacheStores[cmd.Kubernetes.CacheStore] {
		return jetbridge.Config{}, fmt.Errorf("invalid --kubernetes-cache-store value %q (valid: hostpath, emptydir)", cmd.Kubernetes.CacheStore)
	}
	key, err := cmd.loadArtifactResolveCapabilityKey()
	if err != nil {
		return jetbridge.Config{}, err
	}

	k8sCfg := jetbridge.NewConfig(cmd.Kubernetes.Namespace, cmd.Kubernetes.Kubeconfig)
	k8sCfg.PodStartupTimeout = cmd.Kubernetes.PodStartupTimeout
	k8sCfg.PodSchedulingTimeout = cmd.Kubernetes.PodSchedulingTimeout
	if cmd.Kubernetes.PreferredStepNode != "" {
		preferred, err := jetbridge.ParseStepNodeLabel(cmd.Kubernetes.PreferredStepNode)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-preferred-step-node: %w", err)
		}
		k8sCfg.PreferredStepNode = &preferred
	}
	if cmd.Kubernetes.RequiredStepNode != "" {
		required, err := jetbridge.ParseStepNodeLabel(cmd.Kubernetes.RequiredStepNode)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-required-step-node: %w", err)
		}
		k8sCfg.RequiredStepNode = &required
	}
	k8sCfg.ImagePullSecrets = cmd.Kubernetes.ImagePullSecrets
	k8sCfg.ServiceAccount = cmd.Kubernetes.ServiceAccount
	for _, value := range cmd.Kubernetes.StepPodGrants {
		grant, err := jetbridge.ParseStepPodGrant(value)
		if err != nil {
			return jetbridge.Config{}, fmt.Errorf("--kubernetes-step-pod-grant: %w", err)
		}
		k8sCfg.StepPodGrants = append(k8sCfg.StepPodGrants, grant)
	}
	if err := jetbridge.ValidateStepPodGrants(k8sCfg.StepPodGrants); err != nil {
		return jetbridge.Config{}, fmt.Errorf("--kubernetes-step-pod-grant: %w", err)
	}
	k8sCfg.CacheStore = cmd.Kubernetes.CacheStore
	k8sCfg.CacheHostPath = cmd.Kubernetes.CacheHostPath
	k8sCfg.ArtifactHelperImage = cmd.Kubernetes.ArtifactHelperImage
	k8sCfg.ArtifactDaemonPort = cmd.Kubernetes.ArtifactDaemonPort
	k8sCfg.ArtifactDaemonHostPath = cmd.Kubernetes.ArtifactDaemonHostPath
	k8sCfg.ArtifactDaemonResolveCapabilityKey = key
	k8sCfg.ArtifactDaemonService = cmd.Kubernetes.ArtifactDaemonService
	k8sCfg.ArtifactDaemonNamespace = cmd.Kubernetes.ArtifactDaemonNamespace
	k8sCfg.ArtifactDaemonTLSCert = cmd.Kubernetes.ArtifactDaemonTLSCert
	k8sCfg.ArtifactDaemonTLSKey = cmd.Kubernetes.ArtifactDaemonTLSKey
	k8sCfg.ArtifactDaemonTLSCACert = cmd.Kubernetes.ArtifactDaemonTLSCACert
	k8sCfg.ArtifactDaemonTLSEnabled = jetbridge.DaemonTLSConfigured(
		cmd.Kubernetes.ArtifactDaemonTLSCert,
		cmd.Kubernetes.ArtifactDaemonTLSKey,
		cmd.Kubernetes.ArtifactDaemonTLSCACert,
	)
	k8sCfg.OutputPlaneEnabled = cmd.Kubernetes.OutputPlaneEnabled
	k8sCfg.OutputOperationTimeout = cmd.Kubernetes.OutputOperationTimeout
	if cmd.Kubernetes.ImageRegistrySecret != "" {
		k8sCfg.ImageRegistry = &jetbridge.ImageRegistryConfig{
			SecretName: cmd.Kubernetes.ImageRegistrySecret,
		}
	}
	if len(cmd.Kubernetes.BaseResourceTypes) > 0 {
		k8sCfg.ResourceTypeImages = imageresolver.MergeResourceTypeImages(cmd.Kubernetes.BaseResourceTypes)
	}

	if err := jetbridge.ValidateResolveCapabilityConfig(k8sCfg); err != nil {
		return jetbridge.Config{}, err
	}
	return k8sCfg, nil
}

// artifactLocator is the one artifact locator of this web: every pool's worker
// factory records into it and the Reaper drops from it. It is the only place a
// locator is created. Without one, workers skip recording output locations.
func (cmd *RunCommand) artifactLocator() *jetbridge.ArtifactLocator {
	if cmd.k8sArtifactLocator == nil {
		cmd.k8sArtifactLocator = jetbridge.NewArtifactLocator()
	}
	return cmd.k8sArtifactLocator
}

func (cmd *RunCommand) constructPool(logger lager.Logger, dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.Pool, error) {
	factory, workerDB, err := cmd.workerFactory(logger, dbConn, lockFactory, workerCache)
	if err != nil {
		return worker.Pool{}, err
	}
	return worker.NewPool(factory, workerDB), nil
}

// workerFactory builds the worker factory a pool hands its workers, and the
// worker DB beside it. It is split from constructPool so the collaborators the
// factory carries (the artifact locator above all) can be checked against the
// ones the Reaper is given.
func (cmd *RunCommand) workerFactory(logger lager.Logger, dbConn db.DbConn, lockFactory lock.LockFactory, workerCache *db.WorkerCache) (worker.DefaultFactory, worker.DB, error) {
	dbResourceCacheFactory := db.NewResourceCacheFactory(dbConn, lockFactory)
	dbWorkerBaseResourceTypeFactory := db.NewWorkerBaseResourceTypeFactory(dbConn)
	dbTaskCacheFactory := db.NewTaskCacheFactory(dbConn)
	dbWorkerTaskCacheFactory := db.NewWorkerTaskCacheFactory(dbConn)
	dbVolumeRepository := db.NewVolumeRepository(dbConn)
	dbWorkerFactory := db.NewWorkerFactory(dbConn, workerCache)
	dbTeamFactory := db.NewTeamFactory(dbConn, lockFactory)
	runFactory := db.NewPipelineRunFactory(dbConn, lockFactory)
	cmd.runResultFinalizer = &runs.ResultFinalizer{Conn: dbConn, Factory: runFactory}
	stepPodBuilds := db.NewBuildFactory(dbConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)

	db := worker.NewDB(
		dbWorkerFactory,
		dbTeamFactory,
		dbVolumeRepository,
		dbTaskCacheFactory,
		dbWorkerTaskCacheFactory,
		dbResourceCacheFactory,
		dbWorkerBaseResourceTypeFactory,
		lockFactory,
	)

	factory := worker.DefaultFactory{
		DB:       db,
		Streamer: cmd.streamer(),
	}
	executionStarter := &runs.ExecutionStarter{Conn: dbConn, Factory: runFactory}
	factory.K8sExecutionPreparer = executionStarter

	if cmd.Kubernetes.Namespace != "" {
		rt, err := cmd.jetbridgeConfig()
		if err != nil {
			return worker.DefaultFactory{}, worker.DB{}, err
		}
		k8sCfg, k8sClientset, k8sRestConfig := rt.config, rt.clientset, rt.restConfig

		factory.K8sClientset = k8sClientset
		factory.K8sConfig = &k8sCfg
		factory.K8sExecutor = jetbridge.NewSPDYExecutor(k8sClientset, k8sRestConfig)
		factory.K8sArtifactLocator = cmd.artifactLocator()
		factory.K8sStepPodBuilds = stepPodBuilds
		if k8sCfg.OutputPlaneEnabled && cmd.hangarSigner != nil {
			// Both dispatch and recovery use the same node plane.
			factory.K8sOutputControls = jetbridge.NewOutputControls(k8sCfg,
				jetbridge.NewNodeIPResolver(k8sClientset),
				cmd.hangarSigner)
			source := jetbridge.NewOutputSource(k8sClientset, k8sCfg, cmd.hangarSigner)
			source.SetExecutor(factory.K8sExecutor)
			cmd.runCancellationSource = source
			if err := cmd.configureOutputReads(dbConn, source); err != nil {
				return worker.DefaultFactory{}, worker.DB{}, err
			}
			if err := cmd.configureRunInputUploads(dbConn, runFactory, dbTeamFactory, source); err != nil {
				return worker.DefaultFactory{}, worker.DB{}, err
			}
			executionStarter.Source = source
			cmd.runOutputStarter = runs.NewOutputStarter(dbConn, runFactory, source, cmd.Kubernetes.OutputCaptureDeadline)
			executionStarter.Output = cmd.runOutputStarter
			if cmd.readWarrantMinter != nil {
				executionStarter.SetInputReadMinter(cmd.readWarrantMinter)
			}
			cmd.runTaskStarter = executionStarter
			cmd.hangarOutputSource = source
		}

		if k8sCfg.ArtifactDaemonService != "" {
			// The Config picks the daemon's namespace, not this call site:
			// the daemon may live apart from the step-pod namespace.
			factory.K8sDaemonClient = jetbridge.NewDaemonClientFromConfig(logger.Session("daemon-client"), k8sClientset, k8sCfg)
		}
	}

	return factory, db, nil
}

func (cmd *RunCommand) gcComponents(
	logger lager.Logger,
	gcConn db.DbConn,
	lockFactory lock.LockFactory,
) ([]RunnableComponent, error) {
	dbWorkerLifecycle := db.NewWorkerLifecycle(gcConn)
	dbResourceCacheLifecycle := db.NewResourceCacheLifecycle(gcConn)
	dbTaskCacheLifecycle := db.NewTaskCacheLifecycle(gcConn)
	dbContainerRepository := db.NewContainerRepository(gcConn)
	dbArtifactLifecycle := db.NewArtifactLifecycle(gcConn)
	dbAccessTokenLifecycle := db.NewAccessTokenLifecycle(gcConn)
	resourceConfigCheckSessionLifecycle := db.NewResourceConfigCheckSessionLifecycle(gcConn)
	dbBuildFactory := db.NewBuildFactory(gcConn, lockFactory, cmd.GC.OneOffBuildGracePeriod, cmd.GC.FailedGracePeriod)
	dbResourceConfigFactory := db.NewResourceConfigFactory(gcConn, lockFactory)
	dbPipelineLifecycle := db.NewPipelineLifecycle(gcConn, lockFactory)
	dbPipelineRunReclaimLifecycle := db.NewPipelineRunReclaimLifecycle(gcConn)
	dbCheckLifecycle := db.NewCheckLifecycle(gcConn)

	dbVolumeRepository := db.NewVolumeRepository(gcConn)

	// set the 'unreferenced resource config' grace period to be the longer than
	// the check timeout, just to make sure it doesn't get removed out from under
	// a running check. The containers of in-memory checks use the same grace
	// period, so a check that is still running keeps its container.
	//
	// 5 minutes is arbitrary - this really shouldn't matter a whole lot, but
	// exposing a config specifically for it is a little risky, since you don't
	// want to set it too low.
	unreferencedConfigGracePeriod := cmd.GlobalResourceCheckTimeout + 5*time.Minute

	collectors := map[string]component.Runnable{
		atc.ComponentCollectorBuilds:            gc.NewBuildCollector(dbBuildFactory),
		atc.ComponentCollectorWorkers:           gc.NewWorkerCollector(dbWorkerLifecycle),
		atc.ComponentCollectorResourceConfigs:   gc.NewResourceConfigCollector(dbResourceConfigFactory, unreferencedConfigGracePeriod),
		atc.ComponentCollectorResourceCaches:    gc.NewResourceCacheCollector(dbResourceCacheLifecycle),
		atc.ComponentCollectorTaskCaches:        gc.NewTaskCacheCollector(dbTaskCacheLifecycle),
		atc.ComponentCollectorResourceCacheUses: gc.NewResourceCacheUseCollector(dbResourceCacheLifecycle),
		atc.ComponentCollectorArtifacts:         gc.NewArtifactCollector(dbArtifactLifecycle),
		atc.ComponentCollectorVolumes:           gc.NewVolumeCollector(dbVolumeRepository, cmd.GC.MissingGracePeriod),
		atc.ComponentCollectorContainers:        gc.NewContainerCollector(dbContainerRepository, cmd.GC.MissingGracePeriod, cmd.GC.HijackGracePeriod, unreferencedConfigGracePeriod),
		atc.ComponentCollectorCheckSessions:     gc.NewResourceConfigCheckSessionCollector(resourceConfigCheckSessionLifecycle),
		atc.ComponentCollectorPipelines:         gc.NewPipelineCollector(dbPipelineLifecycle),
		atc.ComponentCollectorAccessTokens:      gc.NewAccessTokensCollector(dbAccessTokenLifecycle, jwt.DefaultLeeway),
		atc.ComponentCollectorChecks:            gc.NewChecksCollector(dbCheckLifecycle),
		atc.ComponentCollectorDeprecatedScopes:  gc.NewDeprecatedScopeCollector(gcConn, cmd.GC.DeprecatedScopeGracePeriod),
	}
	var components []RunnableComponent
	for collectorName, collector := range collectors {
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: collectorName,
			},
			Runnable: collector,
		})
	}
	components = append(components, newPipelineRunReclaimerComponent(dbPipelineRunReclaimLifecycle, time.Now))

	return components, nil
}

// hangarOutputComponents is everything the output plane contributes to the
// component table, and the one place the plane's own flag decides whether any
// of it runs.
//
// THE CAPTURE ADVANCER ONCE RAN ON EVERY DEPLOYMENT. It was appended outside
// both the Kubernetes block and any output-plane check, so an upgraded
// deployment that never opted in -- a non-Kubernetes one included -- grew a
// `components` row, an advisory lock and a query a minute, for a plane it does
// not have. Each pass is one bounded indexed SELECT rolled back immediately,
// so the cost was small; a deployment with capture disabled must behave as it
// did before, and "small" is not that.
//
// The status component keeps its own additional condition, and it is a
// different question: the flag says this deployment HAS an output plane, and
// a configured bucket says it is configured enough to describe. A status
// surface reporting "0 live generations, not at risk" about a plane nobody
// has configured is an alert rule that will never fire looking exactly like
// coverage.
func (cmd *RunCommand) hangarOutputComponents(dbConn db.DbConn) []RunnableComponent {
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	components := []RunnableComponent{
		cmd.hangarOutputCaptureComponent(dbConn),
	}
	if status := cmd.hangarOutputStatusComponent(dbConn); status != nil {
		components = append(components, *status)
	}

	return components
}

// hangarOutputCoordinator is the capture coordinator: the capture rows in
// PostgreSQL and the node daemons the rows name.
func (cmd *RunCommand) hangarOutputCoordinator(dbConn db.DbConn) *hangaroutput.Coordinator {
	coordinator := &hangaroutput.Coordinator{
		Transactor: hangarOutputTransactor{conn: dbConn},
		Rows:       db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()),
	}
	// With no runtime source the coordinator's Dial stays nil, and it refuses
	// every node-side step with a typed error.
	if source := cmd.hangarOutputSource; source != nil {
		coordinator.Dial = func(ctx context.Context, node string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
			dial, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return source.CaptureControl(dial, node, uid)
		}
	}

	return coordinator
}

// hangarOutputCaptureComponent runs the capture sequence and its recovery:
// every pending capture through its seal and publish, every publishing one
// through recovery, every pending one past its deadline to failed, and every
// terminal one's marker released.
//
// It is a component and not a goroutine beside the step because the process
// that started a capture is exactly the process that may be gone: what has to
// survive is the row, and the ability to read it and take the next step. The
// component runner's lease makes one ATC own one pass; the CAS on the row
// makes a second one benign.
//
// A ten-second interval: a seal waits for its node's Pod to stop, and the
// pass is what asks again.
func (cmd *RunCommand) hangarOutputCaptureComponent(dbConn db.DbConn) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputCapture},
		Runnable:  cmd.hangarOutputCoordinator(dbConn),
		Interval:  10 * time.Second,
	}
}

// runComponents is what every web node registers for Runs, with or without an
// output plane.
//
// The cancellation worker is here and not among the output plane's components
// because the cancel route answers on every node: a fence accepted where
// the worker is absent would never converge. Its own operations (build
// abort, candidate settlement, terminalization) need no output plane. Without
// one the node admits no exact execution, so no capture is ever discarded.
//
// The web fleet must agree on the output plane. The worker's lease is one
// row that a live owner renews on every pass, so a node without the plane
// that holds it keeps it; an execution admitted by a node with the plane is
// then retried, its Run staying running, until the lease holder stops. The
// chart renders every web node alike.
func (cmd *RunCommand) runComponents(dbConn db.DbConn) []RunnableComponent {
	return []RunnableComponent{
		cmd.runResultsComponent(),
		cmd.runCancellationComponent(dbConn),
	}
}

// runResultsComponent makes each settled Run's one terminal publication. It
// is registered on every web node, with or without an output plane; a Run whose
// captures are still unsettled is simply left for a later pass. Build
// completion wakes it; the interval is the net under a lost wake-up.
func (cmd *RunCommand) runResultsComponent() RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunResults},
		Runnable: component.RunFunc(func(ctx context.Context) error {
			if cmd.runResultFinalizer == nil {
				return nil
			}
			return cmd.runResultFinalizer.Run(ctx)
		}),
		Interval: 10 * time.Second,
	}
}

// Cancellation has its own bounded pass and nonzero fallback. It shares the
// source coordinator and the Run's immutable identities with normal completion.
func (cmd *RunCommand) runCancellationComponent(dbConn db.DbConn) RunnableComponent {
	factory := db.NewPipelineRunFactory(dbConn, nil)
	if cmd.runResultFinalizer != nil {
		factory = cmd.runResultFinalizer.Factory
	}
	executions := &runs.CancellationExecutions{Conn: dbConn, Factory: factory, Source: cmd.runCancellationSource}
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunCancellation},
		Interval:  runs.CancellationPollInterval,
		Runnable: &runs.CancellationWorker{Conn: dbConn, Factory: factory, OwnerID: uuid.NewString(),
			Actions: runs.CancellationActionSet{factory, executions, runs.CancellationActionFunc(factory.ExecuteCancellationFinality)}},
	}
}

// hangarOutputTransactor adapts the connection to the coordinator's
// Transactor.
//
// It lives here rather than in atc/db because the interface belongs to the
// coordinator and atc/db has no business importing it: an interface is a
// statement of what a consumer uses, and the package that satisfies one should
// not have to know it exists.
type hangarOutputTransactor struct{ conn db.DbConn }

func (transactor hangarOutputTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	// The COMMIT answers in the output leaf's vocabulary. Two of this plane's
	// constraint triggers are DEFERRED, so their refusals arrive here and
	// nowhere earlier, and a coordinator handed an unclassified commit failure
	// would read a denial as an ambiguous commit and retry it forever.
	return db.HangarOutputTx{Tx: tx}, nil
}

func newPipelineRunReclaimerComponent(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentReclaimerPipelineRuns},
		Runnable:  gc.NewPipelineRunReclaimer(lifecycle, now),
		Interval:  time.Minute,
	}
}

func (cmd *RunCommand) secretManager(logger lager.Logger) (creds.Secrets, error) {
	var secretsFactory creds.SecretsFactory = noop.NewNoopFactory()
	for name, manager := range cmd.CredentialManagers {
		if !manager.IsConfigured() {
			continue
		}

		credsLogger := logger.Session("credential-manager", lager.Data{
			"name": name,
		})

		credsLogger.Info("configured credentials manager")

		err := manager.Init(credsLogger)
		if err != nil {
			return nil, err
		}

		err = manager.Validate()
		if err != nil {
			return nil, fmt.Errorf("credential manager '%s' misconfigured: %s", name, err)
		}

		secretsFactory, err = manager.NewSecretsFactory(credsLogger)
		if err != nil {
			return nil, err
		}

		break
	}

	return cmd.CredentialManagement.NewSecrets(secretsFactory), nil
}

// encryptionKey is the one key this web encrypts and decrypts database
// columns with. --encryption-key is a required flag, so the AEAD is set by
// the time Runner is called; validate() names the flag if it is not.
func (cmd *RunCommand) encryptionKey() *encryption.Key {
	return encryption.NewKey(cmd.EncryptionKey.AEAD)
}

func (cmd *RunCommand) constructWebHandler(logger lager.Logger) (http.Handler, error) {
	webHandler, err := web.NewHandler(logger, cmd.WebPublicDir.Path())
	if err != nil {
		return nil, err
	}
	return metric.WrapHandler(logger, metric.Metrics, "web", webHandler), nil
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

func run(runner ifrit.Runner, onReady func(), onExit func()) ifrit.Runner {
	return ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
		process := ifrit.Background(runner)

		subExited := process.Wait()
		subReady := process.Ready()

		for {
			select {
			case <-subReady:
				onReady()
				close(ready)
				subReady = nil
			case err := <-subExited:
				onExit()
				return err
			case sig := <-signals:
				process.Signal(sig)
			}
		}
	})
}

func (cmd *RunCommand) configureMetrics(logger lager.Logger) error {
	host := cmd.Metrics.HostName
	if host == "" {
		host, _ = os.Hostname()
	}

	return metric.Metrics.Initialize(logger.Session("metrics"), host, cmd.Metrics.Attributes, cmd.Metrics.BufferSize)
}

func (cmd *RunCommand) constructDBConn(
	driverName string,
	logger lager.Logger,
	maxConns int,
	idleConns int,
	connectionName string,
	lockFactory lock.LockFactory,
) (db.DbConn, error) {
	dbConn, err := db.Open(logger.Session("db"), driverName, cmd.Postgres.ConnectionString(), cmd.encryptionKey(), connectionName, lockFactory)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %s", err)
	}

	// Instrument with Metrics
	dbConn = metric.CountQueries(dbConn)
	metric.Metrics.Databases = append(metric.Metrics.Databases, dbConn)

	// Instrument with Logging
	if cmd.LogDBQueries {
		dbConn = db.Log(logger.Session("log-dbconn"), dbConn)
	}

	// Prepare
	dbConn.SetMaxOpenConns(maxConns)
	dbConn.SetMaxIdleConns(idleConns)

	return dbConn, nil
}

type Closer interface {
	Close() error
}

func constructLockConns(driverName, connectionString string) ([lock.FactoryCount]*sql.DB, error) {
	conns := [lock.FactoryCount]*sql.DB{}
	for i := range lock.FactoryCount {
		dbConn, err := sql.Open(driverName, connectionString)
		if err != nil {
			return conns, err
		}

		dbConn.SetMaxOpenConns(1)
		dbConn.SetMaxIdleConns(1)
		dbConn.SetConnMaxLifetime(0)

		conns[i] = dbConn
	}
	return conns, nil
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

func (cmd *RunCommand) constructEngine(
	workerPool worker.Pool,
	workerFactory db.WorkerFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	secretManager creds.Secrets,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	lockFactory lock.LockFactory,
	rateLimiter engine.RateLimiter,
	resolver imageresolver.Resolver,
	childRunAdmitter exec.ChildRunAdmitter,
) engine.Engine {
	coreOptions := []engine.CoreStepFactoryOption{
		engine.WithCoreImageResolver(resolver),
		engine.WithChildRunAdmitter(childRunAdmitter),
	}
	if cmd.runTaskStarter != nil {
		coreOptions = append(coreOptions, engine.WithCoreRunTaskPreparer(cmd.runTaskStarter))
	}
	return engine.NewEngine(
		engine.NewStepperFactory(
			engine.NewCoreStepFactory(
				workerPool,
				cmd.streamer(),
				lockFactory,
				teamFactory,
				buildFactory,
				resourceCacheFactory,
				resourceConfigFactory,
				defaultLimits,
				defaultRequests,
				cmd.GlobalResourceCheckTimeout,
				cmd.DefaultGetTimeout,
				cmd.DefaultPutTimeout,
				cmd.DefaultTaskTimeout,
				coreOptions...,
			),
			cmd.ExternalURL.String(),
			rateLimiter,
			workerFactory,
			lockFactory,
			resourceConfigFactory,
			resourceCacheFactory,
			resolver,
		),
		secretManager,
	)
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
		wrappa.NewAPIMetricsWrappa(logger),
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

type drainRunner struct {
	logger  lager.Logger
	drainer component.Drainable
}

func (runner drainRunner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	close(ready)
	<-signals
	runner.drainer.Drain(lagerctx.NewContext(context.Background(), runner.logger))
	return nil
}

type RunnableComponent struct {
	atc.Component
	component.Runnable
	Interval time.Duration
}

func (cmd *RunCommand) isMTLSEnabled() bool {
	return string(cmd.TLSCaCert) != ""
}

// loadArtifactResolveCapabilityKey reads the shared signing key once and caches
// it. Empty path means no signing, which the daemon accepts only if it too was
// started without a key.
func (cmd *RunCommand) loadArtifactResolveCapabilityKey() ([]byte, error) {
	if cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey == "" {
		return nil, nil
	}
	cmd.artifactResolveCapabilityMu.Lock()
	defer cmd.artifactResolveCapabilityMu.Unlock()
	if len(cmd.artifactResolveCapabilityKey) != 0 {
		return append([]byte(nil), cmd.artifactResolveCapabilityKey...), nil
	}
	key, err := artifactcap.LoadKeyFile(cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey)
	if err != nil {
		return nil, err
	}
	cmd.artifactResolveCapabilityKey = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}

// hangarOutputStatusComponent publishes the output plane's operational state:
// the in-service flag, the residue a drain waits on, and the open integrity
// findings.
//
// Nil when no output plane is configured, and that is the honest answer rather
// than a component emitting zeroes: a status surface reporting "0 residue, not
// at risk" about a plane that does not exist is an alert rule that will never
// fire looking exactly like coverage.
func (cmd *RunCommand) hangarOutputStatusComponent(dbConn db.DbConn) *RunnableComponent {
	// The same condition the admin status API answers on.
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	return &RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputStatus},
		Runnable: &hangaroutput.StatusPublisher{
			Reader: cmd.hangarOutputStatusReader(dbConn),
		},
		Interval: time.Minute,
	}
}
