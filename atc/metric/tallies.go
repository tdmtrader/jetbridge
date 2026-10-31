package metric

// Tallies are the process-wide values the periodic tick samples into
// Prometheus every interval: counters are published as the delta since the
// last tick, gauges as the highest level seen since the last tick.
type Tallies struct {
	DatabaseQueries Counter

	JobsScheduled  Counter
	JobsScheduling Gauge

	// Check builds are counted apart so that BuildsStarted means what the
	// dashboards built on it expect.
	BuildsStarted      Counter
	BuildsRunning      Gauge
	CheckBuildsStarted Counter
	CheckBuildsRunning Gauge

	// When global resource is not enabled, ChecksStarted should equal to CheckBuildsStarted.
	// But with global resource enabled, ChecksStarted measures how many checks really run.
	// For example, there are 10 resources having exact same config, so they belong to the same
	// resource configure scope. In each check period, 10 check builds will be created,
	// CheckBuildsStarted should be 10. But only 1 check build should run real check, rest 9 check
	// builds should reuse the first check's result, thus ChecksStarted will be 1.
	// The bigger diff between ChecksStarted and CheckBuildsStarted, the more global resource benefits.
	ChecksStarted Counter

	// ChecksFinishedWithError+ChecksFinishedWithSuccess should equal to ChecksStarted.
	ChecksFinishedWithError   Counter
	ChecksFinishedWithSuccess Counter

	ChecksEnqueued   Counter
	GetStepCacheHits Counter

	K8sPodStartupDuration Gauge
	K8sImagePullFailures  Counter

	// Counted but published nowhere: the brine scenarios read them to see
	// that the runtime did what it says. Nothing scrapes them.
	ContainersCreated      Counter
	FailedContainers       Counter
	ContainersDeleted      Counter
	ResourceCacheLocalHits Counter
}

var Metrics = &Tallies{}
