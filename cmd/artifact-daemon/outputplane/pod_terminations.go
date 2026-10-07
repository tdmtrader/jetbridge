package outputplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// nodePodTerminations reads this node's own Pods from the API server. It has
// get/list on Pods and nothing that changes one: a seal waits for writers to
// stop and never makes them.
type nodePodTerminations struct {
	client    kubernetes.Interface
	namespace string
	node      string
}

// NewNodePodTerminations observes Pods in namespace scheduled to node.
func NewNodePodTerminations(client kubernetes.Interface, namespace, node string) PodTerminations {
	return &nodePodTerminations{client: client, namespace: namespace, node: node}
}

// Terminated is true when the Pod with this UID is gone from the node, or
// every one of its containers -- init, regular and ephemeral -- has a
// terminated state, or its phase is terminal. Any container running, waiting
// to start or without a status yet is a writer that may still write.
func (terminations *nodePodTerminations) Terminated(ctx context.Context, uid executioncontrol.PodUID) (bool, error) {
	pods, err := terminations.client.CoreV1().Pods(terminations.namespace).List(ctx, metav1.ListOptions{
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

// declaredPodTerminations is the standalone answer: a Pod has terminated when
// a file named after its UID exists in dir. It exists for a daemon run with no
// Kubernetes API -- a test, a single host -- where something else knows when
// the Pod's processes have stopped and says so.
type declaredPodTerminations struct{ dir string }

// DeclaredPodTerminations reads terminations declared as files in dir.
func DeclaredPodTerminations(dir string) PodTerminations { return declaredPodTerminations{dir: dir} }

func (declared declaredPodTerminations) Terminated(_ context.Context, uid executioncontrol.PodUID) (bool, error) {
	name := string(uid)
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return false, fmt.Errorf("%q is not a Pod UID", name)
	}
	_, err := os.Stat(filepath.Join(declared.dir, name))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
