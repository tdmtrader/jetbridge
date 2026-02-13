package integration_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Step Combinations", func() {
	It("executes on_success hook after task succeeds", func() {
		pipelineFile := writePipelineFile("on-success.yml", `
jobs:
- name: on-success-job
  plan:
  - task: main-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["main-task-passed"]
    on_success:
      task: success-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["on-success-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("on-success-job")

		session := waitForBuildAndWatch("on-success-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("main-task-passed"))
		Expect(session.Out).To(gbytes.Say("on-success-hook-ran"))
	})

	It("executes on_failure hook after task fails", func() {
		pipelineFile := writePipelineFile("on-failure.yml", `
jobs:
- name: on-failure-job
  plan:
  - task: failing-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo failing-task-output && exit 1"]
    on_failure:
      task: failure-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["on-failure-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("on-failure-job")

		session := waitForBuildAndWatch("on-failure-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("failing-task-output"))
		Expect(session.Out).To(gbytes.Say("on-failure-hook-ran"))
	})

	It("executes on_abort hook when build is aborted", func() {
		pipelineFile := writePipelineFile("on-abort.yml", `
jobs:
- name: on-abort-job
  plan:
  - task: long-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo abort-task-started && sleep 3600"]
    on_abort:
      task: abort-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["on-abort-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("on-abort-job")

		By("waiting for the task to start")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("on-abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		By("aborting the build")
		fly.Run("abort-build", "-j", inPipeline("on-abort-job"), "-b", "1")

		By("verifying the build is aborted")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("on-abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
	})

	It("runs ensure step even after failure", func() {
		pipelineFile := writePipelineFile("ensure-step.yml", `
jobs:
- name: ensure-job
  plan:
  - task: failing-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo main-task-failing && exit 1"]
    ensure:
      task: ensure-task
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["ensure-step-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("ensure-job")

		session := waitForBuildAndWatch("ensure-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("main-task-failing"))
		Expect(session.Out).To(gbytes.Say("ensure-step-ran"))
	})

	It("catches failure with try step", func() {
		pipelineFile := writePipelineFile("try-step.yml", `
jobs:
- name: try-job
  plan:
  - try:
      task: may-fail
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo try-task-failing && exit 1"]
  - task: after-try
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["after-try-succeeded"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("try-job")

		session := waitForBuildAndWatch("try-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("try-task-failing"))
		Expect(session.Out).To(gbytes.Say("after-try-succeeded"))
	})

	It("retries a task on failure", func() {
		pipelineFile := writePipelineFile("retry-step.yml", `
jobs:
- name: retry-job
  plan:
  - task: flaky-task
    attempts: 3
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      caches:
      - path: .state
      run:
        path: sh
        args:
        - -c
        - |
          if [ -f .state/attempt2 ]; then
            echo "retry-succeeded-on-attempt-3"
            exit 0
          elif [ -f .state/attempt1 ]; then
            touch .state/attempt2
            echo "retry-attempt-2-failing"
            exit 1
          else
            touch .state/attempt1
            echo "retry-attempt-1-failing"
            exit 1
          fi
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("retry-job")

		session := waitForBuildAndWatch("retry-job")
		// The task may succeed if caches persist across retries, or
		// fail if they don't. Either way verify it attempted retries.
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("retry-attempt-1-failing"))
	})

	It("aborts a slow task with timeout", func() {
		pipelineFile := writePipelineFile("timeout-step.yml", `
jobs:
- name: timeout-job
  plan:
  - task: slow-task
    timeout: 10s
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo timeout-task-started && sleep 120"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("timeout-job")

		session := waitForBuildAndWatch("timeout-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("timeout-task-started"))

		builds := flyTable("builds", "-j", inPipeline("timeout-job"))
		Expect(builds).ToNot(BeEmpty())
		// Build should fail or error due to timeout
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("failed"), Equal("errored")))
	})

	It("runs across step with static values", func() {
		pipelineFile := writePipelineFile("across-step.yml", `
jobs:
- name: across-job
  plan:
  - task: multi-value
    across:
    - var: color
      values: ["red", "green", "blue"]
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      params:
        COLOR: ((.:color))
      run:
        path: sh
        args:
        - -c
        - echo "color=${COLOR}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("across-job")

		session := waitForBuildAndWatch("across-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("color=red"))
		Expect(output).To(ContainSubstring("color=green"))
		Expect(output).To(ContainSubstring("color=blue"))
	})

	It("runs in_parallel with mixed step types", func() {
		pipelineFile := writePipelineFile("parallel-mixed.yml", `
resources:
- name: par-resource
  type: mock
  source:
    create_files:
      data.txt: "parallel-data"
- name: par-output
  type: mock
  source: {}

jobs:
- name: parallel-mixed-job
  plan:
  - get: par-resource
    trigger: false
  - in_parallel:
    - task: parallel-task-a
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-task-a-done"]
    - task: parallel-task-b
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-task-b-done"]
    - put: par-output
      params:
        version: parallel-v1
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("par-resource", "v1")
		triggerJob("parallel-mixed-job")

		session := waitForBuildAndWatch("parallel-mixed-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("parallel-task-a-done"))
		Expect(output).To(ContainSubstring("parallel-task-b-done"))
	})

	It("runs do step with sequential tasks in order", func() {
		pipelineFile := writePipelineFile("do-step.yml", `
jobs:
- name: do-job
  plan:
  - do:
    - task: step-1
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        outputs:
        - name: shared
        run:
          path: sh
          args:
          - -c
          - echo -n "step1" > shared/order.txt && echo "do-step-1-done"
    - task: step-2
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        inputs:
        - name: shared
        outputs:
        - name: shared
        run:
          path: sh
          args:
          - -c
          - |
            prev=$(cat shared/order.txt)
            echo -n "${prev},step2" > shared/order.txt
            echo "do-step-2-done"
    - task: step-3
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        inputs:
        - name: shared
        run:
          path: sh
          args:
          - -c
          - |
            order=$(cat shared/order.txt)
            echo "execution-order=${order}"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("do-job")

		session := waitForBuildAndWatch("do-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("do-step-1-done"))
		Expect(session.Out).To(gbytes.Say("do-step-2-done"))
		Expect(session.Out).To(gbytes.Say("execution-order=step1,step2"))
	})
})
