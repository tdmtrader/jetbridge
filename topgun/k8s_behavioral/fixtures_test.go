package behavioral_test

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------
// Pipeline YAML fixtures / template generators
//
// These helpers generate pipeline YAML strings for common test patterns.
// Each returns a YAML string that can be passed to writePipelineFile.
// ---------------------------------------------------------------------

// yamlToPrintfArgs converts a multi-line YAML string (as typically passed
// from Go) into a series of single-quoted shell arguments for printf.
// It strips the common leading whitespace (dedent) and drops blank lines,
// preserving the relative indentation structure of the YAML content.
func yamlToPrintfArgs(yaml string) string {
	lines := strings.Split(yaml, "\n")

	// Find the minimum indentation of non-empty lines.
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent < 0 {
		minIndent = 0
	}

	// Build printf arguments, one per non-empty line, dedented.
	var args []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		dedented := line
		if len(dedented) >= minIndent {
			dedented = dedented[minIndent:]
		}
		// Escape single quotes for shell: replace ' with '\''
		escaped := strings.ReplaceAll(dedented, "'", "'\\''")
		args = append(args, fmt.Sprintf("'%s'", escaped))
	}
	return strings.Join(args, " ")
}

// fixtureTaskWithTimeout returns a pipeline with a task that has a timeout.
func fixtureTaskWithTimeout(jobName, timeout, script string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    timeout: %s
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, timeout, script)
}

// fixtureTaskWithRetries returns a pipeline with a task that has attempts.
func fixtureTaskWithRetries(jobName string, attempts int, script string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    attempts: %d
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, attempts, script)
}

// fixtureTryStep returns a pipeline with a try step wrapping a task,
// followed by a continuation task.
func fixtureTryStep(jobName, tryScript, afterScript string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - try:
      task: may-fail
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args:
          - -c
          - |
            %s
  - task: after-try
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, tryScript, afterScript)
}

// fixtureDoStep returns a pipeline with a do block containing sequential tasks.
func fixtureDoStep(jobName string, taskScripts map[string]string) string {
	tasks := ""
	for name, script := range taskScripts {
		tasks += fmt.Sprintf(`    - task: %s
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
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - do:
%s`, jobName, tasks)
}

// fixtureAcrossStep returns a pipeline with a task using across with
// static values.
func fixtureAcrossStep(jobName, varName string, values []string, script string) string {
	valuesYAML := ""
	for _, v := range values {
		valuesYAML += fmt.Sprintf(`"%s", `, v)
	}
	if len(valuesYAML) > 2 {
		valuesYAML = valuesYAML[:len(valuesYAML)-2] // trim trailing comma+space
	}
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: across-task
    across:
    - var: %s
      values: [%s]
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        VAL: ((.:%s))
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, varName, valuesYAML, varName, script)
}

// fixtureCustomResourceType returns a pipeline with a custom resource_type
// definition and a resource using it.
func fixtureCustomResourceType(jobName, typeName, typeImage, resourceName, script string) string {
	return fmt.Sprintf(`
resource_types:
- name: %s
  type: registry-image
  source:
    repository: %s

resources:
- name: %s
  type: %s
  source: {}

jobs:
- name: %s
  plan:
  - get: %s
    trigger: false
  - task: use-it
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, typeName, typeImage, resourceName, typeName, jobName, resourceName, script)
}

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
