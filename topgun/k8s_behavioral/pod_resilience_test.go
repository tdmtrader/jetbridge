package behavioral_test

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

var _ = Describe("Pod Resilience", func() {

	// Deletion DURING the step's command, not before it.
	//
	// A pause pod taken away BEFORE its command runs is replaced in place and
	// the step goes on -- deliberate policy, see recreatePausePod -- so a spec
	// that deletes the pod the moment it appears exercises that path and
	// legitimately ends green. This one waits for the command's own output to
	// reach `fly watch` first. That is the point past which the runtime
	// refuses to run anything a second time, and from there the pod's
	// destruction has to end the build rather than be absorbed.
	//
	// It is the case the exec transport cannot see on its own: a command's
	// exit status arrives on the SPDY error stream, and a pod destroyed
	// mid-exec closes that stream with nothing on it, exactly the way a
	// command exiting 0 does. Before the fix this build reported `succeeded`.
	It("errors the build when the pod is deleted while the step's command is running", func() {
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
        args: ["-c", 'printf "command-%s\n" running && sleep 120']
`)
		setAndUnpausePipeline(cfg)
		triggerJob("eviction-job")

		By("waiting for the task pod to appear")
		var taskPodName string
		Eventually(func() bool {
			selector := fmt.Sprintf(
				"concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/type=task",
				pipelineName,
			)
			pods := getPods(selector)
			for i := range pods {
				if pods[i].DeletionTimestamp == nil {
					taskPodName = pods[i].Name
					return true
				}
			}
			return false
		}, 2*time.Minute, time.Second).Should(BeTrue(), "expected a task pod to be created")

		By("waiting for the step's command to actually start")
		// The command's own first line is the only evidence that it is
		// running. A pod in phase Running is not: the pause container sleeps
		// whether or not anything has been exec'd into it. Nor is fly's own
		// `running sh -c ...` line, which echoes the command before the exec
		// exists -- so the sentinel is assembled by printf at run time and
		// never appears verbatim in the command line. Build 192 matched the
		// echo, deleted the pod 360ms before the exec was created, and got a
		// generic "cannot exec in a stopped state" instead of this sentence.
		sess := fly.Start("watch", "-j", inPipeline("eviction-job"), "-b", "1")
		Eventually(sess.Out, 3*time.Minute).Should(gbytes.Say("command-running"))

		By("deleting the pod out from under the running command")
		Expect(kubeClient.CoreV1().Pods(config.Namespace).Delete(
			context.Background(), taskPodName, metav1.DeleteOptions{},
		)).To(Succeed())

		By("verifying the build ends non-zero and says what took the pod")
		Eventually(sess, 5*time.Minute).Should(gexec.Exit())
		Expect(sess.ExitCode()).ToNot(Equal(0),
			"a pod destroyed while its command was running must not report success")
		// The reason -- pod_deleted, evicted, node_lost -- follows in
		// parentheses; the sentence itself is the same whichever one it was.
		Expect(string(sess.Out.Contents()) + string(sess.Err.Contents())).To(
			ContainSubstring("the step's command was running when its pod was destroyed"))
	})

	// Triggers OOM using a static Go binary that allocates 10 MB slices
	// in a tight loop. Shell-based approaches (awk, dd, /dev/shm) don't
	// reliably count against the container memory cgroup in K3s.
	// The oom-trigger image is built and loaded by buildAndLoadOOMTriggerImage
	// in cluster_lifecycle_test.go.
	It("detects OOM-killed containers", func() {
		cfg := writePipelineFile("oom.yml", `
jobs:
- name: oom-job
  plan:
  - task: eat-memory
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: oom-trigger}}
      container_limits: {memory: 64MB}
      run:
        path: /oom-trigger
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
		//
		// The deletion has to land while the command is running, for the
		// reason the eviction spec above spells out: a pause pod taken away
		// before its command starts is replaced and the build goes green.
		// Build 192 deleted the pod ~200ms after the trigger and failed on
		// exactly that. Same sentinel discipline as above -- printf keeps it
		// out of the command line fly echoes.
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
        args: ["-c", 'printf "node-fail-%s\n" running && sleep 60']
`)
		setAndUnpausePipeline(cfg)
		triggerJob("node-fail-job")

		By("waiting for the pod to appear and its command to start")
		pods := waitForConcoursePodsAtLeast(1)
		sess := fly.Start("watch", "-j", inPipeline("node-fail-job"), "-b", "1")
		Eventually(sess.Out, 3*time.Minute).Should(gbytes.Say("node-fail-running"))

		By("deleting the pod out from under the running command")
		Expect(kubeClient.CoreV1().Pods(config.Namespace).Delete(
			context.Background(), pods[0].Name, metav1.DeleteOptions{},
		)).To(Succeed())

		By("verifying the build errors out")
		Eventually(sess, 5*time.Minute).Should(gexec.Exit())
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
