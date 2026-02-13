package integration_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Hook Combinations", func() {
	Context("job-level hooks", func() {
		It("runs job-level on_success after all plan steps succeed", func() {
			pipelineFile := writePipelineFile("job-on-success.yml", `
jobs:
- name: job-success-hook
  on_success:
    task: job-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["job-level-on-success-ran"]
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["main-task-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("job-success-hook")

			session := waitForBuildAndWatch("job-success-hook")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("main-task-done"))
			Expect(session.Out).To(gbytes.Say("job-level-on-success-ran"))
		})

		It("runs job-level on_failure when plan fails", func() {
			pipelineFile := writePipelineFile("job-on-failure.yml", `
jobs:
- name: job-failure-hook
  on_failure:
    task: job-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["job-level-on-failure-ran"]
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo main-failing && exit 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("job-failure-hook")

			session := waitForBuildAndWatch("job-failure-hook")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("main-failing"))
			Expect(session.Out).To(gbytes.Say("job-level-on-failure-ran"))
		})

		It("runs job-level on_abort when build is aborted", func() {
			pipelineFile := writePipelineFile("job-on-abort.yml", `
jobs:
- name: job-abort-hook
  on_abort:
    task: job-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["job-level-on-abort-ran"]
  plan:
  - task: long-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo abort-started && sleep 3600"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("job-abort-hook")

			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("job-abort-hook"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

			fly.Run("abort-build", "-j", inPipeline("job-abort-hook"), "-b", "1")

			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("job-abort-hook"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
		})

		It("runs job-level ensure even when plan fails", func() {
			pipelineFile := writePipelineFile("job-ensure.yml", `
jobs:
- name: job-ensure-hook
  ensure:
    task: job-ensure
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["job-level-ensure-ran"]
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo main-failing && exit 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("job-ensure-hook")

			session := waitForBuildAndWatch("job-ensure-hook")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("main-failing"))
			Expect(session.Out).To(gbytes.Say("job-level-ensure-ran"))
		})

		It("runs multiple job-level hooks: on_failure + ensure", func() {
			pipelineFile := writePipelineFile("job-multi-hooks.yml", `
jobs:
- name: job-multi-hooks
  on_failure:
    task: failure-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["multi-on-failure-ran"]
  ensure:
    task: ensure-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["multi-ensure-ran"]
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo multi-main-failing && exit 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("job-multi-hooks")

			session := waitForBuildAndWatch("job-multi-hooks")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("multi-main-failing"))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("multi-on-failure-ran"))
			Expect(output).To(ContainSubstring("multi-ensure-ran"))
		})
	})

	Context("nested hooks", func() {
		It("runs on_failure inside on_success (nested hooks)", func() {
			pipelineFile := writePipelineFile("nested-hooks.yml", `
jobs:
- name: nested-hooks-job
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["nested-main-passed"]
    on_success:
      task: inner-task
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo inner-task-failing && exit 1"]
      on_failure:
        task: inner-failure-hook
        config:
          platform: linux
          rootfs_uri: docker:///busybox
          run:
            path: echo
            args: ["nested-on-failure-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("nested-hooks-job")

			session := waitForBuildAndWatch("nested-hooks-job")
			// Build fails because the on_success hook's inner task fails
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("nested-main-passed"))
			Expect(output).To(ContainSubstring("inner-task-failing"))
			Expect(output).To(ContainSubstring("nested-on-failure-ran"))
		})

		It("runs step-level ensure inside job-level ensure", func() {
			pipelineFile := writePipelineFile("nested-ensure.yml", `
jobs:
- name: nested-ensure-job
  ensure:
    task: job-ensure
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["job-ensure-ran"]
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo main-failing && exit 1"]
    ensure:
      task: step-ensure
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["step-ensure-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("nested-ensure-job")

			session := waitForBuildAndWatch("nested-ensure-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("main-failing"))
			Expect(output).To(ContainSubstring("step-ensure-ran"))
			Expect(output).To(ContainSubstring("job-ensure-ran"))
		})
	})

	Context("hook and modifier combinations", func() {
		It("runs on_failure after all retry attempts exhausted", func() {
			pipelineFile := writePipelineFile("retry-on-failure.yml", `
jobs:
- name: retry-failure-job
  plan:
  - task: always-fail
    attempts: 2
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo retry-attempt && exit 1"]
    on_failure:
      task: failure-after-retry
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["on-failure-after-retry-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("retry-failure-job")

			session := waitForBuildAndWatch("retry-failure-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			Expect(session.Out).To(gbytes.Say("on-failure-after-retry-ran"))
		})

		It("runs ensure after timeout expires", func() {
			pipelineFile := writePipelineFile("timeout-ensure.yml", `
jobs:
- name: timeout-ensure-job
  plan:
  - task: slow-task
    timeout: 10s
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo timeout-task-started && sleep 120"]
    ensure:
      task: cleanup-after-timeout
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["ensure-after-timeout-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("timeout-ensure-job")

			session := waitForBuildAndWatch("timeout-ensure-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("timeout-task-started"))
			Expect(output).To(ContainSubstring("ensure-after-timeout-ran"))
		})

		It("runs on_success only when try suppresses failure", func() {
			pipelineFile := writePipelineFile("try-on-success.yml", `
jobs:
- name: try-success-job
  plan:
  - try:
      task: may-fail
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo try-failing && exit 1"]
  - task: after-try
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["after-try-ran"]
  on_success:
    task: success-hook
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["try-on-success-hook-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("try-success-job")

			session := waitForBuildAndWatch("try-success-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("try-failing"))
			Expect(output).To(ContainSubstring("after-try-ran"))
			Expect(output).To(ContainSubstring("try-on-success-hook-ran"))
		})

		It("does not run on_success when hook itself fails", func() {
			pipelineFile := writePipelineFile("hook-fails.yml", `
jobs:
- name: hook-fails-job
  plan:
  - task: main
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["main-passed"]
    on_success:
      task: bad-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo hook-failing && exit 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("hook-fails-job")

			session := waitForBuildAndWatch("hook-fails-job")
			// Build should fail because on_success hook failed
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("main-passed"))
			Expect(output).To(ContainSubstring("hook-failing"))
		})
	})

	Context("hooks on structural steps", func() {
		It("runs on_failure on a do block when any step fails", func() {
			pipelineFile := writePipelineFile("do-on-failure.yml", `
jobs:
- name: do-failure-job
  plan:
  - do:
    - task: step-1
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["do-step-1-ok"]
    - task: step-2
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: sh
          args: ["-c", "echo do-step-2-failing && exit 1"]
    on_failure:
      task: do-failure-hook
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["do-on-failure-hook-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("do-failure-job")

			session := waitForBuildAndWatch("do-failure-job")
			Expect(session.ExitCode()).ToNot(Equal(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("do-step-1-ok"))
			Expect(output).To(ContainSubstring("do-step-2-failing"))
			Expect(output).To(ContainSubstring("do-on-failure-hook-ran"))
		})

		It("runs ensure on in_parallel regardless of outcome", func() {
			pipelineFile := writePipelineFile("parallel-ensure.yml", `
jobs:
- name: parallel-ensure-job
  plan:
  - in_parallel:
    - task: par-a
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["par-a-done"]
    - task: par-b
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["par-b-done"]
    ensure:
      task: parallel-ensure
      config:
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["parallel-ensure-ran"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("parallel-ensure-job")

			session := waitForBuildAndWatch("parallel-ensure-job")
			Expect(session).To(gexec.Exit(0))
			output := string(session.Out.Contents())
			Expect(output).To(ContainSubstring("par-a-done"))
			Expect(output).To(ContainSubstring("par-b-done"))
			Expect(output).To(ContainSubstring("parallel-ensure-ran"))
		})
	})
})
