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

// handleLabelKey is the Pod label the web stamps on every step pod with the
// step's container handle (atc/worker/jetbridge/config.go). The handle is the
// name of the step's directory under steps/, so it is how a directory on disk
// maps back to a Pod on this node.
const handleLabelKey = "concourse.ci/handle"

// NodePods is the daemon's one view of this node's own Pods in the step pod
// namespace: the capture seal asks it whether a producing Pod has terminated,
// the TTL sweep which step pods are still live. It has get/list on Pods and
// nothing that changes one: neither ends a Pod to get its answer.
type NodePods struct {
	client    kubernetes.Interface
	namespace string
	node      string
}

// NewNodePods observes Pods in namespace scheduled to node.
func NewNodePods(client kubernetes.Interface, namespace, node string) *NodePods {
	return &NodePods{client: client, namespace: namespace, node: node}
}

func (pods *NodePods) list(ctx context.Context, labelSelector string) ([]corev1.Pod, error) {
	list, err := pods.client.CoreV1().Pods(pods.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + pods.node,
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Terminated is true when the Pod with this UID is gone from the node, or
// every one of its containers -- init, regular and ephemeral -- has a
// terminated state, or its phase is terminal. Any container running, waiting
// to start or without a status yet is a writer that may still write.
func (pods *NodePods) Terminated(ctx context.Context, uid executioncontrol.PodUID) (bool, error) {
	items, err := pods.list(ctx, "")
	if err != nil {
		return false, fmt.Errorf("listing this node's Pods: %w", err)
	}
	for _, pod := range items {
		if string(pod.UID) != string(uid) {
			continue
		}

		return PodTerminated(&pod), nil
	}

	return true, nil
}

// Live returns the handles of this node's step pods that have not terminated:
// a container of each may still read its inputs or write its outputs under
// steps/<handle>.
func (pods *NodePods) Live(ctx context.Context) (map[string]struct{}, error) {
	items, err := pods.list(ctx, handleLabelKey)
	if err != nil {
		return nil, fmt.Errorf("listing this node's step pods: %w", err)
	}
	live := map[string]struct{}{}
	for i := range items {
		pod := &items[i]
		handle := pod.Labels[handleLabelKey]
		if handle == "" || pod.Spec.NodeName != pods.node || PodTerminated(pod) {
			continue
		}
		live[handle] = struct{}{}
	}
	return live, nil
}

// PodTerminated is the one definition of "every writer has stopped" for a Pod:
// its phase is terminal, or every declared container -- init, regular and
// ephemeral -- has a terminated state. The capture seal and the daemon's TTL
// sweep both ask it, so a Pod they disagree about cannot exist.
func PodTerminated(pod *corev1.Pod) bool {
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
