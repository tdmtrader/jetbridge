package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("Skip Image Resource Download", func() {
	// When a get step targets a registry-image type resource, the physical
	// download is skipped and kubelet pulls the image natively. No get pod
	// is created.
	//
	// The fetch_artifact param forces the full download path, creating a
	// get pod and making the artifact volume available to downstream steps.

	Context("with native registry-image resources", func() {
		// These tests require Docker Hub API access for registry-image checks.
		// Run with: ginkgo --label-filter="e2e" -focus="Skip Image Resource Download"

		PIt("skips get pod when passed between jobs and used as task image", Label("e2e"), func() {
			pipelineFile := writePipelineFile("skip-get-registry-image.yml", `
resources:
- name: alpine-image
  type: registry-image
  source:
    repository: alpine
    tag: "3.19"

jobs:
- name: upstream-job
  plan:
  - get: alpine-image

- name: downstream-job
  plan:
  - get: alpine-image
    passed: [upstream-job]
  - task: use-image
    image: alpine-image
    config:
      platform: linux
      run:
        path: sh
        args:
        - -c
        - |
          cat /etc/alpine-release
          echo "registry-image-short-circuit-verified"
`)
			setAndUnpausePipeline(pipelineFile)

			By("waiting for the resource check to discover alpine versions")
			Eventually(func() int {
				return len(flyTable("resource-versions", "-r", inPipeline("alpine-image")))
			}, 3*time.Minute, 5*time.Second).Should(BeNumerically(">", 0))

			By("triggering upstream job")
			triggerJob("upstream-job")
			session := waitForBuildAndWatch("upstream-job")
			Expect(session).To(gexec.Exit(0))

			By("triggering downstream job with passed constraint")
			triggerJob("downstream-job")
			session = waitForBuildAndWatch("downstream-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("registry-image-short-circuit-verified"))

			By("verifying no get pods were created")
			pods := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(pods).To(BeEmpty(),
				"expected no get pods — registry-image type should short-circuit",
			)
		})
	})
})
