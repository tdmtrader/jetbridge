// The backend's component table: backendComponents (the lidar scanner,
// pipeline pauser, scheduler, build tracker and build reaper, plus what the
// JetBridge runtime, the output plane and Runs contribute), gcComponents (the
// collectors and the pipeline-run reclaimer) and RunnableComponent, the row
// constructMembers turns into a component runner.

package atccmd

import (
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/pauser"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/atc/util"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/time/rate"
)

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
				cmd.varSourcePool,
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

func newPipelineRunReclaimerComponent(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentReclaimerPipelineRuns},
		Runnable:  gc.NewPipelineRunReclaimer(lifecycle, now),
		Interval:  time.Minute,
	}
}

type RunnableComponent struct {
	atc.Component
	component.Runnable
	Interval time.Duration
}
