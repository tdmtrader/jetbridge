package integration_test

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

// waitForConcoursePodsAtLeast waits until at least n pods exist with
// the concourse worker label, then returns all matching pods.
func waitForConcoursePodsAtLeast(n int) []corev1.Pod {
	GinkgoHelper()
	var pods []corev1.Pod
	Eventually(func() int {
		pods = findConcoursePodsForWorker()
		return len(pods)
	}, 2*time.Minute, time.Second).Should(BeNumerically(">=", n),
		fmt.Sprintf("expected at least %d concourse pods", n),
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
// Pod cleanup assertion helpers
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
		selector := fmt.Sprintf("concourse.ci/worker,concourse.ci/pipeline=%s", pipelineName)
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
		fmt.Sprintf("expected all workload pods for pipeline %q to be cleaned up", pipelineName),
	)
}
