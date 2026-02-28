package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Hijack", func() {
	// Pending: fly intercept on K8s workers triggers a "create pause pod:
	// empty image for resource type" error. This is a K8s runtime limitation
	// where the intercept path can't resolve the resource type image URI.
	PIt("intercepts a running task and executes a command", func() {
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

		By("waiting for the task pod to be running")
		waitForPodWithLabel(
			fmt.Sprintf("concourse.ci/pipeline=%s,concourse.ci/type=task", pipelineName),
			corev1.PodRunning,
		)

		// Give the container a moment to be fully ready for exec
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
