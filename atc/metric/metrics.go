package metric

import (
	"context"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/prometheus/client_golang/prometheus"
)

func ms(duration time.Duration) float64 {
	return float64(duration) / 1000000
}

// StepWaited records how long a step waited for a worker.
func StepWaited(teamID, teamName, stepType string, waited time.Duration) {
	stepsWaitingDuration.WithLabelValues("", teamID, teamName, stepType, "").Observe(waited.Seconds())
	RecordStepsWaitDuration(context.Background(), waited.Seconds(), teamName, stepType)
}

// JobSchedulingDuration records one scheduling pass over a job.
func JobSchedulingDuration(pipeline, job string, jobID int, d time.Duration) {
	jobsSchedulingDuration.WithLabelValues(pipeline, job, strconv.Itoa(jobID)).Observe(ms(d))
	RecordSchedulingJobDuration(context.Background(), d.Seconds(), pipeline, job)
}

// The garbage collectors' pass durations.
func GCBuildCollectorDuration(d time.Duration) {
	gcBuildCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "build", ms(d))
}
func GCWorkerCollectorDuration(d time.Duration) {
	gcWorkerCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "worker", ms(d))
}
func GCResourceCacheUseCollectorDuration(d time.Duration) {
	gcResourceCacheUseCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "resource-cache-use", ms(d))
}
func GCResourceConfigCollectorDuration(d time.Duration) {
	gcResourceConfigCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "resource-config", ms(d))
}
func GCDeprecatedScopeCollectorDuration(d time.Duration) {
	gcDeprecatedScopeCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "deprecated-scope", ms(d))
}
func GCResourceCacheCollectorDuration(d time.Duration) {
	gcResourceCacheCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "resource-cache", ms(d))
}
func GCTaskCacheCollectorDuration(d time.Duration) {
	gcTaskCacheCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "task-cache", ms(d))
}
func GCResourceConfigCheckSessionCollectorDuration(d time.Duration) {
	gcResourceConfigCheckSessionCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "resource-config-check-session", ms(d))
}
func GCArtifactCollectorDuration(d time.Duration) {
	gcArtifactCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "artifact", ms(d))
}
func GCContainerCollectorDuration(d time.Duration) {
	gcContainerCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "container", ms(d))
}
func GCVolumeCollectorDuration(d time.Duration) {
	gcVolumeCollectorDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "volume", ms(d))
}

// PipelineRunReclaimBacklog is the number of pipeline runs eligible for
// reclamation at the start of a reclaimer pass, counted without the batch
// bound. Read it against the batch size: a backlog that stays above the batch
// pass after pass is a reclaimer that will never catch up, and every run in it
// is a payload pipeline, its jobs and its build rows still on disk.
func PipelineRunReclaimBacklog(runs int) { pipelineRunReclaimBacklog.Set(float64(runs)) }

// PipelineRunReclaimDuration is how long one reclaimer pass took, batch
// included. It is per-batch rather than per-run so that it stays comparable
// with the component's own interval: a pass that outlasts the interval is the
// other half of the backlog story.
func PipelineRunReclaimDuration(d time.Duration) {
	pipelineRunReclaimDuration.Observe(ms(d))
	RecordGCCollectorDuration(context.Background(), "pipeline-run-reclaim", ms(d))
}

// What one garbage collection pass found to delete.
func CreatingContainersToBeGarbageCollected(n int) {
	creatingContainersToBeGarbageCollected.Add(float64(n))
}
func CreatedContainersToBeGarbageCollected(n int) {
	createdContainersToBeGarbageCollected.Add(float64(n))
}
func DestroyingContainersToBeGarbageCollected(n int) {
	destroyingContainersToBeGarbageCollected.Add(float64(n))
}
func FailedContainersToBeGarbageCollected(n int) {
	failedContainersToBeGarbageCollected.Add(float64(n))
}
func CreatedVolumesToBeGarbageCollected(n int) { createdVolumesToBeGarbageCollected.Add(float64(n)) }
func DestroyingVolumesToBeGarbageCollected(n int) {
	destroyingVolumesToBeGarbageCollected.Add(float64(n))
}
func FailedVolumesToBeGarbageCollected(n int) { failedVolumesToBeGarbageCollected.Add(float64(n)) }

var buildStatusValues = map[string]float64{"succeeded": 0, "failed": 1, "aborted": 2, "errored": 3}

// LatestCompletedBuildStatus records a job's latest completed build. Only the
// four terminal statuses are published.
func LatestCompletedBuildStatus(team, pipeline, job, status string) {
	if value, ok := buildStatusValues[status]; ok {
		latestCompletedBuildStatus.WithLabelValues(job, pipeline, team).Set(value)
	}
}

// BuildFinished records a finished pipeline build, with its trace as the
// duration's exemplar.
func BuildFinished(build db.Build) {
	attrs := build.TracingAttrs()
	status := build.Status()

	buildsFinished.Inc()
	buildsFinishedVec.WithLabelValues(attrs["team_name"], attrs["pipeline"], attrs["job"], string(status)).Inc()

	switch status {
	case db.BuildStatusAborted:
		buildsAborted.Inc()
	case db.BuildStatusSucceeded:
		buildsSucceeded.Inc()
	case db.BuildStatusFailed:
		buildsFailed.Inc()
	case db.BuildStatusErrored:
		buildsErrored.Inc()
	}

	var traceID string
	if sc := build.SpanContext(); sc != nil {
		traceID = extractTraceID(sc.Get("traceparent"))
	}

	// seconds are the standard prometheus base unit for time
	duration := ms(build.EndTime().Sub(build.StartTime())) / 1000
	observeWithTrace(buildDurationsVec.WithLabelValues(attrs["team_name"], attrs["pipeline"], attrs["job"]), duration, traceID)

	RecordBuildDuration(
		context.Background(),
		build.EndTime().Sub(build.StartTime()),
		attrs["team_name"],
		attrs["pipeline"],
		attrs["job"],
		string(status),
	)
	RecordBuildFinished(context.Background(), string(status))
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

func observeWithTrace(observer prometheus.Observer, value float64, traceID string) {
	if eo, ok := observer.(prometheus.ExemplarObserver); ok && traceID != "" {
		eo.ObserveWithExemplar(value, prometheus.Labels{"trace_id": traceID})
		return
	}
	observer.Observe(value)
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

func LogLockAcquired(logger lager.Logger, lockID lock.LockID) {
	logger.Debug("acquired")
	if gauge, ok := lockGauge(lockID); ok {
		gauge.Inc()
	}
}

func LogLockReleased(logger lager.Logger, lockID lock.LockID) {
	logger.Debug("released")
	if gauge, ok := lockGauge(lockID); ok {
		gauge.Dec()
	}
}

func lockGauge(lockID lock.LockID) (prometheus.Gauge, bool) {
	if len(lockID) == 0 {
		return nil, false
	}
	lockType, ok := lockTypeNames[lockID[0]]
	if !ok {
		return nil, false
	}
	return locksHeld.WithLabelValues(lockType), true
}

// WorkersRegistered records how many workers the database holds in each
// state, zeroes included.
func WorkersRegistered(stateByName map[string]db.WorkerState) {
	for _, state := range db.AllWorkerStates() {
		count := 0
		for _, workerState := range stateByName {
			if workerState == state {
				count++
			}
		}
		workersRegistered.WithLabelValues(string(state)).Set(float64(count))
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
	OpenIntegrityFindings  int

	// Residue is what a drain waits on; zero with Enabled false is drained.
	Residue int
}

// HangarOutputStatus publishes one status pass.
func HangarOutputStatus(status HangarOutputSnapshot) {
	hangarOutputEnabled.Set(boolValue(status.Enabled))
	hangarOutputAtRisk.WithLabelValues(status.Reasons).Set(boolValue(status.AtRisk))
	for violation, count := range status.Violations {
		hangarOutputViolations.WithLabelValues(violation).Set(float64(count))
	}
	for kind, value := range map[string]int{
		"live_generations":        status.LiveGenerations,
		"nonterminal_captures":    status.NonterminalCaptures,
		"pending_captures":        status.PendingCaptures,
		"publishing_captures":     status.PublishingCaptures,
		"unreleased_captures":     status.UnreleasedCaptures,
		"unacknowledged_releases": status.UnacknowledgedReleases,
		"open_claims":             status.OpenClaims,
		"open_integrity_findings": status.OpenIntegrityFindings,
		"residue":                 status.Residue,
	} {
		hangarOutputPlaneInventory.WithLabelValues(kind).Set(float64(value))
	}
}

// HangarOutputReclaimPass is what one web reclaim pass did: generations it
// reclaimed, generations it deferred to a later pass (claimed or captured
// between the query and the lock), and deletes that did not answer.
func HangarOutputReclaimPass(reclaimed, deferred, failed int) {
	hangarOutputReclaimed.WithLabelValues("reclaimed").Add(float64(reclaimed))
	hangarOutputReclaimed.WithLabelValues("deferred").Add(float64(deferred))
	hangarOutputReclaimed.WithLabelValues("failed").Add(float64(failed))
}

// HangarOutputOrphanSweep is one orphan sweep's count of listed objects per
// class: the one it deleted and every class it left alone. A failed pass
// stopped on an error -- a list, a judgement or a delete -- and counts as a
// failure rather than a completed pass; a skipped pass judged nothing because
// the shared deletion lock was held elsewhere, and is neither.
func HangarOutputOrphanSweep(objects map[string]int, failed, skipped bool) {
	for class, value := range objects {
		hangarOutputSweepObjects.WithLabelValues(class).Set(float64(value))
	}
	switch {
	case skipped:
	case failed:
		hangarOutputSweepFailures.Inc()
	default:
		hangarOutputSweepPasses.Inc()
	}
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
