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

	// ComponentHangarOutputReadLeaseCleanup closes read leases whose readers
	// are gone.
	//
	// A read lease is the READER's protection and it outlives the claim, so
	// that releasing the last claim during a transfer cannot delete the bytes
	// out from under a materializing task. The cost of that is a lease nobody
	// closes if the materializer dies mid-transfer: the generation stays
	// protected against reclaim for the life of the deployment, because an
	// active lease refuses reclaim admission.
	//
	// Expiry alone is what bounds it, and expiry is measured on the DATABASE
	// clock rather than the reader's -- but something still has to notice. This
	// is that something, and until Phase 7 it did not exist: the repository
	// method was written, specified and unreachable.
	ComponentHangarOutputReadLeaseCleanup = "hangar_output_read_lease_cleanup"
)

type Component struct {
	Name string
}
