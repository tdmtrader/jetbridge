package integration_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests reproduce the production failure where a downstream step
// attempts to StreamOut an artifact after the producing pod has been reaped,
// surfacing as `exec stream: pods "..." not found`.
//
// Scenarios covered:
//   1. File-based task config (`task: ... file: artifact/task-input.yaml`)
//      where the producing get step's pod is deleted before the task step
//      begins its config fetch.
//   2. Cross-step input consumption where a later task depends on an
//      artifact produced by an earlier step, with an intermediate task
//      that does not reference that artifact. The producer's pod is
//      deleted during the intermediate step.
//
// Both scenarios should succeed when all artifact reads resolve through
// the DaemonSet artifact cache (the fix) regardless of the producing
// pod's lifecycle. They are expected to fail today when the artifact
// read path still resolves to a DeferredVolume pointing at the reaped
// pod.
var _ = Describe("Artifact Read After Producer Pod Reap", func() {
	// deleteProducerPod finds the pod matching the given Concourse labels
	// and force-deletes it. Uses a zero grace period so the delete is
	// observable to downstream steps quickly.
	deleteProducerPod := func(pipeline, job, build, stepType string) {
		selector := fmt.Sprintf(
			"concourse.ci/pipeline=%s,concourse.ci/job=%s,concourse.ci/build=%s,concourse.ci/type=%s",
			pipeline, job, build, stepType,
		)
		By(fmt.Sprintf("locating producer pod with selector %q", selector))
		var podName string
		Eventually(func() string {
			pods := getPods(selector)
			if len(pods) == 0 {
				return ""
			}
			podName = pods[0].Name
			return podName
		}, 3*time.Minute, 2*time.Second).ShouldNot(BeEmpty(),
			fmt.Sprintf("expected producer pod for step type %q to exist", stepType),
		)

		By(fmt.Sprintf("force-deleting producer pod %q", podName))
		grace := int64(0)
		err := kubeClient.CoreV1().Pods(config.Namespace).Delete(
			context.Background(),
			podName,
			metav1.DeleteOptions{GracePeriodSeconds: &grace},
		)
		Expect(err).ToNot(HaveOccurred())

		By(fmt.Sprintf("waiting for pod %q to be fully removed from the cluster", podName))
		Eventually(func() bool {
			_, err := kubeClient.CoreV1().Pods(config.Namespace).Get(
				context.Background(),
				podName,
				metav1.GetOptions{},
			)
			return err != nil
		}, 1*time.Minute, time.Second).Should(BeTrue(),
			fmt.Sprintf("expected pod %q to be deleted", podName),
		)
	}

	It("loads a file-based task config even after the producing get pod has been reaped", func() {
		// The pipeline: a get step produces an artifact containing a
		// task YAML file, an intermediate task waits long enough for us
		// to delete the get pod, then a file-config task loads its
		// config from the reaped get step's artifact.
		//
		// With the DaemonSet artifact cache in place, the third task's
		// config fetch should resolve via HTTP to the DaemonSet and
		// succeed. If the fetch resolves to a DeferredVolume pointing
		// at the deleted get pod, it fails with
		// `exec stream: pods "..." not found`.
		pipelineFile := writePipelineFile("file-config-after-reap.yml", `
resources:
- name: task-source
  type: mock
  source:
    create_files:
      tasks/after-reap.yml: |
        platform: linux
        rootfs_uri: docker:///busybox
        run:
          path: echo
          args: ["file-config-after-reap-done"]

jobs:
- name: file-config-after-reap
  plan:
  - get: task-source
    trigger: false
  - task: hold
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo hold-started && sleep 45 && echo hold-done"]
  - task: from-reaped-artifact
    file: task-source/tasks/after-reap.yml
`)
		setAndUnpausePipeline(pipelineFile)
		newMockVersion("task-source", "v1")
		triggerJob("file-config-after-reap")

		// Delete the get step's pod while the hold task is running.
		deleteProducerPod(pipelineName, "file-config-after-reap", "1", "get")

		session := waitForBuildAndWatch("file-config-after-reap")
		Expect(session).To(gexec.Exit(0),
			"expected build to succeed; if this fails with `exec stream: pods ... not found`, "+
				"the file-config fetch is still resolving through the reaped pod instead of the DaemonSet",
		)
		Expect(session.Out).To(gbytes.Say("file-config-after-reap-done"))
	})

	It("materializes a cross-step input even after the producing task pod has been reaped", func() {
		// Three-task pipeline:
		//   producer: writes data into an output artifact (`payload`).
		//   bystander: runs without referencing `payload`, giving us a
		//     deterministic window in which to delete the producer pod.
		//   consumer: takes `payload` as an input and reads its contents.
		//
		// With DaemonSet-backed artifact reads, the consumer should
		// fetch `payload` via the DaemonSet regardless of whether the
		// producer pod still exists. If any input-materialization code
		// path still execs into the producer pod, this will fail with
		// `exec stream: pods "..." not found`.
		pipelineFile := writePipelineFile("cross-step-after-reap.yml", `
jobs:
- name: cross-step-after-reap
  plan:
  - task: producer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      outputs:
      - name: payload
      run:
        path: sh
        args:
        - -c
        - |
          echo "cross-step-payload-marker" > payload/data.txt
          echo "producer-done"
  - task: bystander
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      run:
        path: sh
        args: ["-c", "echo bystander-started && sleep 45 && echo bystander-done"]
  - task: consumer
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: payload
      run:
        path: sh
        args:
        - -c
        - |
          cat payload/data.txt
          echo "consumer-done"
`)
		setAndUnpausePipeline(pipelineFile)
		triggerJob("cross-step-after-reap")

		// Delete the producer task pod while the bystander is running.
		deleteProducerPod(pipelineName, "cross-step-after-reap", "1", "task")

		session := waitForBuildAndWatch("cross-step-after-reap")
		Expect(session).To(gexec.Exit(0),
			"expected build to succeed; if this fails with `exec stream: pods ... not found`, "+
				"the consumer's input materialization is still routed through the reaped producer pod",
		)
		Expect(session.Out).To(gbytes.Say("cross-step-payload-marker"))
		Expect(session.Out).To(gbytes.Say("consumer-done"))
	})
})
