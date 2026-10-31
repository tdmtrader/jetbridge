package metric

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Every Concourse metric is declared here, as a prometheus.*Opts literal: the
// chart's alert-drift test reads this file's AST to resolve the names its
// alerting rules use. Names, labels and help are a published interface --
// alerts and dashboards reference them -- and testdata/metric_families.txt
// pins what the /metrics page carries.

var (
	collectorBuckets = []float64{1, 60, 180, 300, 600, 900, 1200, 1800, 2700, 3600, 7200, 18000, 36000}

	errorLogs = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "error", Name: "logs", Help: "Number of error logged"}, []string{"message"})
	locksHeld = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "locks", Name: "held", Help: "Database locks held"}, []string{"type"})

	jobsScheduled          = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "jobs", Name: "scheduled_total", Help: "Total number of Concourse jobs scheduled."})
	jobsScheduling         = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "jobs", Name: "scheduling", Help: "Number of Concourse jobs currently being scheduled."})
	jobsSchedulingDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "jobs", Name: "schedulingDuration", Help: "Duration of jobs being scheduled in milliseconds", Buckets: []float64{30, 60, 180, 300, 600, 900, 1200, 1800, 2700, 3600, 7200, 18000, 36000}}, []string{"pipeline", "job", "job_id"})

	buildsStarted              = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "started_total", Help: "Total number of Concourse builds started."})
	buildsRunning              = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "builds", Name: "running", Help: "Number of Concourse builds currently running."})
	checkBuildsStarted         = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_started_total", Help: "Total number of Concourse check builds started."})
	checkBuildsRunning         = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_running", Help: "Number of Concourse check builds currently running."})
	latestCompletedBuildStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "builds", Name: "latest_completed_build_status", Help: "Status of Latest Completed Build. 0=Success, 1=Failed, 2=Aborted, 3=Errored."}, []string{"jobName", "pipelineName", "teamName"})
	buildsFinished             = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "finished_total", Help: "Total number of Concourse builds finished."})
	buildsSucceeded            = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "succeeded_total", Help: "Total number of Concourse builds succeeded."})
	buildsErrored              = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "errored_total", Help: "Total number of Concourse builds errored."})
	buildsFailed               = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "failed_total", Help: "Total number of Concourse builds failed."})
	buildsAborted              = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "aborted_total", Help: "Total number of Concourse builds aborted."})
	buildsFinishedVec          = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "finished", Help: "Count of builds finished across various dimensions."}, []string{"team", "pipeline", "job", "status"})
	buildDurationsVec          = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "builds", Name: "duration_seconds", Help: "Build time in seconds", Buckets: collectorBuckets}, []string{"team", "pipeline", "job"})

	// Nothing increments the check-build outcome counters; they publish zero.
	checkBuildsFinished  = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_finished_total", Help: "Total number of Concourse check builds finished."})
	checkBuildsSucceeded = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_succeeded_total", Help: "Total number of Concourse check builds succeeded."})
	checkBuildsErrored   = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_errored_total", Help: "Total number of Concourse check builds errored."})
	checkBuildsFailed    = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_failed_total", Help: "Total number of Concourse check builds failed."})
	checkBuildsAborted   = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "builds", Name: "check_aborted_total", Help: "Total number of Concourse check builds aborted."})

	stepsWaitingDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "steps", Name: "wait_duration", Help: "Elapsed time waiting for execution", Buckets: []float64{10, 30, 60, 120, 300, 600, 1800, 2400, 3000, 3600}}, []string{"platform", "teamId", "teamName", "type", "workerTags"})

	// Declared and registered, but nothing writes them, so no series appears.
	stepsWaiting            = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "steps", Name: "waiting", Help: "Number of Concourse build steps currently waiting."}, []string{"platform", "teamId", "teamName", "type", "workerTags"})
	workerContainers        = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "containers", Help: "Number of containers per worker"}, []string{"worker", "platform", "team", "tags"})
	workerUnknownContainers = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "unknown_containers", Help: "Number of unknown containers found on worker"}, []string{"worker"})
	workerVolumes           = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "volumes", Help: "Number of volumes per worker"}, []string{"worker", "platform", "team", "tags"})
	workerUnknownVolumes    = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "unknown_volumes", Help: "Number of unknown volumes found on worker"}, []string{"worker"})
	workerTasks             = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "tasks", Help: "Number of active tasks per worker"}, []string{"worker", "platform"})

	workersRegistered = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "workers", Name: "registered", Help: "Number of workers per state as seen by the database"}, []string{"state"})

	httpRequestsDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "http_responses", Name: "duration_seconds", Help: "Response time in seconds"}, []string{"method", "route", "status"})

	dbQueriesTotal = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "db", Name: "queries_total", Help: "Total number of database Concourse database queries"})
	dbConnections  = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "db", Name: "connections", Help: "Current number of concourse database connections"}, []string{"dbname"})

	checksFinished = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "lidar", Name: "checks_finished_total", Help: "Total number of checks finished."}, []string{"status"})
	checksStarted  = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "lidar", Name: "checks_started_total", Help: "Total number of checks started. With global resource enabled, a check build may not really run a check, thus total checks started should be less than total check builds started."})
	checksEnqueued = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "lidar", Name: "checks_enqueued_total", Help: "Total number of checks enqueued"})

	// Nothing increments it; it publishes zero.
	workerOrphanedVolumesToBeCollected = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "volumes", Name: "orphaned_volumes_to_be_deleted", Help: "Number of orphaned volumes to be garbage collected."})

	k8sPodStartupDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "k8s", Name: "pod_startup_duration_milliseconds", Help: "Time from pod creation to Running state in milliseconds.", Buckets: []float64{500, 1000, 2000, 5000, 10000, 30000, 60000, 120000}})
	k8sImagePullFailures  = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "k8s", Name: "image_pull_failures_total", Help: "Number of K8s image pull failures (ImagePullBackOff, ErrImagePull)."})

	creatingContainersToBeGarbageCollected   = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "creating_containers_to_be_garbage_collected", Help: "Creating Containers being garbage collected"})
	createdContainersToBeGarbageCollected    = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "created_containers_to_be_garbage_collected", Help: "Created Containers being garbage collected"})
	failedContainersToBeGarbageCollected     = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "failed_containers_to_be_garbage_collected", Help: "Failed Containers being garbage collected"})
	destroyingContainersToBeGarbageCollected = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "destroying_containers_to_be_garbage_collected", Help: "Destorying Containers being garbage collected"})
	createdVolumesToBeGarbageCollected       = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "created_volumes_to_be_garbage_collected", Help: "Created Volumes being garbage collected"})
	destroyingVolumesToBeGarbageCollected    = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "destroying_volumes_to_be_garbage_collected", Help: "Destroying Volumes being garbage collected"})
	failedVolumesToBeGarbageCollected        = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "gc", Name: "failed_volumes_to_be_garbage_collected", Help: "Failed Volumes being garbage collected"})

	gcBuildCollectorDuration                      = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_build_collector_duration", Help: "Duration of gc build collector (ms)", Buckets: collectorBuckets})
	gcWorkerCollectorDuration                     = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_worker_collector_duration", Help: "Duration of gc worker collector (ms)", Buckets: collectorBuckets})
	gcResourceCacheUseCollectorDuration           = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_resource_cache_use_collector_duration", Help: "Duration of gc resource cache use collector (ms)", Buckets: collectorBuckets})
	gcResourceConfigCollectorDuration             = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_resource_config_collector_duration", Help: "Duration of gc resource config collector (ms)", Buckets: collectorBuckets})
	gcDeprecatedScopeCollectorDuration            = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_deprecated_scope_collector_duration", Help: "Duration of gc deprecated scope collector (ms)", Buckets: collectorBuckets})
	gcResourceCacheCollectorDuration              = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_resource_cache_collector_duration", Help: "Duration of gc resource cache collector (ms)", Buckets: collectorBuckets})
	gcTaskCacheCollectorDuration                  = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_task_cache_collector_duration", Help: "Duration of gc task cache collector (ms)", Buckets: collectorBuckets})
	gcResourceConfigCheckSessionCollectorDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_resource_config_check_session_collector_duration", Help: "Duration of gc resource config check session collector (ms)", Buckets: collectorBuckets})
	gcArtifactCollectorDuration                   = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_artifact_collector_duration", Help: "Duration of gc artifact collector (ms)", Buckets: collectorBuckets})
	gcContainerCollectorDuration                  = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_container_collector_duration", Help: "Duration of gc container collector (ms)", Buckets: collectorBuckets})
	gcVolumeCollectorDuration                     = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "gc_volume_collector_duration", Help: "Duration of gc volume collector (ms)", Buckets: collectorBuckets})

	// The backlog is a level, not a rate: it is what is waiting right now, and
	// it can fall as well as rise. A counter could not express that.
	pipelineRunReclaimBacklog = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "gc", Name: "pipeline_run_reclaim_backlog", Help: "Pipeline runs currently eligible for reclamation"})

	// The reclaimer runs once a minute, so the buckets are tighter than the
	// shared collector buckets: the question is whether a pass fits inside its
	// own interval, which the coarse buckets could not answer.
	pipelineRunReclaimDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "concourse", Subsystem: "gc", Name: "pipeline_run_reclaim_duration", Help: "Duration of one pipeline run reclaimer pass (ms)", Buckets: []float64{1, 10, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}})

	// The Hangar output plane's operator surface. The plane must ALERT when it
	// goes fail-closed, and an alert is a rule over a series somebody is
	// already scraping -- a status page nobody has open at three in the
	// morning is the same as no status page.
	hangarOutputEnabled        = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "enabled", Help: "1 while the Hangar output plane is in service (hangar_enabled); admission refuses while it is 0"})
	hangarOutputAtRisk         = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "at_risk", Help: "1 while unresolved Hangar storage integrity findings block admission, with the classes that put it there"}, []string{"reasons"})
	hangarOutputViolations     = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "policy_violations", Help: "Open, unreconciled Hangar runtime storage integrity findings by class"}, []string{"violation"})
	hangarOutputPlaneInventory = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "plane_inventory", Help: "What the Hangar output plane is holding: live generations, captures by state, open claims, open integrity findings, and the residue a drain waits on"}, []string{"kind"})
	hangarOutputReclaimed      = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "reclaim_generations_total", Help: "Generations the web's reclaim pass reclaimed, deferred to a later pass, or failed to delete"}, []string{"outcome"})
	hangarOutputSweepObjects   = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "orphan_sweep_objects", Help: "Objects the last orphan sweep listed, by class: the orphans it deleted and every class it left alone"}, []string{"class"})
	hangarOutputSweepPasses    = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "orphan_sweep_passes_total", Help: "Completed orphan sweeps; a counter that stops moving is a sweep that stopped"})
	hangarOutputSweepFailures  = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "hangar_output", Name: "orphan_sweep_failures_total", Help: "Orphan sweeps that stopped on an error: a list, a judgement or a delete"})

	getStepCacheHits = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "concourse", Subsystem: "caches", Name: "get_step_cache_hits", Help: "Total number of get steps that hit caches"})
)

var collectors = []prometheus.Collector{
	errorLogs, locksHeld,
	jobsScheduled, jobsScheduling, jobsSchedulingDuration,
	buildsStarted, buildsRunning, checkBuildsStarted, checkBuildsRunning, latestCompletedBuildStatus,
	stepsWaiting, stepsWaitingDuration,
	buildsFinished, buildsSucceeded, buildsErrored, buildsFailed, buildsAborted, buildsFinishedVec, buildDurationsVec,
	checkBuildsFinished, checkBuildsSucceeded, checkBuildsErrored, checkBuildsFailed, checkBuildsAborted,
	workerContainers, workerUnknownContainers, workerVolumes, workerUnknownVolumes, workerTasks, workersRegistered,
	httpRequestsDuration, dbQueriesTotal, dbConnections,
	checksFinished, checksStarted, checksEnqueued,
	workerOrphanedVolumesToBeCollected, k8sPodStartupDuration, k8sImagePullFailures,
	creatingContainersToBeGarbageCollected, createdContainersToBeGarbageCollected, failedContainersToBeGarbageCollected,
	destroyingContainersToBeGarbageCollected, createdVolumesToBeGarbageCollected, destroyingVolumesToBeGarbageCollected,
	failedVolumesToBeGarbageCollected,
	gcBuildCollectorDuration, gcWorkerCollectorDuration, gcResourceCacheUseCollectorDuration,
	gcResourceConfigCollectorDuration, gcDeprecatedScopeCollectorDuration, gcResourceCacheCollectorDuration,
	gcTaskCacheCollectorDuration, gcResourceConfigCheckSessionCollectorDuration, gcArtifactCollectorDuration,
	gcContainerCollectorDuration, gcVolumeCollectorDuration, pipelineRunReclaimBacklog,
	hangarOutputEnabled, hangarOutputAtRisk, hangarOutputViolations, hangarOutputPlaneInventory,
	hangarOutputReclaimed, hangarOutputSweepObjects, hangarOutputSweepPasses, hangarOutputSweepFailures,
	pipelineRunReclaimDuration, getStepCacheHits,
}

// Register registers every Concourse collector with reg, each carrying
// attributes as constant labels. A collector may be registered with any
// number of registries.
func Register(reg prometheus.Registerer, attributes map[string]string) error {
	wrapped := prometheus.WrapRegistererWith(sanitizePrometheusLabels(attributes), reg)
	for _, collector := range collectors {
		if err := wrapped.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

type PrometheusConfig struct {
	BindIP   string `long:"prometheus-bind-ip" description:"IP to listen on to expose Prometheus metrics."`
	BindPort string `long:"prometheus-bind-port" description:"Port to listen on to expose Prometheus metrics."`
}

func (config PrometheusConfig) IsConfigured() bool {
	return config.BindPort != "" && config.BindIP != ""
}

// Serve registers the collectors with the default registry and serves it on
// /metrics, returning the address it listens on. It does nothing, and returns
// "", when no bind address is configured.
func (config PrometheusConfig) Serve(attributes map[string]string) (string, error) {
	if !config.IsConfigured() {
		return "", nil
	}

	if err := Register(prometheus.DefaultRegisterer, attributes); err != nil {
		return "", err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", config.BindIP, config.BindPort))
	if err != nil {
		return "", err
	}

	go http.Serve(listener, promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{EnableOpenMetrics: true},
	))

	return listener.Addr().String(), nil
}

// rePrometheusLabelInvalid matches any invalid characters we may have in our
// emitted labels.
//
// Prometheus format:
//
//	> Label names may contain ASCII letters, numbers, as well as underscores.
//	> They must match the regex [a-zA-Z_][a-zA-Z0-9_]*. Label names beginning
//	> with __ are reserved for internal use.
//	Link: https://prometheus.io/docs/concepts/data_model/#metric-names-and-labels
var rePrometheusLabelInvalid = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// rePrometheusLabelClean ensures we clean any duplicate underscores.
var rePrometheusLabelClean = regexp.MustCompile(`__+`)

// sanitizePrometheusLabels ensures the attribute names that become constant
// labels are valid Prometheus label names.
func sanitizePrometheusLabels(labels map[string]string) prometheus.Labels {
	newLabels := prometheus.Labels{}

	for k, v := range labels {
		k = rePrometheusLabelInvalid.ReplaceAllString(k, "_")
		k = strings.Trim(rePrometheusLabelClean.ReplaceAllString(k, "_"), "_")
		newLabels[k] = v
	}

	return newLabels
}
