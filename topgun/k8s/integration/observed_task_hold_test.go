package integration_test

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"os/exec"
	"time"
)

// Called only after the original pod-inspection assertions have passed.
// The original task timer remains the fallback if no release is sent.
func releaseObservedTaskHold(name string) {
	GinkgoHelper()
	pods := findConcoursePodsForWorker()
	var selected []corev1.Pod
	for _, pod := range pods {
		if pod.Labels["concourse.ci/type"] == "task" && (name == "" || pod.Name == name) {
			selected = append(selected, pod)
		}
	}
	Expect(selected).To(HaveLen(1), "expected the uniquely observed task in the current pipeline")
	original := selected[0]
	Expect(original.UID).ToNot(BeEmpty())
	Eventually(func() corev1.PodPhase {
		current := getPodByName(original.Name)
		Expect(current.UID).To(Equal(original.UID))
		return current.Status.Phase
	}, 2*time.Minute, 100*time.Millisecond).Should(Equal(corev1.PodRunning))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	container := mainContainer(&original)
	output, err := exec.CommandContext(ctx, "kubectl", "--kubeconfig", config.Kubeconfig, "-n", config.Namespace, "exec", original.Name, "-c", container.Name, "--", "touch", "/tmp/concourse-test-release").CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "release observed task: %s", output)
	Expect(getPodByName(original.Name).UID).To(Equal(original.UID))
}
