package integration_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Build Lifecycle", func() {
	It("reports succeeded and failed build status correctly", func() {
		pipelineFile := writePipelineFile("status-pipeline.yml", `
jobs:
- name: success-job
  plan:
  - task: pass
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["pass"]

- name: failure-job
  plan:
  - task: fail
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "exit 1"]
`)
		setAndUnpausePipeline(pipelineFile)

		By("running a successful build")
		triggerJob("success-job")
		session := waitForBuildAndWatch("success-job")
		Expect(session).To(gexec.Exit(0))

		By("running a failed build")
		triggerJob("failure-job")
		session = waitForBuildAndWatch("failure-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		By("verifying build statuses via fly builds")
		successBuilds := flyTable("builds", "-j", inPipeline("success-job"))
		Expect(successBuilds).ToNot(BeEmpty())
		Expect(successBuilds[0]["status"]).To(Equal("succeeded"))

		failureBuilds := flyTable("builds", "-j", inPipeline("failure-job"))
		Expect(failureBuilds).ToNot(BeEmpty())
		Expect(failureBuilds[0]["status"]).To(Equal("failed"))
	})

	It("cleans up when a build is cancelled", func() {
		pipelineFile := writePipelineFile("cancel-pipeline.yml", `
jobs:
- name: long-job
  plan:
  - task: sleep
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo started && sleep 3600"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("long-job")

		By("waiting for the task to start")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("long-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		By("aborting the build")
		fly.Run("abort-build", "-j", inPipeline("long-job"), "-b", "1")

		By("verifying the build status is aborted")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("long-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
	})

	It("streams logs incrementally during task execution", func() {
		pipelineFile := writePipelineFile("log-stream.yml", `
jobs:
- name: log-job
  plan:
  - task: log-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args:
        - -c
        - |
          echo "log-line-1"
          sleep 1
          echo "log-line-2"
          sleep 1
          echo "log-line-3"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("log-job")

		session := waitForBuildAndWatch("log-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("log-line-1"))
		Expect(session.Out).To(gbytes.Say("log-line-2"))
		Expect(session.Out).To(gbytes.Say("log-line-3"))
	})
})
