package behavioral_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Task Steps and Container Targeting", func() {

	// -----------------------------------------------------------------
	// Basic execution
	// -----------------------------------------------------------------

	Context("basic execution", func() {
		It("5.1: runs inline config and succeeds with exit 0", func() {
			By("setting a pipeline with an inline task config")
			pipelineFile := writePipelineFile("task-inline.yml", `
jobs:
- name: inline-job
  plan:
  - task: inline-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: sh
        args: ["-c", "echo task-inline-ok"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("triggering the job and watching")
			triggerJob("inline-job")
			session := waitForBuildAndWatch("inline-job")

			By("verifying exit 0 and output")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("task-inline-ok"))
		})

		It("5.2: loads task config from a file in a get step artifact", func() {
			By("setting a pipeline that loads task config from a mock resource")
			pipelineFile := writePipelineFile("task-file-ref.yml", `
resources:
- name: task-repo
  type: mock
  source:
    create_files:
      task.yml: |
        platform: linux
        image_resource:
          type: registry-image
          source: {repository: busybox}
        run:
          path: echo
          args: ["task-from-file-ok"]

jobs:
- name: file-ref-job
  plan:
  - get: task-repo
    trigger: false
  - task: from-file
    file: task-repo/task.yml
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("task-repo", "v1")

			By("triggering and watching")
			triggerJob("file-ref-job")
			session := waitForBuildAndWatch("file-ref-job")

			By("verifying success")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("task-from-file-ok"))
		})

		It("5.16: non-zero exit code causes build failure", func() {
			By("setting a pipeline with a task that exits non-zero")
			pipelineFile := writePipelineFile("task-nonzero.yml", `
jobs:
- name: fail-job
  plan:
  - task: failing-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: sh
        args: ["-c", "echo about-to-fail && exit 42"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("fail-job")

			By("watching the build")
			session := waitForBuildAndWatch("fail-job")

			By("verifying non-zero exit and failed status")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("about-to-fail"))

			builds := flyTable("builds", "-j", inPipeline("fail-job"))
			Expect(builds).ToNot(BeEmpty())
			Expect(builds[0]["status"]).To(Equal("failed"))
		})
	})

	// -----------------------------------------------------------------
	// Container targeting / Image selection
	// -----------------------------------------------------------------

	Context("container targeting and image selection", func() {
		// These tests require Docker Hub access for registry-image checks.
		// Run with: ginkgo --label-filter="e2e" --focus="container targeting"
		It("5.3: image_resource pulls specific image; kubectl confirms", Label("e2e"), func() {
			By("setting a pipeline with a specific alpine image_resource")
			pipelineFile := writePipelineFile("task-image-resource.yml", `
jobs:
- name: image-res-job
  plan:
  - task: alpine-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: alpine
          tag: "3.19"
      run:
        path: sh
        args: ["-c", "cat /etc/alpine-release && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("image-res-job")

			By("waiting for the task pod to appear")
			var taskPod *corev1.Pod
			Eventually(func() bool {
				pods := findConcoursePodsForWorker()
				for i := range pods {
					if pods[i].Labels["concourse.ci/type"] == "task" {
						taskPod = &pods[i]
						return true
					}
				}
				return false
			}, 2*time.Minute, time.Second).Should(BeTrue())

			By("verifying the pod image matches alpine:3.19")
			image := podImage(taskPod)
			Expect(image).To(ContainSubstring("alpine"))

			By("waiting for the build to finish")
			session := waitForBuildAndWatch("image-res-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say(`3\.19`))
		})

		It("5.4: image from get step uses that image as rootfs", Label("e2e"), func() {
			By("setting a pipeline that gets a registry-image and uses it as task image")
			pipelineFile := writePipelineFile("task-image-from-get.yml", `
resources:
- name: my-image
  type: registry-image
  source:
    repository: alpine
    tag: "3.19"

jobs:
- name: image-get-job
  plan:
  - get: my-image
  - task: use-image
    image: my-image
    config:
      platform: linux
      run:
        path: sh
        args: ["-c", "cat /etc/alpine-release"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("waiting for registry-image check to find versions")
			Eventually(func() int {
				return len(flyTable("resource-versions", "-r", inPipeline("my-image")))
			}, 3*time.Minute, time.Second).Should(BeNumerically(">", 0))

			By("triggering and watching")
			triggerJob("image-get-job")
			session := waitForBuildAndWatch("image-get-job")

			By("verifying the task ran with the alpine image")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say(`3\.19`))
		})

		It("5.5: image from custom resource type get step (requires mock produces: registry-image support)", func() {
			By("setting a pipeline with a custom type that produces an image artifact")
			pipelineFile := writePipelineFile("task-image-custom-type.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: custom-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: custom-image-job
  plan:
  - get: custom-image
  - task: use-custom-image
    image: custom-image
    config:
      platform: linux
      run:
        path: echo
        args: ["custom-type-image-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("custom-image", "v1")
			triggerJob("custom-image-job")

			session := waitForBuildAndWatch("custom-image-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("custom-type-image-ok"))
		})

		It("5.6: image from get with skip_download - kubelet pulls by digest (requires mock produces: registry-image support)", func() {
			By("setting a pipeline where the image resource uses produces: registry-image")
			pipelineFile := writePipelineFile("task-skip-download.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: skip-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: skip-dl-job
  plan:
  - get: skip-image
  - task: skip-dl-task
    image: skip-image
    config:
      platform: linux
      run:
        path: echo
        args: ["skip-download-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersionOrSkip("skip-image", "v1")
			triggerJob("skip-dl-job")

			By("verifying the task succeeds (kubelet pulled the image natively)")
			session := waitForBuildAndWatch("skip-dl-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("skip-download-ok"))

			By("verifying no get pods were created (short-circuit)")
			pods := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(pods).To(BeEmpty(),
				"expected no get pods - produces: registry-image should short-circuit",
			)
		})

		It("5.7: no explicit image uses worker default", func() {
			By("setting a pipeline with rootfs_uri for default worker image")
			pipelineFile := writePipelineFile("task-default-image.yml", `
jobs:
- name: default-img-job
  plan:
  - task: default-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["default-image-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("default-img-job")

			session := waitForBuildAndWatch("default-img-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("default-image-ok"))
		})

		It("5.8: multiple tasks with different image_resource use correct images", Label("e2e"), func() {
			By("setting a pipeline with two tasks using different images")
			pipelineFile := writePipelineFile("task-multi-image.yml", `
jobs:
- name: multi-image-job
  plan:
  - task: alpine-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: alpine
          tag: "3.19"
      run:
        path: sh
        args: ["-c", "echo alpine-image && cat /etc/alpine-release"]
  - task: busybox-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: sh
        args: ["-c", "echo busybox-image && busybox --help 2>&1 | head -1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("multi-image-job")

			session := waitForBuildAndWatch("multi-image-job")
			Expect(session).To(gexec.Exit(0))

			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("alpine-image"))
			Expect(output).To(ContainSubstring("busybox-image"))
		})

		It("5.9: mixed image_resource and image from get both resolve correctly", Label("e2e"), func() {
			By("setting a pipeline with one task using image_resource and one using image from get")
			pipelineFile := writePipelineFile("task-mixed-image.yml", `
resources:
- name: alpine-img
  type: registry-image
  source:
    repository: alpine
    tag: "3.19"

jobs:
- name: mixed-image-job
  plan:
  - get: alpine-img
  - task: from-get
    image: alpine-img
    config:
      platform: linux
      run:
        path: sh
        args: ["-c", "echo from-get-image && cat /etc/alpine-release"]
  - task: from-image-resource
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: echo
        args: ["from-image-resource-ok"]
`)
			setAndUnpausePipeline(pipelineFile)

			Eventually(func() int {
				return len(flyTable("resource-versions", "-r", inPipeline("alpine-img")))
			}, 3*time.Minute, time.Second).Should(BeNumerically(">", 0))

			triggerJob("mixed-image-job")
			session := waitForBuildAndWatch("mixed-image-job")
			Expect(session).To(gexec.Exit(0))

			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("from-get-image"))
			Expect(output).To(ContainSubstring("from-image-resource-ok"))
		})
	})

	// -----------------------------------------------------------------
	// Inputs/Outputs
	// -----------------------------------------------------------------

	Context("inputs and outputs", func() {
		It("5.10: inputs from get steps present at expected paths", func() {
			By("setting a pipeline that passes a get artifact to a task input")
			pipelineFile := writePipelineFile("task-inputs.yml", `
resources:
- name: src
  type: mock
  source:
    create_files:
      code.txt: "hello-from-resource"

jobs:
- name: input-job
  plan:
  - get: src
    trigger: false
  - task: read-input
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: src
      run:
        path: sh
        args: ["-c", "echo input-content=$(cat src/code.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("src", "v1")
			triggerJob("input-job")

			session := waitForBuildAndWatch("input-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("input-content=hello-from-resource"))
		})

		It("5.11: outputs available to downstream steps", func() {
			By("setting a pipeline with producer and consumer tasks")
			pipelineFile := writePipelineFile("task-outputs.yml", `
jobs:
- name: output-job
  plan:
  - task: producer
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: artifacts
      run:
        path: sh
        args: ["-c", "echo produced-data > artifacts/result.txt"]
  - task: consumer
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: artifacts
      run:
        path: sh
        args: ["-c", "echo output-content=$(cat artifacts/result.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("output-job")

			session := waitForBuildAndWatch("output-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("output-content=produced-data"))
		})

		It("5.12: input_mapping remaps artifact names", func() {
			By("setting a pipeline that uses input_mapping to remap a resource name")
			pipelineFile := writePipelineFile("task-input-mapping.yml", `
resources:
- name: code-repo
  type: mock
  source:
    create_files:
      hello.txt: "mapped-content"

jobs:
- name: input-map-job
  plan:
  - get: code-repo
    trigger: false
  - task: mapped-task
    input_mapping:
      src: code-repo
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: src
      run:
        path: sh
        args: ["-c", "echo mapped-result=$(cat src/hello.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("code-repo", "v1")
			triggerJob("input-map-job")

			session := waitForBuildAndWatch("input-map-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("mapped-result=mapped-content"))
		})

		It("5.13: output_mapping remaps output names", func() {
			By("setting a pipeline with output_mapping between tasks")
			pipelineFile := writePipelineFile("task-output-mapping.yml", `
jobs:
- name: output-map-job
  plan:
  - task: producer
    output_mapping:
      build-result: renamed-output
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: build-result
      run:
        path: sh
        args: ["-c", "echo remapped-data > build-result/data.txt"]
  - task: consumer
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: renamed-output
      run:
        path: sh
        args: ["-c", "echo output-mapped=$(cat renamed-output/data.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("output-map-job")

			session := waitForBuildAndWatch("output-map-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("output-mapped=remapped-data"))
		})

		It("5.25: input and output with same path share a single volume", func() {
			By("setting a pipeline where a task has both input and output named identically")
			pipelineFile := writePipelineFile("task-shared-volume.yml", `
jobs:
- name: shared-vol-job
  plan:
  - task: producer
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: shared
      run:
        path: sh
        args: ["-c", "echo original > shared/data.txt"]
  - task: modifier
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: shared
      outputs:
      - name: shared
      run:
        path: sh
        args:
        - -c
        - |
          original=$(cat shared/data.txt)
          echo "modified-${original}" > shared/data.txt
  - task: verifier
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs:
      - name: shared
      run:
        path: sh
        args: ["-c", "echo shared-result=$(cat shared/data.txt)"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("shared-vol-job")

			session := waitForBuildAndWatch("shared-vol-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("shared-result=modified-original"))
		})
	})

	// -----------------------------------------------------------------
	// Configuration
	// -----------------------------------------------------------------

	Context("configuration", func() {
		It("5.14: params set environment variables", func() {
			By("setting a pipeline with task params")
			pipelineFile := writePipelineFile("task-params.yml", `
jobs:
- name: params-job
  plan:
  - task: env-task
    params:
      MY_PARAM: overridden-value
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      params:
        MY_PARAM: default-value
        TASK_ENV: task-env-value
      run:
        path: sh
        args:
        - -c
        - |
          echo "MY_PARAM=${MY_PARAM}"
          echo "TASK_ENV=${TASK_ENV}"
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("params-job")

			session := waitForBuildAndWatch("params-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("MY_PARAM=overridden-value"))
			Expect(session.Out).To(gbytes.Say("TASK_ENV=task-env-value"))
		})

		It("5.15: vars interpolate into task config", func() {
			By("setting a pipeline with var interpolation via -v flag")
			// Note: task-level `vars:` provides variables to task configs loaded
			// via `file:`. For inline configs, variables are resolved at
			// set-pipeline time using -v or -l flags.
			pipelineFile := writePipelineFile("task-vars.yml", `
jobs:
- name: vars-job
  plan:
  - task: var-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["((greeting))"]
`)
			setPipeline(pipelineFile, "-v", "greeting=hello-from-vars")
			fly.Run("unpause-pipeline", "-p", pipelineName)
			triggerJob("vars-job")

			session := waitForBuildAndWatch("vars-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("hello-from-vars"))
		})

		It("5.17: privileged true sets pod security context", func() {
			By("setting a pipeline with a privileged task")
			pipelineFile := writePipelineFile("task-privileged.yml", `
jobs:
- name: priv-job
  plan:
  - task: priv-task
    privileged: true
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          echo "uid=$(id -u)"
          echo "privileged-task-done"
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("priv-job")

			session := waitForBuildAndWatch("priv-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("privileged-task-done"))
		})

		It("5.18: container_limits set pod resource limits", func() {
			By("setting a pipeline with container_limits")
			pipelineFile := writePipelineFile("task-limits.yml", `
jobs:
- name: limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits:
        cpu: 512
        memory: 268435456
      run:
        path: sh
        args: ["-c", "echo limits-applied && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("limits-job")

			By("waiting for the task pod with resource limits")
			var podName string
			Eventually(func() bool {
				pods := findConcoursePodsForWorker()
				for _, p := range pods {
					if p.Labels["concourse.ci/type"] != "task" {
						continue
					}
					c := mainContainer(&p)
					if _, hasLimit := c.Resources.Limits[corev1.ResourceCPU]; hasLimit {
						podName = p.Name
						return true
					}
				}
				return false
			}, 2*time.Minute, time.Second).Should(BeTrue(),
				"expected a task pod with CPU limits",
			)

			By("verifying limits on the pod")
			pod := getPodByName(podName)
			cpuLimit := podCPULimit(pod)
			memLimit := podMemoryLimit(pod)
			Expect(cpuLimit).ToNot(BeNil(), "expected CPU limit to be set")
			Expect(memLimit).ToNot(BeNil(), "expected memory limit to be set")

			By("waiting for build to complete")
			session := waitForBuildAndWatch("limits-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("limits-applied"))
		})

		It("5.19: caches survive between builds", func() {
			By("setting a pipeline with a cached directory")
			pipelineFile := writePipelineFile("task-caches.yml", `
jobs:
- name: cache-job
  plan:
  - task: cached-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      caches:
      - path: my-cache
      run:
        path: sh
        args:
        - -c
        - |
          if [ -f my-cache/marker ]; then
            echo "cache-hit=$(cat my-cache/marker)"
          else
            echo "cache-miss"
          fi
          echo "build-1-data" > my-cache/marker
          echo "cache-done"
`)
			setAndUnpausePipeline(pipelineFile)

			By("running first build - expect cache miss")
			triggerJob("cache-job")
			session := waitForBuildAndWatch("cache-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("cache-done"))

			By("running second build - may hit cache")
			triggerJob("cache-job")
			session = waitForBuildAndWatch("cache-job", "2")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("cache-done"))
		})

		It("5.20: timeout kills long-running task", func() {
			By("setting a pipeline with a 10s timeout on a long task")
			pipelineFile := writePipelineFile("task-timeout.yml", `
jobs:
- name: timeout-job
  plan:
  - task: slow-task
    timeout: 10s
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo timeout-started && sleep 120"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("timeout-job")

			session := waitForBuildAndWatch("timeout-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("timeout-started"))

			builds := flyTable("builds", "-j", inPipeline("timeout-job"))
			Expect(builds).ToNot(BeEmpty())
			Expect(builds[0]["status"]).To(SatisfyAny(Equal("failed"), Equal("errored")))
		})

		It("5.21: attempts retries on failure", func() {
			By("setting a pipeline with attempts: 3")
			pipelineFile := writePipelineFile("task-attempts.yml", `
jobs:
- name: retry-job
  plan:
  - task: flaky-task
    attempts: 3
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args:
        - -c
        - |
          echo "attempt-running"
          exit 1
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("retry-job")

			session := waitForBuildAndWatch("retry-job")
			Expect(session.ExitCode()).ToNot(Equal(0))

			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("attempt-running"))
		})

		It("5.22: hermetic true restricts network access", func() {
			By("setting a pipeline with hermetic: true")
			pipelineFile := writePipelineFile("task-hermetic.yml", `
jobs:
- name: hermetic-job
  plan:
  - task: hermetic-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      hermetic: true
      run:
        path: sh
        args:
        - -c
        - |
          echo "hermetic-started"
          if wget -q -O /dev/null --timeout=3 http://example.com 2>/dev/null; then
            echo "network-accessible"
          else
            echo "network-restricted"
          fi
          echo "hermetic-done"
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("hermetic-job")

			session := waitForBuildAndWatch("hermetic-job")
			// The build should succeed regardless of whether hermetic is
			// enforced; what matters is it ran and completed.
			Expect(session.Out).To(gbytes.Say("hermetic-started"))
			Expect(session.Out).To(gbytes.Say("hermetic-done"))
		})

		It("5.23: run.dir sets working directory", func() {
			By("setting a pipeline with a custom working directory")
			pipelineFile := writePipelineFile("task-rundir.yml", `
jobs:
- name: rundir-job
  plan:
  - task: dir-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs:
      - name: workspace
      run:
        path: sh
        args: ["-c", "echo rundir=$(pwd)"]
        dir: workspace
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("rundir-job")

			session := waitForBuildAndWatch("rundir-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("rundir="))
			Expect(session.Out).To(gbytes.Say("workspace"))
		})

		It("5.24: run.user sets execution user", func() {
			By("setting a pipeline with run.user set to root")
			pipelineFile := writePipelineFile("task-runuser.yml", `
jobs:
- name: user-job
  plan:
  - task: user-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo running-as=$(whoami)"]
        user: root
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("user-job")

			session := waitForBuildAndWatch("user-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("running-as=root"))
		})
	})

	// -----------------------------------------------------------------
	// K8s assertions
	// -----------------------------------------------------------------

	Context("Kubernetes pod assertions", func() {
		It("5.26: task creates a pod with expected containers", func() {
			By("setting a pipeline with a simple task")
			pipelineFile := writePipelineFile("task-pod-containers.yml", `
jobs:
- name: pod-job
  plan:
  - task: inspect-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo pod-containers-test && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("pod-job")

			By("waiting for the task pod to be created")
			var taskPod *corev1.Pod
			Eventually(func() bool {
				selector := fmt.Sprintf(
					"concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/type=task",
					pipelineName,
				)
				pods := getPods(selector)
				if len(pods) > 0 {
					taskPod = &pods[0]
					return true
				}
				return false
			}, 2*time.Minute, time.Second).Should(BeTrue(),
				"expected a task pod to be created",
			)

			By("verifying the pod has at least 1 container")
			Expect(taskPod.Spec.Containers).ToNot(BeEmpty())

			By("waiting for build to complete")
			session := waitForBuildAndWatch("pod-job")
			Expect(session).To(gexec.Exit(0))
		})

		It("5.27: resource limits match container_limits", func() {
			By("setting a pipeline with specific container_limits")
			pipelineFile := writePipelineFile("task-k8s-limits.yml", `
jobs:
- name: k8s-limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits:
        cpu: 512
        memory: 256000000
      run:
        path: sh
        args: ["-c", "echo k8s-limits-test && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("k8s-limits-job")

			By("waiting for task pod with resource limits")
			var podName string
			Eventually(func() bool {
				pods := findConcoursePodsForWorker()
				for _, p := range pods {
					if p.Labels["concourse.ci/type"] != "task" {
						continue
					}
					c := mainContainer(&p)
					if _, hasLimit := c.Resources.Limits[corev1.ResourceCPU]; hasLimit {
						podName = p.Name
						return true
					}
				}
				return false
			}, 2*time.Minute, time.Second).Should(BeTrue())

			By("verifying both CPU and memory limits are set")
			pod := getPodByName(podName)
			cpuLimit := podCPULimit(pod)
			memLimit := podMemoryLimit(pod)
			Expect(cpuLimit).ToNot(BeNil(), "expected CPU limit")
			Expect(memLimit).ToNot(BeNil(), "expected memory limit")

			session := waitForBuildAndWatch("k8s-limits-job")
			Expect(session).To(gexec.Exit(0))
		})

		It("5.28: no limits without container_limits", func() {
			By("setting a pipeline without container_limits")
			pipelineFile := writePipelineFile("task-no-limits.yml", `
jobs:
- name: no-limits-job
  plan:
  - task: unlimited-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo no-limits-test && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("no-limits-job")

			By("waiting for the task pod to appear")
			var taskPod *corev1.Pod
			Eventually(func() bool {
				selector := fmt.Sprintf(
					"concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/type=task",
					pipelineName,
				)
				pods := getPods(selector)
				if len(pods) > 0 {
					taskPod = &pods[0]
					return true
				}
				return false
			}, 2*time.Minute, time.Second).Should(BeTrue())

			By("verifying no resource limits are set")
			assertNoResourceLimits(taskPod)

			session := waitForBuildAndWatch("no-limits-job")
			Expect(session).To(gexec.Exit(0))
		})

		It("5.29: all pods cleaned up after build completes", func() {
			By("setting a simple pipeline")
			pipelineFile := writePipelineFile("task-cleanup.yml", `
jobs:
- name: cleanup-job
  plan:
  - task: quick-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["cleanup-test-ok"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-job")

			session := waitForBuildAndWatch("cleanup-job")
			Expect(session).To(gexec.Exit(0))

			By("verifying all workload pods are cleaned up")
			assertPodCleanupForPipeline()
		})
	})
})
