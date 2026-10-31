package metric_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/tracing"
	"github.com/tedsuo/ifrit"
	"go.opentelemetry.io/otel/propagation"

	. "github.com/onsi/gomega"
)

// driveEveryMetric fires every production emit site once, through the same
// calls the production code makes, so that every family a running web can put
// on /metrics is on the page.
func driveEveryMetric() {
	logger := testLogger

	// Database: queries counted through the instrumented connection, and one
	// open-connection gauge per pool.
	conn := metric.InstrumentDB(openTestConn("snapshot"))
	_, err := conn.Exec("SELECT 1")
	Expect(err).NotTo(HaveOccurred())

	// Error logs, through the lager sink.
	metric.ErrorSink{}.Log(lager.LogFormat{LogLevel: lager.ERROR, Message: "snapshot-error"})

	// Locks.
	metric.LogLockAcquired(logger, lock.LockID{lock.LockTypeBatch})

	// HTTP response times.
	server := httptest.NewServer(metric.WrapHandler("Snapshot", http.NotFoundHandler()))
	res, err := http.Get(server.URL + "/snapshot")
	Expect(err).NotTo(HaveOccurred())
	res.Body.Close()
	server.Close()

	// Scheduling, builds and checks: the sampled tallies.
	metric.Metrics.JobsScheduling.Inc()
	metric.Metrics.JobsScheduling.Dec()
	metric.Metrics.JobsScheduled.Inc()
	metric.Metrics.BuildsStarted.Inc()
	metric.Metrics.CheckBuildsStarted.Inc()
	metric.Metrics.BuildsRunning.Inc()
	metric.Metrics.BuildsRunning.Dec()
	metric.Metrics.CheckBuildsRunning.Inc()
	metric.Metrics.CheckBuildsRunning.Dec()
	metric.Metrics.ChecksStarted.Inc()
	metric.Metrics.ChecksFinishedWithSuccess.Inc()
	metric.Metrics.ChecksFinishedWithError.Inc()
	metric.Metrics.ChecksEnqueued.Inc()
	metric.Metrics.GetStepCacheHits.Inc()
	metric.Metrics.K8sPodStartupDuration.Set(1500)
	metric.Metrics.K8sImagePullFailures.Inc()

	metric.JobSchedulingDuration("pipeline", "job", 1, time.Second)
	metric.StepWaited("1", "main", "task", time.Second)
	metric.LatestCompletedBuildStatus("main", "pipeline", "job", "failed")
	metric.BuildFinished(snapshotBuild{})

	// Workers.
	metric.WorkersRegistered(map[string]db.WorkerState{"worker": db.WorkerStateRunning})

	// Garbage collection.
	metric.GCBuildCollectorDuration(time.Millisecond)
	metric.GCWorkerCollectorDuration(time.Millisecond)
	metric.GCResourceCacheUseCollectorDuration(time.Millisecond)
	metric.GCResourceConfigCollectorDuration(time.Millisecond)
	metric.GCDeprecatedScopeCollectorDuration(time.Millisecond)
	metric.GCResourceCacheCollectorDuration(time.Millisecond)
	metric.GCTaskCacheCollectorDuration(time.Millisecond)
	metric.GCResourceConfigCheckSessionCollectorDuration(time.Millisecond)
	metric.GCArtifactCollectorDuration(time.Millisecond)
	metric.GCContainerCollectorDuration(time.Millisecond)
	metric.GCVolumeCollectorDuration(time.Millisecond)
	metric.PipelineRunReclaimDuration(time.Millisecond)
	metric.PipelineRunReclaimBacklog(3)
	metric.CreatingContainersToBeGarbageCollected(1)
	metric.CreatedContainersToBeGarbageCollected(1)
	metric.DestroyingContainersToBeGarbageCollected(1)
	metric.FailedContainersToBeGarbageCollected(1)
	metric.CreatedVolumesToBeGarbageCollected(1)
	metric.DestroyingVolumesToBeGarbageCollected(1)
	metric.FailedVolumesToBeGarbageCollected(1)

	// The Hangar output plane.
	metric.HangarOutputStatus(metric.HangarOutputSnapshot{
		Enabled:    true,
		Violations: map[string]int{"missing_object": 0},
	})
	metric.HangarOutputReclaimPass(1, 0, 0)
	metric.HangarOutputOrphanSweep(map[string]int{"orphan": 1}, false, false)
	metric.HangarOutputOrphanSweep(map[string]int{"orphan": 0}, true, false)

	// One periodic tick publishes the sampled tallies and the pool gauges.
	process := ifrit.Invoke(metric.PeriodicallyEmit(logger, metric.Metrics, 50*time.Millisecond))
	time.Sleep(200 * time.Millisecond)
	process.Signal(os.Interrupt)
	<-process.Wait()
}

// snapshotBuild is a finished pipeline build with a trace, the shape
// BuildFinished reads.
type snapshotBuild struct{ db.Build }

func (snapshotBuild) TracingAttrs() tracing.Attrs {
	return tracing.Attrs{"build_id": "1", "build": "1", "team_name": "main", "pipeline": "pipeline", "job": "job"}
}
func (snapshotBuild) Status() db.BuildStatus { return db.BuildStatusSucceeded }
func (snapshotBuild) StartTime() time.Time   { return time.Unix(0, 0) }
func (snapshotBuild) EndTime() time.Time     { return time.Unix(5, 0) }
func (snapshotBuild) SpanContext() propagation.TextMapCarrier {
	return propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
}
