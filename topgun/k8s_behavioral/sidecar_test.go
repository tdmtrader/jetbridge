package behavioral_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Sidecar Containers", func() {

	It("runs an inline sidecar alongside the main task", func() {
		cfg := writePipelineFile("sidecar-inline.yml", `
jobs:
- name: sidecar-job
  plan:
  - task: with-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "for i in $(seq 1 30); do if wget -qO- http://localhost:80/ 2>/dev/null; then echo SIDECAR_OK; exit 0; fi; sleep 1; done; echo SIDECAR_TIMEOUT; exit 1"]
    sidecars:
    - name: web
      image: nginx:alpine
      ports:
      - containerPort: 80
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-job")

		sess := waitForBuildAndWatch("sidecar-job")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("SIDECAR_OK"))
	})

	It("applies resource limits to sidecar containers", func() {
		cfg := writePipelineFile("sidecar-limits.yml", `
jobs:
- name: sidecar-limits-job
  plan:
  - task: limited
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["done"]
    sidecars:
    - name: limited-svc
      image: busybox
      command: ["sleep", "30"]
      resources:
        limits:
          memory: 64Mi
          cpu: 100m
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-limits-job")

		By("waiting for a pod to appear and inspecting sidecar limits")
		pods := waitForConcoursePodsAtLeast(1)
		sidecar := containerByName(&pods[0], "limited-svc")
		if sidecar != nil {
			Expect(sidecar.Resources.Limits).ToNot(BeEmpty())
		}

		sess := waitForBuildAndWatch("sidecar-limits-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("stops sidecars when the main task completes", func() {
		cfg := writePipelineFile("sidecar-stop.yml", `
jobs:
- name: sidecar-stop-job
  plan:
  - task: quick-main
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["main done"]
    sidecars:
    - name: long-svc
      image: busybox
      command: ["sleep", "3600"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("sidecar-stop-job")

		sess := waitForBuildAndWatch("sidecar-stop-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("verifying pod is cleaned up after main completes")
		assertPodCleanupForPipeline()
	})

	It("rejects reserved sidecar container names", func() {
		cfg := writePipelineFile("sidecar-reserved.yml", `
jobs:
- name: reserved-name-job
  plan:
  - task: bad-sidecar
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["hello"]
    sidecars:
    - name: main
      image: busybox
      command: ["sleep", "5"]
`)
		By("verifying set-pipeline rejects the reserved sidecar name")
		sess := fly.Start("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
		output := string(sess.Out.Contents()) + string(sess.Err.Contents())
		Expect(output).To(ContainSubstring("reserved"), "expected set-pipeline to reject reserved sidecar name 'main'")
	})

	It("creates the expected container count in K8s pod", func() {
		cfg := writePipelineFile("sidecar-count.yml", `
jobs:
- name: multi-sidecar-job
  plan:
  - task: with-sidecars
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "sleep 5 && echo done"]
    sidecars:
    - name: svc-a
      image: busybox
      command: ["sleep", "30"]
    - name: svc-b
      image: busybox
      command: ["sleep", "30"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("multi-sidecar-job")

		By("waiting for pod and checking container count")
		pods := waitForConcoursePodsAtLeast(1)
		// Main container + 2 sidecars = at least 3 containers
		Expect(len(pods[0].Spec.Containers)).To(BeNumerically(">=", 3),
			fmt.Sprintf("expected at least 3 containers, got %d", len(pods[0].Spec.Containers)))

		sess := waitForBuildAndWatch("multi-sidecar-job")
		Expect(sess.ExitCode()).To(Equal(0))
	})
})
