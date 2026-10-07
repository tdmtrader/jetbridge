package outputplane

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// nodePodTerminations reads this node's own Pods from the API server. It has
// get/list on Pods and nothing that changes one: a seal waits for writers to
// stop and never makes them.
type nodePodTerminations struct {
	client kubernetes.Interface
	node   string
}

// NewNodePodTerminations observes Pods scheduled to node.
func NewNodePodTerminations(client kubernetes.Interface, node string) PodTerminations {
	return &nodePodTerminations{client: client, node: node}
}

// Terminated is true when the Pod with this UID is gone from the node, or
// every one of its containers -- init, regular and ephemeral -- has a
// terminated state, or its phase is terminal. Any container running, waiting
// to start or without a status yet is a writer that may still write.
func (terminations *nodePodTerminations) Terminated(ctx context.Context, uid executioncontrol.PodUID) (bool, error) {
	pods, err := terminations.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + terminations.node,
	})
	if err != nil {
		return false, fmt.Errorf("listing this node's Pods: %w", err)
	}
	for _, pod := range pods.Items {
		if string(pod.UID) != string(uid) {
			continue
		}

		return podTerminated(&pod), nil
	}

	return true, nil
}

func podTerminated(pod *corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return true
	}
	declared := len(pod.Spec.InitContainers) + len(pod.Spec.Containers) + len(pod.Spec.EphemeralContainers)
	statuses := append(append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...),
		pod.Status.ContainerStatuses...), pod.Status.EphemeralContainerStatuses...)
	if len(statuses) < declared {
		return false
	}
	for _, status := range statuses {
		if status.State.Terminated == nil {
			return false
		}
	}

	return true
}
