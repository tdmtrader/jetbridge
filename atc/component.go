package atc

const (
	ComponentScheduler                  = "scheduler"
	ComponentBuildTracker               = "tracker"
	ComponentLidarScanner               = "scanner"
	ComponentBuildReaper                = "reaper"
	ComponentSyslogDrainer              = "drainer"
	ComponentCollectorAccessTokens      = "collector_access_tokens"
	ComponentCollectorArtifacts         = "collector_artifacts"
	ComponentCollectorBuilds            = "collector_builds"
	ComponentCollectorCheckSessions     = "collector_check_sessions"
	ComponentCollectorChecks            = "collector_checks"
	ComponentCollectorContainers        = "collector_containers"
	ComponentCollectorResourceCacheUses = "collector_resource_cache_uses"
	ComponentCollectorResourceCaches    = "collector_resource_caches"
	ComponentCollectorTaskCaches        = "collector_task_caches"
	ComponentCollectorResourceConfigs   = "collector_resource_configs"
	ComponentCollectorVolumes           = "collector_volumes"
	ComponentCollectorWorkers           = "collector_workers"
	ComponentCollectorPipelines         = "collector_pipelines"
	ComponentReclaimerPipelineRuns      = "reclaimer_pipeline_runs"
	ComponentRunCancellation            = "run_cancellation"
	ComponentCollectorDeprecatedScopes  = "collector_deprecated_scopes"
	ComponentK8sWorkerRegistrar         = "k8s_worker_registrar"
	ComponentK8sWorkerReaper            = "k8s_worker_reaper"
	ComponentPipelinePauser             = "pipeline_pauser"
	ComponentSigningKeyLifecycler       = "signing_key_lifecycler"

	// ComponentHangarOutputCapture advances durable output captures.
	//
	// It is a component rather than a goroutine beside the step because the
	// process that started a capture is exactly the process that may be gone:
	// a capture crosses two systems, and what has to survive an ATC restart is
	// the ability to ask what is durably true and take the next bounded step.
	// The DB lease makes one ATC own one capture at a time; the fence makes a
	// takeover safe.
	ComponentHangarOutputCapture = "hangar_output_capture"

	// ComponentRunResults completes pipeline runs. Every run is a v2 run, and a
	// v2 run reaches a terminal status only through its one terminal
	// publication, which this component makes once the run's builds and
	// captures have settled. It runs whether or not a Hangar output plane is
	// configured: a run that declares no result has no capture to wait for.
	// (Executing a run's steps is another matter: every step needs the output
	// plane's execution control; see runs.ExecutionStarter.)
	ComponentRunResults = "run_results"

	// ComponentHangarOutputStatus publishes the output plane's operational
	// state as metrics on the existing scrape path: the in-service flag, the
	// open integrity findings, captures by state, and the residue a drain
	// waits on. `fly hangar-status` reads the same query on demand.
	//
	// It is a component and not an API endpoint, deliberately: an alert is a
	// rule over a series somebody is already scraping, and a status page nobody
	// has open at three in the morning is the same as no status page.
	ComponentHangarOutputStatus = "hangar_output_status"

	// ComponentHangarReclaim is the web's reclaim pass over the output
	// namespace: each registered generation nothing holds is deleted by its
	// exact generation and stamped reclaimed. It shares one advisory lock with
	// the orphan sweep, so across every web replica one of them deletes at a
	// time.
	ComponentHangarReclaim = "hangar_reclaim"

	// ComponentHangarOrphanSweep lists the output namespace and deletes only
	// objects whose marker names this store, that have no lifecycle row and
	// nothing about to give them one, and that are older than twice the
	// capture deadline -- each by its exact listed generation. Everything else
	// it counts and logs.
	ComponentHangarOrphanSweep = "hangar_orphan_sweep"
)

type Component struct {
	Name string
}
