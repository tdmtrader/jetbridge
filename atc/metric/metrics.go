package metric

import (
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/db/lock"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

var nopLogger = lager.NewLogger("metric")

// StepWaited records how long a step waited for a worker.
func StepWaited(teamID, teamName, stepType string, waited time.Duration) {
	Metrics.emit(nopLogger, Event{
		Name:  "steps waiting duration",
		Value: waited.Seconds(),
		Attributes: map[string]string{
			"teamId":   teamID,
			"teamName": teamName,
			"type":     stepType,
		},
	})
}

func emitMs(name string, duration time.Duration) {
	Metrics.emit(nopLogger, Event{Name: name, Value: ms(duration)})
}

// The garbage collectors' pass durations.
func GCBuildCollectorDuration(d time.Duration) { emitMs("gc: build collector duration (ms)", d) }
func GCWorkerCollectorDuration(d time.Duration) {
	emitMs("gc: worker collector duration (ms)", d)
}
func GCResourceCacheUseCollectorDuration(d time.Duration) {
	emitMs("gc: resource cache use collector duration (ms)", d)
}
func GCResourceConfigCollectorDuration(d time.Duration) {
	emitMs("gc: resource config collector duration (ms)", d)
}
func GCDeprecatedScopeCollectorDuration(d time.Duration) {
	emitMs("gc: deprecated scope collector duration (ms)", d)
}
func GCResourceCacheCollectorDuration(d time.Duration) {
	emitMs("gc: resource cache collector duration (ms)", d)
}
func GCTaskCacheCollectorDuration(d time.Duration) {
	emitMs("gc: task cache collector duration (ms)", d)
}
func GCResourceConfigCheckSessionCollectorDuration(d time.Duration) {
	emitMs("gc: resource config check session collector duration (ms)", d)
}
func GCArtifactCollectorDuration(d time.Duration) {
	emitMs("gc: artifact collector duration (ms)", d)
}
func GCContainerCollectorDuration(d time.Duration) {
	emitMs("gc: container collector duration (ms)", d)
}
func GCVolumeCollectorDuration(d time.Duration) { emitMs("gc: volume collector duration (ms)", d) }

// PipelineRunReclaimBacklog is the number of pipeline runs eligible for
// reclamation at the start of a reclaimer pass, counted without the batch
// bound. Read it against the batch size: a backlog that stays above the batch
// pass after pass is a reclaimer that will never catch up, and every run in it
// is a payload pipeline, its jobs and its build rows still on disk.
func PipelineRunReclaimBacklog(runs int) {
	Metrics.emit(nopLogger, Event{Name: "pipeline run reclaim backlog", Value: float64(runs)})
}

// PipelineRunReclaimDuration is how long one reclaimer pass took, batch
// included. It is per-batch rather than per-run so that it stays comparable
// with the component's own interval: a pass that outlasts the interval is the
// other half of the backlog story.
func PipelineRunReclaimDuration(d time.Duration) {
	emitMs("gc: pipeline run reclaim duration (ms)", d)
}

// JobSchedulingDuration records one scheduling pass over a job.
func JobSchedulingDuration(pipeline, job string, jobID int, d time.Duration) {
	Metrics.emit(nopLogger, Event{
		Name:  "scheduling: job duration (ms)",
		Value: ms(d),
		Attributes: map[string]string{
			"pipeline": pipeline,
			"job":      job,
			"job_id":   strconv.Itoa(jobID),
		},
	})
}

func emitCount(name string, count int) {
	Metrics.emit(nopLogger, Event{Name: name, Value: float64(count), Attributes: map[string]string{}})
}

// What one garbage collection pass found to delete.
func CreatingContainersToBeGarbageCollected(n int) {
	emitCount("creating containers to be garbage collected", n)
}
func CreatedContainersToBeGarbageCollected(n int) {
	emitCount("created containers to be garbage collected", n)
}
func DestroyingContainersToBeGarbageCollected(n int) {
	emitCount("destroying containers to be garbage collected", n)
}
func FailedContainersToBeGarbageCollected(n int) {
	emitCount("failed containers to be garbage collected", n)
}
func CreatedVolumesToBeGarbageCollected(n int) {
	emitCount("created volumes to be garbage collected", n)
}
func DestroyingVolumesToBeGarbageCollected(n int) {
	emitCount("destroying volumes to be garbage collected", n)
}
func FailedVolumesToBeGarbageCollected(n int) {
	emitCount("failed volumes to be garbage collected", n)
}

// LatestCompletedBuildStatus records a job's latest completed build. Only the
// four terminal statuses are published.
func LatestCompletedBuildStatus(team, pipeline, job, status string) {
	var value int
	switch status {
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

	Metrics.emit(nopLogger, Event{
		Name:  "latest completed build status",
		Value: float64(value),
		Attributes: map[string]string{
			"jobName":      job,
			"pipelineName": pipeline,
			"teamName":     team,
		},
	})
}

// BuildStarted records a started pipeline build.
func BuildStarted(build db.Build) {
	Metrics.emit(nopLogger, Event{
		Name:       "build started",
		Value:      float64(build.ID()),
		Attributes: build.TracingAttrs(),
	})
}

// BuildFinished records a finished pipeline build.
func BuildFinished(build db.Build) {
	attrs := build.TracingAttrs()
	attrs["build_status"] = build.Status().String()

	duration := build.EndTime().Sub(build.StartTime())

	var traceID string
	if sc := build.SpanContext(); sc != nil {
		traceID = extractTraceID(sc.Get("traceparent"))
	}

	Metrics.emit(nopLogger, Event{
		Name:       "build finished",
		Value:      ms(duration),
		Attributes: attrs,
		TraceID:    traceID,
	})
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

func errorLogged(message string) {
	Metrics.emit(nopLogger, Event{
		Name:       "error log",
		Value:      1,
		Attributes: map[string]string{"message": message},
	})
}

func httpResponded(route, method string, status int, duration time.Duration, traceID string) {
	Metrics.emit(nopLogger, Event{
		Name:  "http response time",
		Value: ms(duration),
		Attributes: map[string]string{
			"route":  route,
			"method": method,
			"status": strconv.Itoa(status),
		},
		TraceID: traceID,
	})
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
	lockHeld(lockID, 1)
}

func LogLockReleased(logger lager.Logger, lockID lock.LockID) {
	logger.Debug("released")
	lockHeld(lockID, 0)
}

func lockHeld(lockID lock.LockID, value float64) {
	if len(lockID) == 0 {
		return
	}

	if lockType, ok := lockTypeNames[lockID[0]]; ok {
		Metrics.emit(nopLogger, Event{
			Name:       "lock held",
			Value:      value,
			Attributes: map[string]string{"type": lockType},
		})
	}
}

// WorkersRegistered records how many workers the database holds in each
// state, zeroes included.
func WorkersRegistered(stateByName map[string]db.WorkerState) {
	for _, state := range db.AllWorkerStates() {
		count := 0
		for _, workerState := range stateByName {
			if workerState == state {
				count += 1
			}
		}

		Metrics.emit(nopLogger, Event{
			Name:       "worker state",
			Value:      float64(count),
			Attributes: map[string]string{"state": string(state)},
		})
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
	Metrics.emit(nopLogger, Event{
		Name:  "hangar output enabled",
		Value: boolValue(status.Enabled),
	})
	Metrics.emit(nopLogger, Event{
		Name:       "hangar output at risk",
		Value:      boolValue(status.AtRisk),
		Attributes: map[string]string{"reasons": status.Reasons},
	})
	for violation, count := range status.Violations {
		Metrics.emit(nopLogger, Event{
			Name:       "hangar output policy violations",
			Value:      float64(count),
			Attributes: map[string]string{"violation": violation},
		})
	}
	for name, value := range map[string]int{
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
		Metrics.emit(nopLogger, Event{
			Name:       "hangar output plane inventory",
			Value:      float64(value),
			Attributes: map[string]string{"kind": name},
		})
	}
}

// HangarOutputReclaimPass is what one web reclaim pass did: generations it
// reclaimed, generations it deferred to a later pass (claimed or captured
// between the query and the lock), and deletes that did not answer.
func HangarOutputReclaimPass(reclaimed, deferred, failed int) {
	for outcome, value := range map[string]int{
		"reclaimed": reclaimed,
		"deferred":  deferred,
		"failed":    failed,
	} {
		Metrics.emit(nopLogger, Event{
			Name:       "hangar output reclaim generations",
			Value:      float64(value),
			Attributes: map[string]string{"outcome": outcome},
		})
	}
}

// HangarOutputOrphanSweep is one orphan sweep's count of listed objects per
// class: the one it deleted and every class it left alone. A failed pass
// stopped on an error -- a list, a judgement or a delete -- and counts as a
// failure rather than a completed pass; a skipped pass judged nothing because
// the shared deletion lock was held elsewhere, and is neither.
func HangarOutputOrphanSweep(objects map[string]int, failed, skipped bool) {
	for class, value := range objects {
		Metrics.emit(nopLogger, Event{
			Name:       "hangar output orphan sweep objects",
			Value:      float64(value),
			Attributes: map[string]string{"class": class},
		})
	}
	if skipped {
		return
	}
	name := "hangar output orphan sweep passes"
	if failed {
		name = "hangar output orphan sweep failures"
	}
	Metrics.emit(nopLogger, Event{Name: name, Value: 1})
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}

	return 0
}
