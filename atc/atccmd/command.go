// The web composition root: RunCommand.Execute and Runner configure the
// metrics, open the connections, publish the check intervals and build the
// secret manager, and constructMembers turns the API members and the
// component table into the ifrit group the web node runs. The engine the
// backend components share is built here too. The flags live in run_flags.go
// and the wiring Runner calls into lives in the wiring_*.go files beside it.

package atccmd

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"code.cloudfoundry.org/clock"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/skymarshal/storage"
	gocache "github.com/patrickmn/go-cache"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"
	"go.yaml.in/yaml/v3"

	// dynamically registered policy checkers

	// dynamically registered credential managers
	_ "github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/idtoken"
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
		logger.RegisterSink(metric.ErrorSink{})
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

	if err := cmd.configureMetrics(); err != nil {
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

	issuer := cmd.ExternalURL.String()
	if cmd.OIDCIssuerURL.String() != "" {
		issuer = cmd.OIDCIssuerURL.String()
	}

	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetIssuer(issuer)
	})

	secretManager, err := cmd.secretManager(logger)
	if err != nil {
		return nil, err
	}

	cmd.varSourcePool = creds.NewVarSourcePool(
		logger.Session("var-source-pool"),
		cmd.CredentialManagement,
		cmd.GC.VarSourceRecyclePeriod,
		1*time.Minute,
		clock.NewClock(),
	)

	members, err := cmd.constructMembers(logger, apiConn, workerConn, backendConn, gcConn, storage, lockFactory, secretManager)
	if err != nil {
		return nil, err
	}

	members = append(members, grouper.Member{
		Name:   "periodic-metrics",
		Runner: metric.PeriodicallyEmit(10 * time.Second),
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
		cmd.varSourcePool.Close()
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

func (cmd *RunCommand) configureMetrics() error {
	_, err := cmd.Metrics.Prometheus.Serve(cmd.Metrics.Attributes)
	return err
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
		cmd.varSourcePool,
	)
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
