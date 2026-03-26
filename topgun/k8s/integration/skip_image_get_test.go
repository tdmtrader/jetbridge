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
	//
	// These tests use images already in testDependencyImages (busybox,
	// alpine, mock-resource) which are pre-loaded into KinD. The native
	// resolver still calls the registry API for digest resolution, so
	// outbound HTTP to Docker Hub is required.

	Context("with native registry-image resources", func() {

		It("resolves task image_resource without check/get pods", func() {
			pipelineFile := writePipelineFile("image-resource-resolve.yml", `
jobs:
- name: resolve-job
  plan:
  - task: inline-image
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: busybox
          tag: latest
      run:
        path: sh
        args:
        - -c
        - |
          echo "image-resource-resolved-inline"
`)
			setAndUnpausePipeline(pipelineFile)

			By("triggering job with image_resource")
			triggerJob("resolve-job")
			session := waitForBuildAndWatch("resolve-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("image-resource-resolved-inline"))

			By("verifying no check or get pods were created")
			checkPods := getPods(fmt.Sprintf(
				"concourse.ci/type=check,concourse.ci/pipeline=%s", pipelineName,
			))
			getPodsFound := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(checkPods).To(BeEmpty(), "expected no check pods for image_resource")
			Expect(getPodsFound).To(BeEmpty(), "expected no get pods for image_resource")
		})

		It("skips get pod when passed between jobs and used as task image", func() {
			pipelineFile := writePipelineFile("skip-get-registry-image.yml", `
resources:
- name: alpine-image
  type: registry-image
  source:
    repository: alpine
    tag: latest

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

		It("sidecar with bare string image gets digest-pinned", func() {
			pipelineFile := writePipelineFile("sidecar-digest-pin.yml", `
jobs:
- name: sidecar-job
  plan:
  - task: with-sidecar
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: busybox
          tag: latest
      run:
        path: sh
        args:
        - -c
        - |
          echo "sidecar-task-completed"
    sidecars:
    - name: helper
      image: alpine:latest
`)
			setAndUnpausePipeline(pipelineFile)

			By("triggering job with sidecar")
			triggerJob("sidecar-job")
			session := waitForBuildAndWatch("sidecar-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("sidecar-task-completed"))

			By("verifying the sidecar container image is digest-pinned")
			taskPods := getPods(fmt.Sprintf(
				"concourse.ci/type=task,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(taskPods).To(HaveLen(1))
			sidecarFound := false
			for _, c := range taskPods[0].Spec.Containers {
				if c.Name == "helper" {
					sidecarFound = true
					Expect(c.Image).To(ContainSubstring("@sha256:"),
						"sidecar image should be digest-pinned")
				}
			}
			Expect(sidecarFound).To(BeTrue(), "helper sidecar container not found")
		})

		It("sidecar using image_artifact resolves from registry-image get step", func() {
			pipelineFile := writePipelineFile("sidecar-image-artifact.yml", `
resources:
- name: sidecar-image
  type: registry-image
  source:
    repository: busybox
    tag: latest

jobs:
- name: artifact-sidecar-job
  plan:
  - get: sidecar-image
  - task: with-artifact-sidecar
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: alpine
          tag: latest
      run:
        path: sh
        args:
        - -c
        - |
          echo "image-artifact-sidecar-verified"
    sidecars:
    - name: from-artifact
      image_artifact: sidecar-image
`)
			setAndUnpausePipeline(pipelineFile)

			By("waiting for the resource check to discover busybox versions")
			Eventually(func() int {
				return len(flyTable("resource-versions", "-r", inPipeline("sidecar-image")))
			}, 3*time.Minute, 5*time.Second).Should(BeNumerically(">", 0))

			By("triggering job")
			triggerJob("artifact-sidecar-job")
			session := waitForBuildAndWatch("artifact-sidecar-job")
			Expect(session).To(gexec.Exit(0))
			Expect(session.Out).To(gbytes.Say("image-artifact-sidecar-verified"))

			By("verifying sidecar used the artifact image (digest-pinned)")
			taskPods := getPods(fmt.Sprintf(
				"concourse.ci/type=task,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(taskPods).To(HaveLen(1))
			sidecarFound := false
			for _, c := range taskPods[0].Spec.Containers {
				if c.Name == "from-artifact" {
					sidecarFound = true
					Expect(c.Image).To(ContainSubstring("busybox"),
						"sidecar should use the busybox image from artifact")
					Expect(c.Image).To(ContainSubstring("@sha256:"),
						"sidecar image should be digest-pinned via RegisterImageRef")
				}
			}
			Expect(sidecarFound).To(BeTrue(), "from-artifact sidecar container not found")

			By("verifying no get pods were created (short-circuit path)")
			getPodsFound := getPods(fmt.Sprintf(
				"concourse.ci/type=get,concourse.ci/pipeline=%s", pipelineName,
			))
			Expect(getPodsFound).To(BeEmpty(),
				"expected no get pods — registry-image should short-circuit",
			)
		})
	})
})
