package atc

const (
	ComponentScheduler                     = "scheduler"
	ComponentBuildTracker                  = "tracker"
	ComponentLidarScanner                  = "scanner"
	ComponentBuildReaper                   = "reaper"
	ComponentSyslogDrainer                 = "drainer"
	ComponentCollectorAccessTokens         = "collector_access_tokens"
	ComponentCollectorArtifacts            = "collector_artifacts"
	ComponentCollectorBuilds               = "collector_builds"
	ComponentCollectorCheckSessions        = "collector_check_sessions"
	ComponentCollectorChecks               = "collector_checks"
	ComponentCollectorContainers           = "collector_containers"
	ComponentCollectorResourceCacheUses    = "collector_resource_cache_uses"
	ComponentCollectorResourceCaches       = "collector_resource_caches"
	ComponentCollectorTaskCaches           = "collector_task_caches"
	ComponentCollectorResourceConfigs      = "collector_resource_configs"
	ComponentCollectorVolumes              = "collector_volumes"
	ComponentCollectorWorkers              = "collector_workers"
	ComponentCollectorPipelines            = "collector_pipelines"
	ComponentCollectorDeprecatedScopes     = "collector_deprecated_scopes"
	ComponentCollectorWorkflowRunTemplates = "collector_workflow_run_templates"
	ComponentK8sWorkerRegistrar            = "k8s_worker_registrar"
	ComponentK8sWorkerReaper               = "k8s_worker_reaper"
	ComponentAgentPlatformCredentialSyncer = "agent_platform_credential_syncer"
	ComponentAgentDispatcher               = "agent_dispatcher"
	ComponentAgentSnapshotGC               = "agent_snapshot_gc"
	ComponentAgentSnapshotRepair           = "agent_snapshot_repair"
	ComponentAgentSnapshotOrphanSweep      = "agent_snapshot_orphan_sweep"
	ComponentAgentSnapshotProjection       = "agent_snapshot_projection"
	ComponentAgentResourceCaptureFinalizer = "agent_resource_capture_finalizer"
	ComponentAgentWorkflowRunReconciler    = "agent_workflow_run_reconciler"
	ComponentAgentChildExecutionReconciler = "agent_child_execution_reconciler"
	ComponentAgentExperimentRunner         = "agent_experiment_runner"
	ComponentAgentExperimentEvaluator      = "agent_experiment_evaluator"
	ComponentAgentExperimentCancellation   = "agent_experiment_cancellation_reconciler"
	ComponentPipelinePauser                = "pipeline_pauser"
	ComponentSigningKeyLifecycler          = "signing_key_lifecycler"
	ComponentPipelineRunLifecycler         = "pipeline_run_lifecycler"
)

const (
	ComponentAgentWorkflowResourceSourceLifecycle       = "agent_workflow_resource_source_lifecycle"
	ComponentAgentWorkflowResourceSourceBuildReconciler = "agent_workflow_resource_source_build_reconciler"
)

type Component struct {
	Name string
}
