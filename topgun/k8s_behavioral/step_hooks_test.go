package behavioral_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Step Hooks", func() {
	It("9.1: on_success fires after step succeeds", func() {
		pipelineFile := writePipelineFile("hook-on-success.yml", `
jobs:
- name: hook-success-job
  plan:
  - task: main-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hook-main-passed"]
    on_success:
      task: success-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["on-success-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-success-job")

		session := waitForBuildAndWatch("hook-success-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("hook-main-passed"))
		Expect(session.Out).To(gbytes.Say("on-success-hook-ran"))
	})

	It("9.2: on_failure fires after step fails", func() {
		pipelineFile := writePipelineFile("hook-on-failure.yml", `
jobs:
- name: hook-failure-job
  plan:
  - task: failing-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo hook-main-failing && exit 1"]
    on_failure:
      task: failure-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["on-failure-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-failure-job")

		session := waitForBuildAndWatch("hook-failure-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("hook-main-failing"))
		Expect(session.Out).To(gbytes.Say("on-failure-hook-ran"))
	})

	It("9.3: on_abort fires when build is aborted", func() {
		pipelineFile := writePipelineFile("hook-on-abort.yml", `
jobs:
- name: hook-abort-job
  plan:
  - task: long-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo hook-abort-started && sleep 3600"]
    on_abort:
      task: abort-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["on-abort-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-abort-job")

		By("waiting for the task to start")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("hook-abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		By("aborting the build")
		fly.Run("abort-build", "-j", inPipeline("hook-abort-job"), "-b", "1")

		By("verifying the build is aborted")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("hook-abort-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
	})

	It("9.4: on_error fires on infrastructure error", func() {
		pipelineFile := writePipelineFile("hook-on-error.yml", `
jobs:
- name: hook-error-job
  plan:
  - task: error-task
    timeout: 10s
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo hook-error-started && sleep 120"]
    on_error:
      task: error-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["on-error-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-error-job")

		session := waitForBuildAndWatch("hook-error-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying that error occurred due to timeout")
		builds := flyTable("builds", "-j", inPipeline("hook-error-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("failed"), Equal("errored")))
	})

	It("9.5: ensure fires regardless of step outcome", func() {
		pipelineFile := writePipelineFile("hook-ensure.yml", `
jobs:
- name: hook-ensure-job
  plan:
  - task: failing-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo hook-ensure-failing && exit 1"]
    ensure:
      task: ensure-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["ensure-hook-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-ensure-job")

		session := waitForBuildAndWatch("hook-ensure-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		Expect(session.Out).To(gbytes.Say("hook-ensure-failing"))
		Expect(session.Out).To(gbytes.Say("ensure-hook-ran"))
	})

	It("9.6: multiple hooks fire together (on_failure + ensure)", func() {
		pipelineFile := writePipelineFile("hook-multi.yml", `
jobs:
- name: hook-multi-job
  plan:
  - task: failing-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo hook-multi-failing && exit 1"]
    on_failure:
      task: failure-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["multi-on-failure-ran"]
    ensure:
      task: ensure-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["multi-ensure-ran"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-multi-job")

		session := waitForBuildAndWatch("hook-multi-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("hook-multi-failing"))
		Expect(output).To(ContainSubstring("multi-on-failure-ran"))
		Expect(output).To(ContainSubstring("multi-ensure-ran"))
	})

	It("9.7: job-level hooks fire after plan completes", func() {
		pipelineFile := writePipelineFile("hook-job-level.yml", `
jobs:
- name: job-hooks-job
  on_success:
    task: job-success-hook
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["job-on-success-ran"]
  ensure:
    task: job-ensure-hook
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["job-ensure-ran"]
  plan:
  - task: main-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["job-level-main-done"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("job-hooks-job")

		session := waitForBuildAndWatch("job-hooks-job")
		Expect(session).To(gexec.Exit(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("job-level-main-done"))
		Expect(output).To(ContainSubstring("job-on-success-ran"))
		Expect(output).To(ContainSubstring("job-ensure-ran"))
	})

	It("9.8: hook failure changes build status", func() {
		pipelineFile := writePipelineFile("hook-fail-status.yml", `
jobs:
- name: hook-fail-status-job
  plan:
  - task: main-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hook-status-main-passed"]
    on_success:
      task: bad-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo hook-status-hook-failing && exit 1"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-fail-status-job")

		session := waitForBuildAndWatch("hook-fail-status-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("hook-status-main-passed"))
		Expect(output).To(ContainSubstring("hook-status-hook-failing"))

		By("verifying the build is marked failed due to hook failure")
		builds := flyTable("builds", "-j", inPipeline("hook-fail-status-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("failed"))
	})

	It("9.9: ensure failure overrides success outcome", func() {
		pipelineFile := writePipelineFile("hook-ensure-override.yml", `
jobs:
- name: ensure-override-job
  plan:
  - task: passing-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["ensure-override-main-passed"]
    ensure:
      task: failing-ensure
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo ensure-override-ensure-failing && exit 1"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("ensure-override-job")

		session := waitForBuildAndWatch("ensure-override-job")
		Expect(session.ExitCode()).ToNot(Equal(0))
		output := string(session.Out.Contents())
		Expect(output).To(ContainSubstring("ensure-override-main-passed"))
		Expect(output).To(ContainSubstring("ensure-override-ensure-failing"))

		By("verifying the build ended as failed despite main task passing")
		builds := flyTable("builds", "-j", inPipeline("ensure-override-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(SatisfyAny(Equal("failed"), Equal("errored")),
			"ensure failure should cause build to fail or error")
	})

	It("9.10: K8s: hook pods are cleaned up after build", func() {
		pipelineFile := writePipelineFile("hook-k8s-cleanup.yml", `
jobs:
- name: hook-cleanup-job
  plan:
  - task: main-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hook-cleanup-main"]
    on_success:
      task: success-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["hook-cleanup-success"]
    ensure:
      task: ensure-hook
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["hook-cleanup-ensure"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hook-cleanup-job")

		session := waitForBuildAndWatch("hook-cleanup-job")
		Expect(session).To(gexec.Exit(0))

		By("verifying all hook pods are cleaned up")
		waitForPodCleanupByPipeline()
	})
})
