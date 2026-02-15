package behavioral_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------------
// Pod spec inspection helpers
// ---------------------------------------------------------------------

// mainContainer returns the first container from the pod spec.
func mainContainer(pod *corev1.Pod) corev1.Container {
	GinkgoHelper()
	Expect(pod.Spec.Containers).ToNot(BeEmpty(), "pod should have at least one container")
	return pod.Spec.Containers[0]
}

// podImage returns the image of the main container.
func podImage(pod *corev1.Pod) string {
	return mainContainer(pod).Image
}

// podCPULimit returns the CPU limit of the main container, or nil if unset.
func podCPULimit(pod *corev1.Pod) *resource.Quantity {
	c := mainContainer(pod)
	if cpu, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		return &cpu
	}
	return nil
}

// podMemoryLimit returns the memory limit of the main container, or nil if unset.
func podMemoryLimit(pod *corev1.Pod) *resource.Quantity {
	c := mainContainer(pod)
	if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
		return &mem
	}
	return nil
}

// podQoSClass returns the QoS class assigned to the pod by K8s.
func podQoSClass(pod *corev1.Pod) corev1.PodQOSClass {
	return pod.Status.QOSClass
}

// podIsPrivileged returns true if the main container has Privileged=true.
func podIsPrivileged(pod *corev1.Pod) bool {
	c := mainContainer(pod)
	if c.SecurityContext == nil {
		return false
	}
	if c.SecurityContext.Privileged == nil {
		return false
	}
	return *c.SecurityContext.Privileged
}

// podAllowsPrivilegeEscalation returns the AllowPrivilegeEscalation setting
// of the main container, or nil if unset.
func podAllowsPrivilegeEscalation(pod *corev1.Pod) *bool {
	c := mainContainer(pod)
	if c.SecurityContext == nil {
		return nil
	}
	return c.SecurityContext.AllowPrivilegeEscalation
}

// podServiceAccount returns the ServiceAccountName from the pod spec.
func podServiceAccount(pod *corev1.Pod) string {
	return pod.Spec.ServiceAccountName
}

// ---------------------------------------------------------------------
// Build-to-pod correlation
// ---------------------------------------------------------------------

// findConcoursePodsForWorker returns pods labeled with the concourse
// worker label. These are pods created by the k8s runtime for builds.
func findConcoursePodsForWorker() []corev1.Pod {
	return getPods("concourse.ci/worker")
}

// waitForConcoursePodsAtLeast waits until at least n pods exist for
// the current pipeline, then returns all matching pods. Filters by
// pipeline label to avoid cross-pipeline interference.
func waitForConcoursePodsAtLeast(n int) []corev1.Pod {
	GinkgoHelper()
	var pods []corev1.Pod
	selector := fmt.Sprintf("concourse.ci/worker,concourse.ci/pipeline=%s", pipelineName)
	Eventually(func() int {
		pods = getPods(selector)
		return len(pods)
	}, 2*time.Minute, time.Second).Should(BeNumerically(">=", n),
		fmt.Sprintf("expected at least %d concourse pods for pipeline %q", n, pipelineName),
	)
	return pods
}

// waitForNoConcourseWorkloadPods waits until no pods with the concourse
// worker label exist (GC has cleaned them up).
func waitForNoConcourseWorkloadPods() {
	GinkgoHelper()
	Eventually(func() int {
		return len(findConcoursePodsForWorker())
	}, 3*time.Minute, 2*time.Second).Should(Equal(0),
		"expected all concourse workload pods to be cleaned up",
	)
}

// cleanupPodsWithLabel deletes all pods matching the label selector.
func cleanupPodsWithLabel(labelSelector string) {
	err := kubeClient.CoreV1().Pods(config.Namespace).DeleteCollection(
		context.Background(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: labelSelector},
	)
	Expect(err).ToNot(HaveOccurred())
}

// ---------------------------------------------------------------------
// Pod cleanup helpers
// ---------------------------------------------------------------------

// countWorkloadPods returns the number of active (non-Succeeded/Failed)
// workload pods created by Concourse.
func countWorkloadPods() int {
	pods := findConcoursePodsForWorker()
	count := 0
	for _, p := range pods {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			count++
		}
	}
	return count
}

// waitForPodCleanupByPipeline waits until no active workload pods exist
// that were created for the current pipeline. This is stricter than
// waitForNoConcourseWorkloadPods because it filters by pipeline label.
func waitForPodCleanupByPipeline() {
	GinkgoHelper()
	Eventually(func() int {
		selector := fmt.Sprintf("concourse.ci/worker,concourse.ci/pipeline=%s,concourse.ci/type!=check", pipelineName)
		pods, err := kubeClient.CoreV1().Pods(config.Namespace).List(
			context.Background(),
			metav1.ListOptions{LabelSelector: selector},
		)
		if err != nil {
			return -1
		}
		count := 0
		for _, p := range pods.Items {
			if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
				count++
			}
		}
		return count
	}, 3*time.Minute, 2*time.Second).Should(Equal(0),
		fmt.Sprintf("expected all workload pods for pipeline %q to be cleaned up (excludes check pods)", pipelineName),
	)
}

// ---------------------------------------------------------------------
// Assertion helpers (new for behavioral suite)
// ---------------------------------------------------------------------

// assertPodCount asserts that the number of pods matching the label
// selector eventually equals the expected count.
func assertPodCount(labelSelector string, expected int) {
	GinkgoHelper()
	Eventually(func() int {
		return len(getPods(labelSelector))
	}, 2*time.Minute, time.Second).Should(Equal(expected),
		fmt.Sprintf("expected %d pods with label %q", expected, labelSelector),
	)
}

// assertContainerCount asserts that the pod has exactly the expected
// number of containers.
func assertContainerCount(pod *corev1.Pod, expected int) {
	GinkgoHelper()
	Expect(pod.Spec.Containers).To(HaveLen(expected),
		fmt.Sprintf("expected pod %q to have %d containers, got %d",
			pod.Name, expected, len(pod.Spec.Containers)),
	)
}

// assertContainerNames asserts that the pod's container names match
// the given names (order-independent).
func assertContainerNames(pod *corev1.Pod, names ...string) {
	GinkgoHelper()
	actual := make([]string, len(pod.Spec.Containers))
	for i, c := range pod.Spec.Containers {
		actual[i] = c.Name
	}
	Expect(actual).To(ConsistOf(names),
		fmt.Sprintf("expected pod %q containers %v to match %v", pod.Name, actual, names),
	)
}

// assertPodHasLabel asserts that the pod has the given label key with
// the expected value.
func assertPodHasLabel(pod *corev1.Pod, key, value string) {
	GinkgoHelper()
	Expect(pod.Labels).To(HaveKeyWithValue(key, value),
		fmt.Sprintf("expected pod %q to have label %s=%s", pod.Name, key, value),
	)
}

// assertPodHasAnnotation asserts that the pod has the given annotation key.
func assertPodHasAnnotation(pod *corev1.Pod, key string) {
	GinkgoHelper()
	Expect(pod.Annotations).To(HaveKey(key),
		fmt.Sprintf("expected pod %q to have annotation %q", pod.Name, key),
	)
}

// assertPodResourceLimits asserts that the main container of the pod
// has the specified CPU and memory limits.
func assertPodResourceLimits(pod *corev1.Pod, cpu, memory string) {
	GinkgoHelper()
	c := mainContainer(pod)

	cpuLimit, ok := c.Resources.Limits[corev1.ResourceCPU]
	Expect(ok).To(BeTrue(), fmt.Sprintf("expected pod %q to have CPU limit", pod.Name))
	Expect(cpuLimit.String()).To(Equal(cpu),
		fmt.Sprintf("expected pod %q CPU limit %s, got %s", pod.Name, cpu, cpuLimit.String()),
	)

	memLimit, ok := c.Resources.Limits[corev1.ResourceMemory]
	Expect(ok).To(BeTrue(), fmt.Sprintf("expected pod %q to have memory limit", pod.Name))
	Expect(memLimit.String()).To(Equal(memory),
		fmt.Sprintf("expected pod %q memory limit %s, got %s", pod.Name, memory, memLimit.String()),
	)
}

// assertNoResourceLimits asserts that the main container of the pod
// has no resource limits set.
func assertNoResourceLimits(pod *corev1.Pod) {
	GinkgoHelper()
	c := mainContainer(pod)
	Expect(c.Resources.Limits).To(BeEmpty(),
		fmt.Sprintf("expected pod %q to have no resource limits, got %v", pod.Name, c.Resources.Limits),
	)
}

// assertPodCleanup is a standard post-build assertion that waits for
// all active concourse workload pods to reach 0.
func assertPodCleanup() {
	GinkgoHelper()
	waitForNoConcourseWorkloadPods()
}

// assertPodCleanupForPipeline is the same as assertPodCleanup but
// filtered by the current pipelineName.
func assertPodCleanupForPipeline() {
	GinkgoHelper()
	waitForPodCleanupByPipeline()
}

// containerByName finds a container by name within the pod spec.
// Returns nil if no container with the given name exists.
func containerByName(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

// podAnnotation returns the value of the given annotation key, or
// an empty string if the annotation does not exist.
func podAnnotation(pod *corev1.Pod, key string) string {
	if pod.Annotations == nil {
		return ""
	}
	return pod.Annotations[key]
}

// waitForPodsWithLabelCount waits until the exact number of pods
// matching the label selector exist, then returns them.
func waitForPodsWithLabelCount(labelSelector string, count int) []corev1.Pod {
	GinkgoHelper()
	var pods []corev1.Pod
	Eventually(func() int {
		pods = getPods(labelSelector)
		return len(pods)
	}, 2*time.Minute, time.Second).Should(Equal(count),
		fmt.Sprintf("expected exactly %d pods with label %q", count, labelSelector),
	)
	return pods
}
