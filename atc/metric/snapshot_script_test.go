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
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
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
	metric.BuildFinished(tracedBuild{})

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

	// The periodic tick publishes the sampled tallies and the pool gauges. It
	// runs until the spec ends, so the comparison waits for a tick rather than
	// racing one.
	process := ifrit.Invoke(metric.PeriodicallyEmit(50 * time.Millisecond))
	DeferCleanup(func() {
		process.Signal(os.Interrupt)
		<-process.Wait()
	})
}
