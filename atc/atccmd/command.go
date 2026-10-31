package atccmd

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/storage"
	gocache "github.com/patrickmn/go-cache"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"
	"go.yaml.in/yaml/v3"

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

func (cmd *RunCommand) configureMetrics(logger lager.Logger) error {
	host := cmd.Metrics.HostName
	if host == "" {
		host, _ = os.Hostname()
	}

	return metric.Metrics.Initialize(logger.Session("metrics"), host, cmd.Metrics.Attributes, cmd.Metrics.BufferSize)
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
