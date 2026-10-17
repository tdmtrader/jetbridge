package behavioral_test

import (
	"fmt"
)

// ---------------------------------------------------------------------
// Pipeline YAML fixtures / template generators
//
// These helpers generate pipeline YAML strings for common test patterns.
// Each returns a YAML string that can be passed to writePipelineFile.
// ---------------------------------------------------------------------

// fixtureMultiJob returns a pipeline with multiple jobs. Each entry in
// the jobs map is jobName -> script.
func fixtureMultiJob(jobs map[string]string) string {
	yaml := "\njobs:\n"
	for name, script := range jobs {
		yaml += fmt.Sprintf(`- name: %s
  plan:
  - task: main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, name, script)
	}
	return yaml
}

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
