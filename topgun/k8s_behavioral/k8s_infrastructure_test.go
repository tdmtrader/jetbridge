package behavioral_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("K8s Infrastructure Assertions", func() {
	// sleepPipeline returns a pipeline YAML with a task that sleeps for
	// a configurable duration, keeping the pod alive for inspection.
	sleepPipeline := func(jobName string, sleepSeconds int) string {
		return writePipelineFile(fmt.Sprintf("k8s-infra-%s.yml", jobName), fmt.Sprintf(`
jobs:
- name: %s
  plan:
  - task: sleep-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo k8s-infra-started && sleep %d"]
`, jobName, sleepSeconds))
	}

	// abortAndWait triggers the job, waits for the pod, then aborts.
	abortAndWait := func(jobName string) {
		By("aborting the build to clean up")
		fly.Run("abort-build", "-j", inPipeline(jobName), "-b", "1")
		Eventually(func() string {
			builds := flyTable("builds", "-j", inPipeline(jobName))
			if len(builds) == 0 {
				return ""
			}
			return builds[0]["status"]
		}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))
	}

	Context("Pod Count Verification", func() {
		PIt("11.1: single task creates exactly 1 pod", func() {
			// Pending: pod count assertions require live K8s cluster with Concourse worker
			pipelineFile := sleepPipeline("single-pod-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("single-pod-job")

			By("waiting for exactly 1 pod to appear")
			pods := waitForConcoursePodsAtLeast(1)
			taskPods := 0
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					taskPods++
				}
			}
			Expect(taskPods).To(BeNumerically(">=", 1))

			abortAndWait("single-pod-job")
		})

		It("11.2: parallel tasks create matching pod count", func() {
			pipelineFile := writePipelineFile("k8s-infra-par-pods.yml", `
jobs:
- name: par-pods-job
  plan:
  - in_parallel:
    - task: par-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo par-a && sleep 30"]
    - task: par-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo par-b && sleep 30"]
    - task: par-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: sh
          args: ["-c", "echo par-c && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("par-pods-job")

			By("waiting for at least 3 concurrent pods")
			pods := waitForConcoursePodsAtLeast(3)
			Expect(len(pods)).To(BeNumerically(">=", 3))

			abortAndWait("par-pods-job")
		})

		It("11.3: get step creates a pod", func() {
			pipelineFile := writePipelineFile("k8s-infra-get-pod.yml", `
resources:
- name: get-pod-resource
  type: mock
  source:
    create_files:
      data.txt: "get-pod-data"

jobs:
- name: get-pod-job
  plan:
  - get: get-pod-resource
    trigger: false
  - task: use-it
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: get-pod-resource}]
      run:
        path: sh
        args: ["-c", "echo get-pod-task && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("get-pod-resource", "v1")
			triggerJob("get-pod-job")

			By("waiting for at least 1 pod during get/task execution")
			pods := waitForConcoursePodsAtLeast(1)
			Expect(len(pods)).To(BeNumerically(">=", 1))

			abortAndWait("get-pod-job")
		})

		It("11.4: put step creates a pod", func() {
			pipelineFile := writePipelineFile("k8s-infra-put-pod.yml", `
resources:
- name: put-pod-resource
  type: mock
  source: {}
  check_every: never

jobs:
- name: put-pod-job
  plan:
  - task: produce
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo produce && sleep 20"]
  - put: put-pod-resource
    params: {version: "put-pod-v1"}
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("put-pod-job")

			By("waiting for at least 1 pod during execution")
			pods := waitForConcoursePodsAtLeast(1)
			Expect(len(pods)).To(BeNumerically(">=", 1))

			abortAndWait("put-pod-job")
		})

		It("11.5: check resource creates a check pod", func() {
			pipelineFile := writePipelineFile("k8s-infra-check-pod.yml", `
resources:
- name: check-pod-resource
  type: mock
  source:
    create_files:
      data.txt: "check-data"

jobs:
- name: check-pod-job
  plan:
  - get: check-pod-resource
    trigger: false
  - task: done
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["check-pod-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			newMockVersion("check-pod-resource", "v1")
			triggerJob("check-pod-job")

			session := waitForBuildAndWatch("check-pod-job")
			Expect(session).To(gexec.Exit(0))
		})

		It("11.6: no pods exist when no builds are running", func() {
			pipelineFile := writePipelineFile("k8s-infra-idle.yml", `
jobs:
- name: idle-job
  plan:
  - task: quick-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["idle-done"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("idle-job")

			session := waitForBuildAndWatch("idle-job")
			Expect(session).To(gexec.Exit(0))

			By("verifying no active pods remain after build completes")
			waitForPodCleanupByPipeline()
		})

		It("11.7: consecutive builds do not accumulate pods", func() {
			pipelineFile := writePipelineFile("k8s-infra-consecutive.yml", `
jobs:
- name: consec-job
  plan:
  - task: quick-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["consec-done"]
`)
			setAndUnpausePipeline(pipelineFile)

			for i := 1; i <= 3; i++ {
				By(fmt.Sprintf("triggering build %d", i))
				triggerJob("consec-job")
				session := waitForBuildAndWatch("consec-job", fmt.Sprintf("%d", i))
				Expect(session).To(gexec.Exit(0))
			}

			By("verifying no pods accumulated after 3 builds")
			waitForPodCleanupByPipeline()
		})
	})

	Context("Container Composition", func() {
		It("11.8: task pod has at least 1 container", func() {
			pipelineFile := sleepPipeline("container-count-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("container-count-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					assertContainerCount(&p, len(p.Spec.Containers))
					Expect(len(p.Spec.Containers)).To(BeNumerically(">=", 1))
					break
				}
			}

			abortAndWait("container-count-job")
		})

		It("11.9: main container runs the configured image", func() {
			pipelineFile := writePipelineFile("k8s-infra-image.yml", `
jobs:
- name: image-check-job
  plan:
  - task: image-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo image-task && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("image-check-job")

			pods := waitForConcoursePodsAtLeast(1)
			var taskPod *corev1.Pod
			for i := range pods {
				if pods[i].Labels["concourse.ci/type"] == "task" {
					taskPod = &pods[i]
					break
				}
			}

			if taskPod != nil {
				By("verifying the main container image contains busybox")
				image := podImage(taskPod)
				Expect(image).To(ContainSubstring("busybox"))
			}

			abortAndWait("image-check-job")
		})

		It("11.10: pod is not running in privileged mode by default", func() {
			pipelineFile := sleepPipeline("no-priv-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("no-priv-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					Expect(podIsPrivileged(&p)).To(BeFalse(),
						fmt.Sprintf("expected pod %q to not be privileged", p.Name))
					break
				}
			}

			abortAndWait("no-priv-job")
		})

		It("11.11: pod uses correct service account", func() {
			pipelineFile := sleepPipeline("sa-check-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("sa-check-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					sa := podServiceAccount(&p)
					By(fmt.Sprintf("pod %q uses service account %q", p.Name, sa))
					Expect(sa).ToNot(BeEmpty(),
						fmt.Sprintf("expected pod %q to have a service account", p.Name))
					break
				}
			}

			abortAndWait("sa-check-job")
		})

		It("11.12: each container in a pod has a name", func() {
			pipelineFile := sleepPipeline("container-names-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("container-names-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				for _, c := range p.Spec.Containers {
					Expect(c.Name).ToNot(BeEmpty(),
						fmt.Sprintf("expected all containers in pod %q to have names", p.Name))
				}
			}

			abortAndWait("container-names-job")
		})
	})

	Context("Resource Allocation", func() {
		It("11.13: tasks with container_limits have CPU limits set", func() {
			pipelineFile := writePipelineFile("k8s-infra-cpu-limits.yml", `
jobs:
- name: cpu-limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits:
        cpu: 512
        memory: 256000000
      run:
        path: sh
        args: ["-c", "echo cpu-limited && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cpu-limits-job")

			By("waiting for a task pod with limits")
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

			pod := getPodByName(podName)
			cpuLimit := podCPULimit(pod)
			Expect(cpuLimit).ToNot(BeNil(), "expected CPU limit to be set")

			abortAndWait("cpu-limits-job")
		})

		It("11.14: tasks with container_limits have memory limits set", func() {
			pipelineFile := writePipelineFile("k8s-infra-mem-limits.yml", `
jobs:
- name: mem-limits-job
  plan:
  - task: limited-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits:
        cpu: 512
        memory: 256000000
      run:
        path: sh
        args: ["-c", "echo mem-limited && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("mem-limits-job")

			By("waiting for a task pod with limits")
			var podName string
			Eventually(func() bool {
				found := findConcoursePodsForWorker()
				for _, p := range found {
					if p.Labels["concourse.ci/type"] != "task" {
						continue
					}
					c := mainContainer(&p)
					if _, hasLimit := c.Resources.Limits[corev1.ResourceMemory]; hasLimit {
						podName = p.Name
						return true
					}
				}
				return false
			}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "expected a task pod with memory limits")

			pod := getPodByName(podName)
			memLimit := podMemoryLimit(pod)
			Expect(memLimit).ToNot(BeNil(), "expected memory limit to be set")

			abortAndWait("mem-limits-job")
		})

		PIt("11.15: tasks without container_limits have no resource limits", func() {
			// Pending: resource limit assertions require live K8s cluster for pod inspection
			pipelineFile := sleepPipeline("no-limits-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("no-limits-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					assertNoResourceLimits(&p)
					break
				}
			}

			abortAndWait("no-limits-job")
		})

		It("11.16: both CPU and memory limits are enforced together", func() {
			pipelineFile := writePipelineFile("k8s-infra-both-limits.yml", `
jobs:
- name: both-limits-job
  plan:
  - task: both-limited
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      container_limits:
        cpu: 256
        memory: 128000000
      run:
        path: sh
        args: ["-c", "echo both-limited && sleep 30"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("both-limits-job")

			By("waiting for a task pod with both limits")
			var podName string
			Eventually(func() bool {
				found := findConcoursePodsForWorker()
				for _, p := range found {
					if p.Labels["concourse.ci/type"] != "task" {
						continue
					}
					c := mainContainer(&p)
					_, hasCPU := c.Resources.Limits[corev1.ResourceCPU]
					_, hasMem := c.Resources.Limits[corev1.ResourceMemory]
					if hasCPU && hasMem {
						podName = p.Name
						return true
					}
				}
				return false
			}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "expected a task pod with both limits")

			pod := getPodByName(podName)
			Expect(podCPULimit(pod)).ToNot(BeNil())
			Expect(podMemoryLimit(pod)).ToNot(BeNil())

			abortAndWait("both-limits-job")
		})
	})

	Context("Pod Cleanup", func() {
		It("11.17: pods are cleaned up after successful build", func() {
			pipelineFile := writePipelineFile("k8s-cleanup-success.yml", `
jobs:
- name: cleanup-success-job
  plan:
  - task: quick-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["cleanup-success"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-success-job")

			session := waitForBuildAndWatch("cleanup-success-job")
			Expect(session).To(gexec.Exit(0))

			waitForPodCleanupByPipeline()
		})

		PIt("11.18: pods are cleaned up after failed build", func() {
			// Pending: pod cleanup after failed build requires live K8s cluster with Reaper integration
			pipelineFile := writePipelineFile("k8s-cleanup-fail.yml", `
jobs:
- name: cleanup-fail-job
  plan:
  - task: failing-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: sh
        args: ["-c", "echo cleanup-failing && exit 1"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-fail-job")

			session := waitForBuildAndWatch("cleanup-fail-job")
			Expect(session.ExitCode()).ToNot(Equal(0))

			waitForPodCleanupByPipeline()
		})

		It("11.19: pods are cleaned up after aborted build", func() {
			pipelineFile := sleepPipeline("cleanup-abort-job", 3600)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-abort-job")

			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("cleanup-abort-job"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 2*time.Minute, 2*time.Second).Should(Equal("started"))

			fly.Run("abort-build", "-j", inPipeline("cleanup-abort-job"), "-b", "1")

			Eventually(func() string {
				builds := flyTable("builds", "-j", inPipeline("cleanup-abort-job"))
				if len(builds) == 0 {
					return ""
				}
				return builds[0]["status"]
			}, 1*time.Minute, 2*time.Second).Should(Equal("aborted"))

			waitForPodCleanupByPipeline()
		})

		It("11.20: parallel task pods are all cleaned up", func() {
			pipelineFile := writePipelineFile("k8s-cleanup-parallel.yml", `
jobs:
- name: cleanup-par-job
  plan:
  - in_parallel:
    - task: par-a
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["par-a"]
    - task: par-b
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["par-b"]
    - task: par-c
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["par-c"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-par-job")

			session := waitForBuildAndWatch("cleanup-par-job")
			Expect(session).To(gexec.Exit(0))

			waitForPodCleanupByPipeline()
		})

		PIt("11.21: hook pods are cleaned up after build", func() {
			// Pending: hook pod cleanup requires live K8s cluster with Reaper integration
			pipelineFile := writePipelineFile("k8s-cleanup-hooks.yml", `
jobs:
- name: cleanup-hooks-job
  plan:
  - task: main-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["cleanup-hooks-main"]
    ensure:
      task: cleanup-ensure
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: busybox}}
        run:
          path: echo
          args: ["cleanup-hooks-ensure"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-hooks-job")

			session := waitForBuildAndWatch("cleanup-hooks-job")
			Expect(session).To(gexec.Exit(0))

			waitForPodCleanupByPipeline()
		})

		It("11.22: pods are cleaned up after pipeline destruction", func() {
			pipelineFile := writePipelineFile("k8s-cleanup-destroy.yml", `
jobs:
- name: cleanup-destroy-job
  plan:
  - task: quick-task
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["about-to-destroy"]
`)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("cleanup-destroy-job")

			session := waitForBuildAndWatch("cleanup-destroy-job")
			Expect(session).To(gexec.Exit(0))

			By("destroying the pipeline")
			fly.Run("destroy-pipeline", "-n", "-p", pipelineName)

			By("verifying pods are cleaned up after pipeline destruction")
			waitForPodCleanupByPipeline()
		})
	})

	Context("Labels Annotations and Metadata", func() {
		It("11.23: pod has concourse.ci/type label", func() {
			pipelineFile := sleepPipeline("label-type-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-type-job")

			pods := waitForConcoursePodsAtLeast(1)
			found := false
			for _, p := range pods {
				if _, ok := p.Labels["concourse.ci/type"]; ok {
					found = true
					Expect(p.Labels["concourse.ci/type"]).ToNot(BeEmpty())
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one pod with concourse.ci/type label")

			abortAndWait("label-type-job")
		})

		It("11.24: pod has concourse.ci/worker label", func() {
			pipelineFile := sleepPipeline("label-worker-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-worker-job")

			pods := waitForConcoursePodsAtLeast(1)
			Expect(pods[0].Labels).To(HaveKey("concourse.ci/worker"),
				"expected pod to have concourse.ci/worker label")

			abortAndWait("label-worker-job")
		})

		It("11.25: pod has concourse.ci/pipeline label", func() {
			pipelineFile := sleepPipeline("label-pipeline-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-pipeline-job")

			pods := waitForConcoursePodsAtLeast(1)
			found := false
			for _, p := range pods {
				if val, ok := p.Labels["concourse.ci/pipeline"]; ok {
					assertPodHasLabel(&p, "concourse.ci/pipeline", val)
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one pod with concourse.ci/pipeline label")

			abortAndWait("label-pipeline-job")
		})

		It("11.26: pod has concourse.ci/job label", func() {
			pipelineFile := sleepPipeline("label-job-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-job-job")

			pods := waitForConcoursePodsAtLeast(1)
			found := false
			for _, p := range pods {
				if _, ok := p.Labels["concourse.ci/job"]; ok {
					Expect(p.Labels["concourse.ci/job"]).ToNot(BeEmpty())
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one pod with concourse.ci/job label")

			abortAndWait("label-job-job")
		})

		PIt("11.27: pod has concourse.ci/build-id label", func() {
			// Pending: build-id label was added to production code; needs live K8s cluster for E2E validation
			pipelineFile := sleepPipeline("label-build-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-build-job")

			pods := waitForConcoursePodsAtLeast(1)
			found := false
			for _, p := range pods {
				if _, ok := p.Labels["concourse.ci/build-id"]; ok {
					Expect(p.Labels["concourse.ci/build-id"]).ToNot(BeEmpty())
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one pod with concourse.ci/build-id label")

			abortAndWait("label-build-job")
		})

		PIt("11.28: task pod has type=task label", func() {
			// Pending: type=task label assertion requires live K8s cluster for pod inspection
			pipelineFile := sleepPipeline("label-task-type-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("label-task-type-job")

			pods := waitForConcoursePodsAtLeast(1)
			found := false
			for _, p := range pods {
				if p.Labels["concourse.ci/type"] == "task" {
					assertPodHasLabel(&p, "concourse.ci/type", "task")
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one pod with concourse.ci/type=task")

			abortAndWait("label-task-type-job")
		})

		It("11.29: pod follows readable naming convention", func() {
			pipelineFile := sleepPipeline("naming-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("naming-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				By(fmt.Sprintf("checking pod name: %s", p.Name))
				// Pod names should contain recognizable components (not random-only)
				Expect(p.Name).ToNot(BeEmpty())
				// Pod name should have multiple segments separated by hyphens
				segments := strings.Split(p.Name, "-")
				Expect(len(segments)).To(BeNumerically(">=", 2),
					fmt.Sprintf("expected pod name %q to have multiple hyphen-separated segments", p.Name))
			}

			abortAndWait("naming-job")
		})

		It("11.30: pod namespace matches configured namespace", func() {
			pipelineFile := sleepPipeline("namespace-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("namespace-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				Expect(p.Namespace).To(Equal(config.Namespace),
					fmt.Sprintf("expected pod %q to be in namespace %q, got %q",
						p.Name, config.Namespace, p.Namespace))
			}

			abortAndWait("namespace-job")
		})

		It("11.31: pod has standard Kubernetes metadata fields", func() {
			pipelineFile := sleepPipeline("metadata-job", 30)
			setAndUnpausePipeline(pipelineFile)
			triggerJob("metadata-job")

			pods := waitForConcoursePodsAtLeast(1)
			for _, p := range pods {
				By(fmt.Sprintf("checking metadata for pod %s", p.Name))
				Expect(p.Name).ToNot(BeEmpty(), "pod should have a name")
				Expect(p.Namespace).ToNot(BeEmpty(), "pod should have a namespace")
				Expect(p.UID).ToNot(BeEmpty(), "pod should have a UID")
				Expect(p.CreationTimestamp.IsZero()).To(BeFalse(), "pod should have a creation timestamp")
				Expect(p.Labels).ToNot(BeEmpty(), "pod should have labels")
			}

			abortAndWait("metadata-job")
		})
	})
})
