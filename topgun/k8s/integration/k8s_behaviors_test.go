package integration_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("K8s-Specific Behaviors", func() {
	It("shows a k8s-backed worker via fly workers", func() {
		workers := flyTable("workers")
		Expect(workers).ToNot(BeEmpty())

		var found bool
		for _, w := range workers {
			if strings.Contains(w["name"], "k8s") {
				found = true
				Expect(w["platform"]).To(Equal("linux"))
				Expect(w["state"]).To(Equal("running"))
				break
			}
		}
		Expect(found).To(BeTrue(), "expected to find a k8s-backed worker")
	})

	It("applies resource limits to task pods", func() {
		pipelineFile := writePipelineFile("limits-pipeline.yml", `
jobs:
- name: limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      container_limits:
        cpu: 512
        memory: 256000000
      run:
        path: sh
        args: ["-c", "echo started && sleep 30"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("limits-job")

		By("waiting for the build to start and finding the task pod with limits")
		var podName string
		Eventually(func() bool {
			found := findConcoursePodsForWorker()
			for _, p := range found {
				if p.Labels["concourse.ci/type"] != "task" {
					continue
				}
				c := mainContainer(&p)
				if _, hasLimit := c.Resources.Limits[corev1.ResourceCPU]; hasLimit {
					podName = p.Name
					return true
				}
			}
			return false
		}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "expected a task pod with CPU limits")

		By("inspecting the pod's resource limits")
		pod := getPodByName(podName)
		cpuLimit := podCPULimit(pod)
		memLimit := podMemoryLimit(pod)

		Expect(cpuLimit).ToNot(BeNil(), "expected CPU limit to be set")
		Expect(memLimit).ToNot(BeNil(), "expected memory limit to be set")

		By("waiting for build to complete")
		session := waitForBuildAndWatch("limits-job")
		Expect(session).To(gexec.Exit(0))
	})

	// Requires pulling from Docker Hub which is subject to rate limiting.
	// Run manually when Docker Hub access is available.
	PIt("reports image pull errors for nonexistent images", func() {
		pipelineFile := writePipelineFile("bad-image.yml", `
jobs:
- name: bad-image-job
  plan:
  - task: bad-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: this-image-definitely-does-not-exist-anywhere-12345
      run:
        path: echo
        args: ["should not reach here"]
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("bad-image-job")

		session := waitForBuildAndWatch("bad-image-job")
		Expect(session.ExitCode()).ToNot(Equal(0))

		builds := flyTable("builds", "-j", inPipeline("bad-image-job"))
		Expect(builds).ToNot(BeEmpty())
		Expect(builds[0]["status"]).ToNot(Equal("succeeded"))
	})

	// Requires pulling from Docker Hub which is subject to rate limiting.
	// Run manually when Docker Hub access is available.
	PIt("runs tasks with the correct image from image_resource", func() {
		pipelineFile := writePipelineFile("image-verify.yml", `
jobs:
- name: image-job
  plan:
  - task: check-image
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: alpine
          tag: "3.19"
      run:
        path: sh
        args:
        - -c
        - cat /etc/alpine-release
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("image-job")

		session := waitForBuildAndWatch("image-job")
		Expect(session).To(gexec.Exit(0))
		Expect(session.Out).To(gbytes.Say(`3\.19`))
	})
})
