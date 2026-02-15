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

// fixtureSimpleTask returns a pipeline with a single inline task job.
func fixtureSimpleTask(jobName, script string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
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
`, jobName, script)
}

// fixtureTaskWithParams returns a pipeline with a task that has params.
func fixtureTaskWithParams(jobName string, params map[string]string, script string) string {
	paramsYAML := ""
	for k, v := range params {
		paramsYAML += fmt.Sprintf("        %s: %s\n", k, v)
	}
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
%s      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, paramsYAML, script)
}

// fixtureTaskWithImage returns a pipeline with a task using a custom image.
func fixtureTaskWithImage(jobName, image, script string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: %s}}
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, image, script)
}

// fixtureGetTaskPut returns a pipeline with a get -> task -> put flow.
func fixtureGetTaskPut(jobName, resourceName, script string) string {
	return fmt.Sprintf(`
resources:
- name: %s
  type: mock
  source:
    create_files:
      data.txt: "mock-data"
- name: output
  type: mock
  source: {}

jobs:
- name: %s
  plan:
  - get: %s
    trigger: false
  - task: process
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: %s
      outputs:
      - name: result
      run:
        path: sh
        args:
        - -c
        - |
          %s
  - put: output
    params:
      version: v1
`, resourceName, jobName, resourceName, resourceName, script)
}

// fixtureGetResource returns a pipeline with a get step and a task that reads it.
func fixtureGetResource(jobName, resourceName, fileName, fileContent, script string) string {
	return fmt.Sprintf(`
resources:
- name: %s
  type: mock
  source:
    create_files:
      %s: "%s"

jobs:
- name: %s
  plan:
  - get: %s
    trigger: false
  - task: read
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: %s
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, resourceName, fileName, fileContent, jobName, resourceName, resourceName, script)
}

// fixturePutResource returns a pipeline with a task that produces output
// and a put step that consumes it.
func fixturePutResource(jobName, resourceName, script string) string {
	return fmt.Sprintf(`
resources:
- name: %s
  type: mock
  source: {}

jobs:
- name: %s
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: produced
      run:
        path: sh
        args:
        - -c
        - |
          %s
  - put: %s
    params:
      version: v1
`, resourceName, jobName, script, resourceName)
}

// fixtureParallelTasks returns a pipeline with N tasks running in_parallel.
func fixtureParallelTasks(jobName string, taskScripts map[string]string) string {
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
  - in_parallel:
%s`, jobName, tasks)
}

// fixtureTaskWithHook returns a pipeline with a task and a single hook.
// hookType must be one of: on_success, on_failure, on_abort, ensure.
func fixtureTaskWithHook(jobName, hookType, mainScript, hookScript string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
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
    %s:
      task: hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args:
          - -c
          - |
            %s
`, jobName, mainScript, hookType, hookScript)
}

// fixtureTaskWithEnsure returns a pipeline with a task and an ensure step.
func fixtureTaskWithEnsure(jobName, mainScript, ensureScript string) string {
	return fixtureTaskWithHook(jobName, "ensure", mainScript, ensureScript)
}

// fixtureLoadVar returns a pipeline that produces a value, loads it,
// and uses it in a subsequent task.
func fixtureLoadVar(jobName, varName, produceScript, consumeScript string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - |
          %s
  - load_var: %s
    file: values/val.txt
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        LOADED: ((.:%s))
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, produceScript, varName, varName, consumeScript)
}

// fixtureLoadVarJSON returns a pipeline that produces JSON, loads it
// with format: json, and uses a nested key in a task.
func fixtureLoadVarJSON(jobName, varName, jsonContent, consumeScript string) string {
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: produce-json
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: values
      run:
        path: sh
        args:
        - -c
        - echo '%s' > values/data.json
  - load_var: %s
    file: values/data.json
    format: json
  - task: consume
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        DATA: ((.:%s))
      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, jsonContent, varName, varName, consumeScript)
}

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

// fixtureSetPipeline returns a pipeline that generates a child pipeline
// YAML from a task output and sets it via set_pipeline step.
func fixtureSetPipeline(jobName, childPipelineName, childPipelineYAML string) string {
	printfArgs := yamlToPrintfArgs(childPipelineYAML)
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: generate-pipeline
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: pipeline-config
      run:
        path: sh
        args:
        - -c
        - |
          printf '%%s\n' %s > pipeline-config/pipeline.yml
          echo "pipeline-generated"
  - set_pipeline: %s
    file: pipeline-config/pipeline.yml
`, jobName, printfArgs, childPipelineName)
}

// fixtureTaskWithInputsOutputs returns a pipeline with a task that has
// explicit inputs and outputs.
func fixtureTaskWithInputsOutputs(jobName string, inputs, outputs []string, script string) string {
	inputsYAML := ""
	for _, inp := range inputs {
		inputsYAML += fmt.Sprintf("      - name: %s\n", inp)
	}
	outputsYAML := ""
	for _, out := range outputs {
		outputsYAML += fmt.Sprintf("      - name: %s\n", out)
	}
	return fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
%s      outputs:
%s      run:
        path: sh
        args:
        - -c
        - |
          %s
`, jobName, inputsYAML, outputsYAML, script)
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
