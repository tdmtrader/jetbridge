package integration_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Hijack", func() {
	It("intercepts a running task and executes a command", func() {
		pipelineFile := writePipelineFile("hijack-pipeline.yml", `
jobs:
- name: hijack-job
  plan:
  - task: long-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo task-started && sleep 120"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("hijack-job")

		By("waiting for the task to start running")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline("hijack-job"))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

		// Give the pod a moment to be fully ready for exec
		time.Sleep(5 * time.Second)

		By("intercepting into the running task")
		session := fly.Start(
			"intercept",
			"-j", inPipeline("hijack-job"),
			"-s", "long-task",
			"--", "echo", "hijack-works",
		)
		Eventually(session, 1*time.Minute).Should(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("hijack-works"))

		By("aborting the build to clean up")
		fly.Run("abort-build", "-j", inPipeline("hijack-job"), "-b", "1")
	})
})
