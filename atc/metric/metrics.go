package metric

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/db/lock"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

type JobStatusLabels struct {
	JobName      string
	TeamName     string
	PipelineName string
}

type StepsWaitingLabels struct {
	TeamId   string
	TeamName string
	Type     string
}

type StepsWaitingDuration struct {
	Labels   StepsWaitingLabels
	Duration time.Duration
}

func (event StepsWaitingDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("steps-waiting-duration"),
		Event{
			Name:  "steps waiting duration",
			Value: event.Duration.Seconds(),
			Attributes: map[string]string{
				"teamId":   event.Labels.TeamId,
				"teamName": event.Labels.TeamName,
				"type":     event.Labels.Type,
			},
		},
	)

	RecordStepsWaitDuration(context.Background(), event.Duration.Seconds(), event.Labels.TeamName, event.Labels.Type)
}

type BuildCollectorDuration struct {
	Duration time.Duration
}

func (event BuildCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-build-collector-duration"),
		Event{
			Name:  "gc: build collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "build", ms(event.Duration))
}

type WorkerCollectorDuration struct {
	Duration time.Duration
}

func (event WorkerCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-worker-collector-duration"),
		Event{
			Name:  "gc: worker collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "worker", ms(event.Duration))
}

type ResourceCacheUseCollectorDuration struct {
	Duration time.Duration
}

func (event ResourceCacheUseCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-resource-cache-use-collector-duration"),
		Event{
			Name:  "gc: resource cache use collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "resource-cache-use", ms(event.Duration))
}

type ResourceConfigCollectorDuration struct {
	Duration time.Duration
}

func (event ResourceConfigCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-resource-config-collector-duration"),
		Event{
			Name:  "gc: resource config collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "resource-config", ms(event.Duration))
}

type DeprecatedScopeCollectorDuration struct {
	Duration time.Duration
}

func (event DeprecatedScopeCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-deprecated-scope-collector-duration"),
		Event{
			Name:  "gc: deprecated scope collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "deprecated-scope", ms(event.Duration))
}

type ResourceCacheCollectorDuration struct {
	Duration time.Duration
}

func (event ResourceCacheCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-resource-cache-collector-duration"),
		Event{
			Name:  "gc: resource cache collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "resource-cache", ms(event.Duration))
}

type TaskCacheCollectorDuration struct {
	Duration time.Duration
}

func (event TaskCacheCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-task-cache-collector-duration"),
		Event{
			Name:  "gc: task cache collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "task-cache", ms(event.Duration))
}

type ResourceConfigCheckSessionCollectorDuration struct {
	Duration time.Duration
}

func (event ResourceConfigCheckSessionCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-resource-config-check-session-collector-duration"),
		Event{
			Name:  "gc: resource config check session collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "resource-config-check-session", ms(event.Duration))
}

type ArtifactCollectorDuration struct {
	Duration time.Duration
}

func (event ArtifactCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-artifact-collector-duration"),
		Event{
			Name:  "gc: artifact collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "artifact", ms(event.Duration))
}

type ContainerCollectorDuration struct {
	Duration time.Duration
}

func (event ContainerCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-container-collector-duration"),
		Event{
			Name:  "gc: container collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "container", ms(event.Duration))
}

type VolumeCollectorDuration struct {
	Duration time.Duration
}

func (event VolumeCollectorDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-volume-collector-duration"),
		Event{
			Name:  "gc: volume collector duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "volume", ms(event.Duration))
}

// PipelineRunReclaimBacklog is the number of pipeline runs eligible for
// reclamation at the start of a reclaimer pass, counted without the batch
// bound. Read it against the batch size: a backlog that stays above the batch
// pass after pass is a reclaimer that will never catch up, and every run in it
// is a payload pipeline, its jobs and its build rows still on disk.
type PipelineRunReclaimBacklog struct {
	Runs int
}

func (event PipelineRunReclaimBacklog) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("pipeline-run-reclaim-backlog"),
		Event{
			Name:  "pipeline run reclaim backlog",
			Value: float64(event.Runs),
		},
	)
}

// PipelineRunReclaimDuration is how long one reclaimer pass took, batch
// included. It is per-batch rather than per-run so that it stays comparable
// with the component's own interval: a pass that outlasts the interval is the
// other half of the backlog story.
type PipelineRunReclaimDuration struct {
	Duration time.Duration
}

func (event PipelineRunReclaimDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("pipeline-run-reclaim-duration"),
		Event{
			Name:  "gc: pipeline run reclaim duration (ms)",
			Value: ms(event.Duration),
		},
	)
	RecordGCCollectorDuration(context.Background(), "pipeline-run-reclaim", ms(event.Duration))
}

type SchedulingJobDuration struct {
	PipelineName string
	JobName      string
	JobID        int
	Duration     time.Duration
}

func (event SchedulingJobDuration) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("job-scheduling-duration"),
		Event{
			Name:  "scheduling: job duration (ms)",
			Value: ms(event.Duration),
			Attributes: map[string]string{
				"pipeline": event.PipelineName,
				"job":      event.JobName,
				"job_id":   strconv.Itoa(event.JobID),
			},
		},
	)

	RecordSchedulingJobDuration(context.Background(), event.Duration.Seconds(), event.PipelineName, event.JobName)
}

type WorkerContainers struct {
	WorkerName string
	Platform   string
	Containers int
	TeamName   string
	Tags       []string
}

func (event WorkerContainers) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("worker-containers"),
		Event{
			Name:  "worker containers",
			Value: float64(event.Containers),
			Attributes: map[string]string{
				"worker":    event.WorkerName,
				"platform":  event.Platform,
				"team_name": event.TeamName,
				"tags":      strings.Join(event.Tags[:], "/"),
			},
		},
	)
}

type WorkerVolumes struct {
	WorkerName string
	Platform   string
	Volumes    int
	TeamName   string
	Tags       []string
}

func (event WorkerVolumes) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("worker-volumes"),
		Event{
			Name:  "worker volumes",
			Value: float64(event.Volumes),
			Attributes: map[string]string{
				"worker":    event.WorkerName,
				"platform":  event.Platform,
				"team_name": event.TeamName,
				"tags":      strings.Join(event.Tags[:], "/"),
			},
		},
	)
}

type WorkerTasks struct {
	WorkerName string
	Platform   string
	Tasks      int
}

func (event WorkerTasks) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("worker-tasks"),
		Event{
			Name:  "worker tasks",
			Value: float64(event.Tasks),
			Attributes: map[string]string{
				"worker":   event.WorkerName,
				"platform": event.Platform,
			},
		},
	)
}

type VolumesToBeGarbageCollected struct {
	Volumes int
}

type CreatingContainersToBeGarbageCollected struct {
	Containers int
}

func (event CreatingContainersToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-creating-containers-for-deletion"),
		Event{
			Name:       "creating containers to be garbage collected",
			Value:      float64(event.Containers),
			Attributes: map[string]string{},
		},
	)
}

type CreatedContainersToBeGarbageCollected struct {
	Containers int
}

func (event CreatedContainersToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-created-containers-for-deletion"),
		Event{
			Name:       "created containers to be garbage collected",
			Value:      float64(event.Containers),
			Attributes: map[string]string{},
		},
	)
}

type DestroyingContainersToBeGarbageCollected struct {
	Containers int
}

func (event DestroyingContainersToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-destroying-containers-for-deletion"),
		Event{
			Name:       "destroying containers to be garbage collected",
			Value:      float64(event.Containers),
			Attributes: map[string]string{},
		},
	)
}

type FailedContainersToBeGarbageCollected struct {
	Containers int
}

func (event FailedContainersToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-failed-containers-for-deletion"),
		Event{
			Name:       "failed containers to be garbage collected",
			Value:      float64(event.Containers),
			Attributes: map[string]string{},
		},
	)
}

type CreatedVolumesToBeGarbageCollected struct {
	Volumes int
}

func (event CreatedVolumesToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-created-volumes-for-deletion"),
		Event{
			Name:       "created volumes to be garbage collected",
			Value:      float64(event.Volumes),
			Attributes: map[string]string{},
		},
	)
}

type DestroyingVolumesToBeGarbageCollected struct {
	Volumes int
}

func (event DestroyingVolumesToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-destroying-volumes-for-deletion"),
		Event{
			Name:       "destroying volumes to be garbage collected",
			Value:      float64(event.Volumes),
			Attributes: map[string]string{},
		},
	)
}

type FailedVolumesToBeGarbageCollected struct {
	Volumes int
}

func (event FailedVolumesToBeGarbageCollected) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("gc-found-failed-volumes-for-deletion"),
		Event{
			Name:       "failed volumes to be garbage collected",
			Value:      float64(event.Volumes),
			Attributes: map[string]string{},
		},
	)
}

type JobStatus struct {
	Status       string
	JobName      string
	PipelineName string
	TeamName     string
}

func (event JobStatus) Emit(logger lager.Logger) {
	var value int
	switch event.Status {
	case "succeeded":
		value = 0
	case "failed":
		value = 1
	case "aborted":
		value = 2
	case "errored":
		value = 3
	default:
		return
	}

	Metrics.emit(
		logger.Session("latest-completed-build-status"),
		Event{
			Name:  "latest completed build status",
			Value: float64(value),
			Attributes: map[string]string{
				"jobName":      event.JobName,
				"pipelineName": event.PipelineName,
				"teamName":     event.TeamName,
			},
		},
	)
}

type BuildStarted struct {
	Build db.Build
}

func (event BuildStarted) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("build-started"),
		Event{
			Name:       "build started",
			Value:      float64(event.Build.ID()),
			Attributes: event.Build.TracingAttrs(),
		},
	)
}

type BuildFinished struct {
	Build db.Build
}

func (event BuildFinished) Emit(logger lager.Logger) {
	attrs := event.Build.TracingAttrs()
	attrs["build_status"] = event.Build.Status().String()

	duration := event.Build.EndTime().Sub(event.Build.StartTime())

	var traceID string
	if sc := event.Build.SpanContext(); sc != nil {
		traceID = extractTraceID(sc.Get("traceparent"))
	}

	Metrics.emit(
		logger.Session("build-finished"),
		Event{
			Name:       "build finished",
			Value:      ms(duration),
			Attributes: attrs,
			TraceID:    traceID,
		},
	)

	RecordBuildDuration(
		context.Background(),
		duration,
		attrs["team_name"],
		attrs["pipeline"],
		attrs["job"],
		event.Build.Status().String(),
	)

	RecordBuildFinished(context.Background(), event.Build.Status().String())
}

// extractTraceID parses the trace ID from a W3C traceparent header.
// Format: 00-<traceID>-<spanID>-<flags>
func extractTraceID(traceparent string) string {
	parts := strings.Split(traceparent, "-")
	if len(parts) >= 3 {
		return parts[1]
	}
	return ""
}

func ms(duration time.Duration) float64 {
	return float64(duration) / 1000000
}

type ErrorLog struct {
	Message string
	Value   int
}

func (e ErrorLog) Emit(logger lager.Logger, m *Monitor) {
	m.emit(
		logger.Session("error-log"),
		Event{
			Name:  "error log",
			Value: float64(e.Value),
			Attributes: map[string]string{
				"message": e.Message,
			},
		},
	)
}

type HTTPResponseTime struct {
	Route      string
	Path       string
	Method     string
	StatusCode int
	Duration   time.Duration
	TraceID    string
}

func (event HTTPResponseTime) Emit(logger lager.Logger, m *Monitor) {
	m.emit(
		logger.Session("http-response-time"),
		Event{
			Name:  "http response time",
			Value: ms(event.Duration),
			Attributes: map[string]string{
				"route":  event.Route,
				"path":   event.Path,
				"method": event.Method,
				"status": strconv.Itoa(event.StatusCode),
			},
			TraceID: event.TraceID,
		},
	)

	RecordHTTPResponseTime(context.Background(), event.Duration, event.Method, event.Route, event.StatusCode)
}

var lockTypeNames = map[int]string{
	lock.LockTypeResourceConfigChecking:     "ResourceConfigChecking",
	lock.LockTypeBuildTracking:              "BuildTracking",
	lock.LockTypeJobScheduling:              "JobScheduling",
	lock.LockTypeBatch:                      "Batch",
	lock.LockTypeVolumeCreating:             "VolumeCreating",
	lock.LockTypeContainerCreating:          "ContainerCreating",
	lock.LockTypeDatabaseMigration:          "DatabaseMigration",
	lock.LockTypeResourceScanning:           "ResourceScanning",
	lock.LockTypeInMemoryCheckBuildTracking: "InMemoryCheckBuildTracking",
	lock.LockTypeResourceGet:                "ResourceGet",
	lock.LockTypeVolumeStreaming:            "VolumeStreaming",
}

type LockAcquired struct {
	LockType string
}

func (event LockAcquired) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("lock-acquired"),
		Event{
			Name:  "lock held",
			Value: 1,
			Attributes: map[string]string{
				"type": event.LockType,
			},
		},
	)
}

type LockReleased struct {
	LockType string
}

func (event LockReleased) Emit(logger lager.Logger) {
	Metrics.emit(
		logger.Session("lock-released"),
		Event{
			Name:  "lock held",
			Value: 0,
			Attributes: map[string]string{
				"type": event.LockType,
			},
		},
	)
}

func LogLockAcquired(logger lager.Logger, lockID lock.LockID) {
	logger.Debug("acquired")

	if len(lockID) == 0 {
		return
	}

	if lockType, ok := lockTypeNames[lockID[0]]; ok {
		LockAcquired{LockType: lockType}.Emit(logger)
	}
}

func LogLockReleased(logger lager.Logger, lockID lock.LockID) {
	logger.Debug("released")

	if len(lockID) == 0 {
		return
	}

	if lockType, ok := lockTypeNames[lockID[0]]; ok {
		LockReleased{LockType: lockType}.Emit(logger)
	}
}

type WorkersState struct {
	WorkerStateByName map[string]db.WorkerState
}

func (event WorkersState) Emit(logger lager.Logger, m *Monitor) {
	for _, state := range db.AllWorkerStates() {
		count := 0
		for _, workerState := range event.WorkerStateByName {
			if workerState == state {
				count += 1
			}
		}

		m.emit(
			logger.Session("worker-state"),
			Event{
				Name:  "worker state",
				Value: float64(count),
				Attributes: map[string]string{
					"state": string(state),
				},
			},
		)
	}
}

// HangarOutputSnapshot is one read of the Hangar output plane's operational
// state, flattened for emission.
//
// Flat and closed-vocabulary: every map below carries EVERY member of its set
// on every emission, including the zeroes. A gauge that appears only when it is
// nonzero is one an alert cannot tell from a scrape that did not happen.
type HangarOutputSnapshot struct {
	// Enabled is the in-service flag admission takes FOR SHARE.
	Enabled bool

	// AtRisk is true while an open integrity finding blocks admission, and
	// Reasons is the comma-joined set of classes that put it there. The reasons
	// are an ATTRIBUTE and not a series, so the gauge's cardinality is one.
	AtRisk  bool
	Reasons string

	Violations map[string]int

	LiveGenerations        int
	NonterminalCaptures    int
	PendingCaptures        int
	PublishingCaptures     int
	UnreleasedCaptures     int
	UnacknowledgedReleases int
	OpenClaims             int
	OpenReadLeases         int
	UnfinalizedReclaimJobs int
	OpenIntegrityFindings  int

	// Residue is what a drain waits on; zero with Enabled false is drained.
	Residue int
}

// HangarOutputStatus is the event one status pass emits.
type HangarOutputStatus struct {
	Status HangarOutputSnapshot
}

func (event HangarOutputStatus) Emit(logger lager.Logger) {
	session := logger.Session("hangar-output-status")

	Metrics.emit(session, Event{
		Name:  "hangar output enabled",
		Value: boolValue(event.Status.Enabled),
	})
	Metrics.emit(session, Event{
		Name:  "hangar output at risk",
		Value: boolValue(event.Status.AtRisk),
		Attributes: map[string]string{
			"reasons": event.Status.Reasons,
		},
	})
	for violation, count := range event.Status.Violations {
		Metrics.emit(session, Event{
			Name:       "hangar output policy violations",
			Value:      float64(count),
			Attributes: map[string]string{"violation": violation},
		})
	}
	for name, value := range map[string]int{
		"live_generations":         event.Status.LiveGenerations,
		"nonterminal_captures":     event.Status.NonterminalCaptures,
		"pending_captures":         event.Status.PendingCaptures,
		"publishing_captures":      event.Status.PublishingCaptures,
		"unreleased_captures":      event.Status.UnreleasedCaptures,
		"unacknowledged_releases":  event.Status.UnacknowledgedReleases,
		"open_claims":              event.Status.OpenClaims,
		"open_read_leases":         event.Status.OpenReadLeases,
		"unfinalized_reclaim_jobs": event.Status.UnfinalizedReclaimJobs,
		"open_integrity_findings":  event.Status.OpenIntegrityFindings,
		"residue":                  event.Status.Residue,
	} {
		Metrics.emit(session, Event{
			Name:       "hangar output plane inventory",
			Value:      float64(value),
			Attributes: map[string]string{"kind": name},
		})
	}
}

// HangarOutputReclaimPass is what one web reclaim pass did: generations it
// admitted, jobs it finalized, and jobs it left open for the next pass.
type HangarOutputReclaimPass struct {
	Admitted  int
	Finalized int
	Open      int
}

func (event HangarOutputReclaimPass) Emit(logger lager.Logger) {
	session := logger.Session("hangar-output-reclaim")

	for outcome, value := range map[string]int{
		"admitted":  event.Admitted,
		"finalized": event.Finalized,
		"open":      event.Open,
	} {
		Metrics.emit(session, Event{
			Name:       "hangar output reclaim jobs",
			Value:      float64(value),
			Attributes: map[string]string{"outcome": outcome},
		})
	}
}

// HangarOutputOrphanSweep is one orphan sweep's count of listed objects per
// class: the one it deleted and every class it left alone.
type HangarOutputOrphanSweep struct {
	Objects map[string]int

	// Failed is a pass that stopped on an error -- a list, a judgement or a
	// delete. It counts as a failure and not as a completed pass.
	Failed bool

	// Skipped is a pass that judged nothing because the shared deletion lock
	// was held elsewhere. It is neither a completed pass nor a failure.
	Skipped bool
}

func (event HangarOutputOrphanSweep) Emit(logger lager.Logger) {
	session := logger.Session("hangar-output-orphan-sweep")

	for class, value := range event.Objects {
		Metrics.emit(session, Event{
			Name:       "hangar output orphan sweep objects",
			Value:      float64(value),
			Attributes: map[string]string{"class": class},
		})
	}
	if event.Skipped {
		return
	}
	name := "hangar output orphan sweep passes"
	if event.Failed {
		name = "hangar output orphan sweep failures"
	}
	Metrics.emit(session, Event{Name: name, Value: 1})
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}

	return 0
}

func boolAttribute(value bool) string {
	if value {
		return "true"
	}

	return "false"
}
