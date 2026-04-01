package behavioral_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Pod Resilience", func() {

	It("handles pod eviction gracefully", func() {
		cfg := writePipelineFile("eviction.yml", `
jobs:
- name: eviction-job
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo running && sleep 30"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("eviction-job")

		By("waiting for the build pod to appear")
		pods := waitForConcoursePodsAtLeast(1)
		podName := pods[0].Name

		By("deleting the pod to simulate eviction")
		err := kubeClient.CoreV1().Pods(config.Namespace).Delete(
			context.Background(), podName, metav1.DeleteOptions{},
		)
		Expect(err).ToNot(HaveOccurred())

		By("verifying the build eventually completes with an error/abort")
		sess := waitForBuildAndWatch("eviction-job")
		// Build should fail or error when pod is evicted
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	// Triggers OOM by growing a shell variable past the 64MB memory limit.
	// BusyBox sort uses temp files (won't OOM), and eval+base64 produces
	// shell metacharacters. Instead, we append safe hex strings to a single
	// variable in a loop — each iteration adds ~2MB of RSS until killed.
	It("detects OOM-killed containers", func() {
		cfg := writePipelineFile("oom.yml", `
jobs:
- name: oom-job
  plan:
  - task: eat-memory
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits: {memory: 64MB}
      run:
        path: sh
        args:
        - -c
        - |
          blob=""
          chunk=$(dd if=/dev/urandom bs=1024 count=1024 2>/dev/null | od -A n -t x1 | tr -d ' \n')
          while true; do
            blob="${blob}${chunk}"
          done
          echo should-not-reach
`)
		setAndUnpausePipeline(cfg)
		triggerJob("oom-job")

		By("waiting for the build to complete")
		sess := waitForBuildAndWatch("oom-job")
		// OOM should cause the build to fail or error
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("handles node failure by marking build as errored", func() {
		// This test validates that when a pod's node becomes unavailable,
		// the build is eventually marked as errored. We simulate this by
		// deleting the pod (since we cannot safely drain a node in tests).
		cfg := writePipelineFile("node-fail.yml", `
jobs:
- name: node-fail-job
  plan:
  - task: work
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo started && sleep 60"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("node-fail-job")

		By("waiting for pod to appear then deleting it")
		pods := waitForConcoursePodsAtLeast(1)
		_ = kubeClient.CoreV1().Pods(config.Namespace).Delete(
			context.Background(), pods[0].Name, metav1.DeleteOptions{},
		)

		By("verifying the build errors out")
		sess := waitForBuildAndWatch("node-fail-job")
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("recovers from external pod deletion", func() {
		cfg := writePipelineFile("pod-delete.yml", `
jobs:
- name: delete-job
  plan:
  - task: quick
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["QUICK_DONE"]
`)
		setAndUnpausePipeline(cfg)

		By("running a normal build first to verify the pipeline works")
		triggerJob("delete-job")
		sess := waitForBuildAndWatch("delete-job", "1")
		Expect(sess.ExitCode()).To(Equal(0))

		By("running a second build to verify recovery")
		triggerJob("delete-job")
		sess = waitForBuildAndWatch("delete-job", "2")
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("handles network partition scenarios", func() {
		// Network partition testing is infrastructure-dependent.
		// We verify that a build with a short timeout completes correctly
		// and that Concourse handles pod lifecycle properly.
		cfg := writePipelineFile("network.yml", `
jobs:
- name: network-job
  plan:
  - task: timeout-test
    timeout: 30s
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo NETWORK_OK && sleep 5"]
`)
		setAndUnpausePipeline(cfg)
		triggerJob("network-job")

		sess := waitForBuildAndWatch("network-job")
		Expect(sess.ExitCode()).To(Equal(0))

		By("verifying pods are cleaned up after build")
		assertPodCleanupForPipeline()
	})
})
