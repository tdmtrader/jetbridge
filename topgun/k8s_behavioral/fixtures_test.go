package behavioral_test

// ---------------------------------------------------------------------
// Pod hygiene fixture
// ---------------------------------------------------------------------

// assertPostBuildHygiene is the standard post-build pod hygiene check
// that every test can call after a build completes. It verifies:
// 1. All concourse workload pods for the current pipeline are cleaned up
// 2. No orphaned pods remain with the concourse.ci/worker label
func assertPostBuildHygiene() {
	assertPodCleanupForPipeline()
}
