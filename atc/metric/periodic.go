package metric

import (
	"context"
	"os"
	"time"

	"github.com/tedsuo/ifrit"
)

// PeriodicallyEmit samples the tallies and the database pools into
// Prometheus every interval.
func PeriodicallyEmit(interval time.Duration) ifrit.Runner {
	return ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		close(ready)

		for {
			select {
			case <-signals:
				return nil
			case <-ticker.C:
				tick()
			}
		}
	})
}

func tick() {
	ctx := context.Background()

	dbQueries := Metrics.DatabaseQueries.Delta()
	dbQueriesTotal.Add(dbQueries)
	if dbQueries > 0 {
		RecordDBQueries(ctx, dbQueries)
	}
	for _, conn := range instrumentedDBs() {
		open := float64(conn.Stats().OpenConnections)
		dbConnections.WithLabelValues(conn.Name()).Set(open)
		RecordDBConnections(ctx, open, conn.Name())
	}

	scheduled := Metrics.JobsScheduled.Delta()
	jobsScheduled.Add(scheduled)
	if scheduled > 0 {
		RecordJobsScheduled(ctx, scheduled)
	}
	scheduling := Metrics.JobsScheduling.Max()
	jobsScheduling.Set(scheduling)
	RecordJobsScheduling(ctx, scheduling)

	started := Metrics.BuildsStarted.Delta()
	buildsStarted.Add(started)
	if started > 0 {
		RecordBuildsStarted(ctx, started)
	}
	running := Metrics.BuildsRunning.Max()
	buildsRunning.Set(running)
	RecordBuildsRunning(ctx, running)
	checkStarted := Metrics.CheckBuildsStarted.Delta()
	checkBuildsStarted.Add(checkStarted)
	if checkStarted > 0 {
		RecordCheckBuildsStarted(ctx, checkStarted)
	}
	checkRunning := Metrics.CheckBuildsRunning.Max()
	checkBuildsRunning.Set(checkRunning)
	RecordCheckBuildsRunning(ctx, checkRunning)

	failedChecks := Metrics.ChecksFinishedWithError.Delta()
	checksFinished.WithLabelValues("error").Add(failedChecks)
	if failedChecks > 0 {
		RecordChecksFinished(ctx, failedChecks, "error")
	}
	succeededChecks := Metrics.ChecksFinishedWithSuccess.Delta()
	checksFinished.WithLabelValues("success").Add(succeededChecks)
	if succeededChecks > 0 {
		RecordChecksFinished(ctx, succeededChecks, "success")
	}
	startedChecks := Metrics.ChecksStarted.Delta()
	checksStarted.Add(startedChecks)
	if startedChecks > 0 {
		RecordChecksStarted(ctx, startedChecks)
	}
	enqueuedChecks := Metrics.ChecksEnqueued.Delta()
	checksEnqueued.Add(enqueuedChecks)
	if enqueuedChecks > 0 {
		RecordChecksEnqueued(ctx, enqueuedChecks)
	}
	getStepCacheHits.Add(Metrics.GetStepCacheHits.Delta())

	podStartupMs := Metrics.K8sPodStartupDuration.Max()
	k8sPodStartupDuration.Observe(podStartupMs)
	if podStartupMs > 0 {
		RecordK8sPodStartupDuration(ctx, time.Duration(podStartupMs)*time.Millisecond)
	}
	k8sImagePullFailures.Add(Metrics.K8sImagePullFailures.Delta())
}
