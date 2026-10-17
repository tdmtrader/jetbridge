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

// fixtureTaskWithCaches returns a pipeline with a task that uses caches.
func fixtureTaskWithCaches(jobName string, cachePaths []string, script string) string {
	cachesYAML := ""
	for _, p := range cachePaths {
		cachesYAML += fmt.Sprintf("      - path: %s\n", p)
	}
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
%s      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, cachesYAML, script)
}

// fixtureJobWithEnsure returns a pipeline with a job-level ensure hook.
func fixtureJobWithEnsure(jobName, mainScript, ensureScript string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  ensure:
    task: job-ensure
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
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
`, jobName, ensureScript, mainScript)
}

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
