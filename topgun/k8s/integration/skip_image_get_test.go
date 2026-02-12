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
	// When a get step targets a registry-image type resource (or a custom
	// type with produces: registry-image), the physical download is skipped
	// and kubelet pulls the image natively. No get pod is created.
	//
	// The fetch_artifact param forces the full download path, creating a
	// get pod and making the artifact volume available to downstream steps.

	Context("with custom type producing registry-image", func() {
		It("skips get pod when passed between jobs and used as task image", func() {
			pipelineFile := writePipelineFile("skip-get-produces.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: my-image
  type: image-type
  source:
    repository: busybox

jobs:
- name: upstream-job
  plan:
  - get: my-image

- name: downstream-job
  plan:
  - get: my-image
    passed: [upstream-job]
  - task: use-image
    image: my-image
    config:
      platform: linux
      run:
        path: echo
        args: ["produces-short-circuit-verified"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("injecting a resource version")
			newMockVersion("my-image", "v1")

			By("triggering upstream job")
			triggerJob("upstream-job")
			session := waitForBuildAndWatch("upstream-job")
			Expect(session).To(gexec.Exit(0))

			By("triggering downstream job with passed constraint")
			triggerJob("downstream-job")
			session = waitForBuildAndWatch("downstream-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("produces-short-circuit-verified"))

			By("verifying no get pods were created (short-circuit skips pod creation)")
			pods := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(pods).To(BeEmpty(),
				"expected no get pods — produces: registry-image should short-circuit",
			)
		})

		It("spawns get pod when fetch_artifact overrides the short-circuit", func() {
			pipelineFile := writePipelineFile("fetch-artifact-override.yml", `
resource_types:
- name: image-type
  type: mock
  source:
    mirror_self: true
    initial_version: type-v1
  produces: registry-image

resources:
- name: my-image
  type: image-type
  source:
    repository: busybox
    create_files:
      artifact-marker.txt: "artifact-downloaded-successfully"

jobs:
- name: upstream-job
  plan:
  - get: my-image

- name: downstream-job
  plan:
  - get: my-image
    passed: [upstream-job]
    params:
      fetch_artifact: true
  - task: verify-artifact
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: my-image
      run:
        path: cat
        args: ["my-image/artifact-marker.txt"]
`)
			setAndUnpausePipeline(pipelineFile)

			By("injecting a resource version")
			newMockVersion("my-image", "v1")

			By("triggering upstream job (short-circuits)")
			triggerJob("upstream-job")
			session := waitForBuildAndWatch("upstream-job")
			Expect(session).To(gexec.Exit(0))

			By("triggering downstream job with fetch_artifact")
			triggerJob("downstream-job")
			session = waitForBuildAndWatch("downstream-job")
			Expect(session).To(gexec.Exit(0))

			By("verifying the artifact is accessible (proves full get ran)")
			Expect(session.Out).To(gbytes.Say("artifact-downloaded-successfully"))
		})
	})

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
