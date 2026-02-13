package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Pod Cleanup", func() {
	It("cleans up task pods after a successful build", func() {
		pipelineFile := writePipelineFile("cleanup-success.yml", `
jobs:
- name: success-job
  plan:
  - task: simple-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["cleanup-success-test"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("success-job")

		session := waitForBuildAndWatch("success-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("cleanup-success-test"))

		By("verifying all workload pods are cleaned up within 3 minutes")
		waitForPodCleanupByPipeline()
	})

	It("cleans up task pods after a failed build", func() {
		pipelineFile := writePipelineFile("cleanup-failure.yml", `
jobs:
- name: fail-job
  plan:
  - task: failing-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo failing-now && exit 1"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("fail-job")

		session := waitForBuildAndWatch("fail-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("failing-now"))

		By("verifying pods are cleaned up even after failure")
		waitForPodCleanupByPipeline()
	})

	It("cleans up pods after an aborted build", func() {
		pipelineFile := writePipelineFile("cleanup-abort.yml", `
jobs:
- name: abort-job
  plan:
  - task: long-running
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo abort-started && sleep 3600"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("abort-job")

		By("waiting for the task to start")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		By("aborting the build")
		fly.Run("abort-build", "-j", inPipeline("abort-job"), "-b", "1")

		By("verifying the build is aborted")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))

		By("verifying pods are cleaned up after abort")
		waitForPodCleanupByPipeline()
	})

	It("cleans up check pods after resource check completes", func() {
		pipelineFile := writePipelineFile("cleanup-check.yml", `
resources:
- name: checked-resource
  type: mock
  source:
    create_files:
      data.txt: "check-data"

jobs:
- name: check-job
  plan:
  - get: checked-resource
    trigger: false
  - task: read-it
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: checked-resource
      run:
        path: echo
        args: ["check-cleanup-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("triggering a resource check")
		newMockVersion("checked-resource", "v1")

		By("running a job that uses the resource")
		triggerJob("check-job")
		session := waitForBuildAndWatch("check-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying all pods (including check pods) are cleaned up")
		waitForPodCleanupByPipeline()
	})

	It("cleans up get, task, and put pods after a multi-step job", func() {
		pipelineFile := writePipelineFile("cleanup-multi-step.yml", `
resources:
- name: src
  type: mock
  source:
    create_files:
      input.txt: "multi-step-data"
- name: dest
  type: mock
  source: {}

jobs:
- name: multi-step-job
  plan:
  - get: src
    trigger: false
  - task: process
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: src
      outputs:
      - name: result
      run:
        path: sh
        args:
        - -c
        - |
          cat src/input.txt > result/output.txt
          echo "multi-step-processed"
  - put: dest
    params:
      version: multi-v1
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("src", "v1")
		triggerJob("multi-step-job")

		session := waitForBuildAndWatch("multi-step-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("multi-step-processed"))

		By("verifying all step pods (get + task + put) are cleaned up")
		waitForPodCleanupByPipeline()
	})

	It("cleans up pods after parallel step completion", func() {
		pipelineFile := writePipelineFile("cleanup-parallel.yml", `
jobs:
- name: parallel-job
  plan:
  - in_parallel:
    - task: task-a
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-a-done"]
    - task: task-b
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-b-done"]
    - task: task-c
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-c-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("parallel-job")

		session := waitForBuildAndWatch("parallel-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying all 3 parallel task pods are cleaned up")
		waitForPodCleanupByPipeline()
	})

	It("does not accumulate pods from consecutive builds", func() {
		pipelineFile := writePipelineFile("cleanup-consecutive.yml", `
jobs:
- name: repeat-job
  plan:
  - task: quick-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["build-done"]
`)
		setAndUnpausePipeline(pipelineFile)

		for i := 1; i <= 3; i++ {
			By(fmt.Sprintf("triggering build %d", i))
			triggerJob("repeat-job")
			session := waitForBuildAndWatch("repeat-job", fmt.Sprintf("%d", i))
			Expect(session).To(gexec.Exit(0))
		}

		By("verifying no pods accumulated after 3 consecutive builds")
		waitForPodCleanupByPipeline()
	})

	It("cleans up pods after pipeline destruction", func() {
		pipelineFile := writePipelineFile("cleanup-destroy.yml", `
jobs:
- name: destroy-job
  plan:
  - task: run-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["about-to-be-destroyed"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("destroy-job")

		session := waitForBuildAndWatch("destroy-job")
		Expect(session).To(gexec.Exit(0))

		By("destroying the pipeline")
		fly.Run("destroy-pipeline", "-n", "-p", pipelineName)

		By("verifying pods are cleaned up after pipeline destruction")
		waitForPodCleanupByPipeline()
	})
})
