package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Smoke", func() {
	It("runs a simple task pipeline end-to-end", func() {
		By("setting a pipeline with a single task job")
		pipelineFile := writePipelineFile("smoke-pipeline.yml", `
jobs:
- name: smoke-job
  plan:
  - task: say-hello
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: echo
        args: ["hello from k8s integration test"]
`)

		setAndUnpausePipeline(pipelineFile)

		By("triggering the job")
		triggerJob("smoke-job")

		By("watching the build and verifying output")
		session := waitForBuildAndWatch("smoke-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say("hello from k8s integration test"))

		By("verifying build status via fly builds")
		builds := flyTable("builds", "-j", inPipeline("smoke-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).To(Equal("succeeded"))
	})
})
