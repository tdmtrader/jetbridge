package metric

import (
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
	dbQueriesTotal.Add(Metrics.DatabaseQueries.Delta())
	for _, conn := range instrumentedDBs() {
		dbConnections.WithLabelValues(conn.Name()).Set(float64(conn.Stats().OpenConnections))
	}

	jobsScheduled.Add(Metrics.JobsScheduled.Delta())
	jobsScheduling.Set(Metrics.JobsScheduling.Max())

	buildsStarted.Add(Metrics.BuildsStarted.Delta())
	buildsRunning.Set(Metrics.BuildsRunning.Max())
	checkBuildsStarted.Add(Metrics.CheckBuildsStarted.Delta())
	checkBuildsRunning.Set(Metrics.CheckBuildsRunning.Max())

	checksFinished.WithLabelValues("error").Add(Metrics.ChecksFinishedWithError.Delta())
	checksFinished.WithLabelValues("success").Add(Metrics.ChecksFinishedWithSuccess.Delta())
	checksStarted.Add(Metrics.ChecksStarted.Delta())
	checksEnqueued.Add(Metrics.ChecksEnqueued.Delta())
	getStepCacheHits.Add(Metrics.GetStepCacheHits.Delta())

	k8sPodStartupDuration.Observe(Metrics.K8sPodStartupDuration.Max())
	k8sImagePullFailures.Add(Metrics.K8sImagePullFailures.Delta())
}
