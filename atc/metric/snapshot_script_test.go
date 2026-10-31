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
	conn := metric.CountQueries(openTestConn("snapshot"))
	_, err := conn.Exec("SELECT 1")
	Expect(err).NotTo(HaveOccurred())
	metric.Metrics.Databases = append(metric.Metrics.Databases, conn)

	// Error logs, through the lager sink.
	sink := metric.NewErrorSinkCollector(logger, metric.Metrics)
	sink.Log(lager.LogFormat{LogLevel: lager.ERROR, Message: "snapshot-error"})

	// Locks.
	metric.LogLockAcquired(logger, lock.LockID{lock.LockTypeBatch})

	// HTTP response times.
	server := httptest.NewServer(metric.WrapHandler(logger, metric.Metrics, "Snapshot", http.NotFoundHandler()))
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

	metric.SchedulingJobDuration{
		PipelineName: "pipeline",
		JobName:      "job",
		JobID:        1,
		Duration:     time.Second,
	}.Emit(logger)

	metric.StepsWaitingDuration{
		Labels:   metric.StepsWaitingLabels{TeamId: "1", TeamName: "main", Type: "task"},
		Duration: time.Second,
	}.Emit(logger)

	metric.JobStatus{
		Status:       "failed",
		JobName:      "job",
		PipelineName: "pipeline",
		TeamName:     "main",
	}.Emit(logger)

	metric.BuildFinished{Build: snapshotBuild{}}.Emit(logger)

	// Workers.
	metric.WorkersState{
		WorkerStateByName: map[string]db.WorkerState{"worker": db.WorkerStateRunning},
	}.Emit(logger, metric.Metrics)

	// Garbage collection.
	metric.BuildCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.WorkerCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ResourceCacheUseCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ResourceConfigCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.DeprecatedScopeCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ResourceCacheCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.TaskCacheCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ResourceConfigCheckSessionCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ArtifactCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.ContainerCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.VolumeCollectorDuration{Duration: time.Millisecond}.Emit(logger)
	metric.PipelineRunReclaimDuration{Duration: time.Millisecond}.Emit(logger)
	metric.PipelineRunReclaimBacklog{Runs: 3}.Emit(logger)
	metric.CreatingContainersToBeGarbageCollected{Containers: 1}.Emit(logger)
	metric.CreatedContainersToBeGarbageCollected{Containers: 1}.Emit(logger)
	metric.DestroyingContainersToBeGarbageCollected{Containers: 1}.Emit(logger)
	metric.FailedContainersToBeGarbageCollected{Containers: 1}.Emit(logger)
	metric.CreatedVolumesToBeGarbageCollected{Volumes: 1}.Emit(logger)
	metric.DestroyingVolumesToBeGarbageCollected{Volumes: 1}.Emit(logger)
	metric.FailedVolumesToBeGarbageCollected{Volumes: 1}.Emit(logger)

	// The Hangar output plane.
	metric.HangarOutputStatus{Status: metric.HangarOutputSnapshot{
		Enabled:    true,
		Violations: map[string]int{"missing_object": 0},
	}}.Emit(logger)
	metric.HangarOutputReclaimPass{Reclaimed: 1}.Emit(logger)
	metric.HangarOutputOrphanSweep{Objects: map[string]int{"orphan": 1}}.Emit(logger)
	metric.HangarOutputOrphanSweep{Objects: map[string]int{"orphan": 0}, Failed: true}.Emit(logger)

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
