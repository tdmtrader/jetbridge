package integration_test

import (
	"context"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Release only the uniquely selected, still-running intermediate task, after
// the caller has verified the original producer-deletion assertions. The task
// retains its original timer as a fallback if no release arrives.
func releaseTaskHold(selector string) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pods, err := kubeClient.CoreV1().Pods(config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	Expect(err).ToNot(HaveOccurred())
	Expect(pods.Items).To(HaveLen(1), "expected the single original intermediate task")
	pod := pods.Items[0]
	Expect(pod.UID).ToNot(BeEmpty())
	Expect(pod.Status.Phase).To(Equal(corev1.PodRunning))
	Expect(pod.Spec.Containers).ToNot(BeEmpty())
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", config.Kubeconfig, "-n", config.Namespace, "exec", pod.Name, "-c", pod.Spec.Containers[0].Name, "--", "touch", "/tmp/concourse-test-release")
	output, err := command.CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "release real intermediate task: %s", output)
}
