package steps

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// API object cleanup must survive a cancelled scenario. Callers capture the
// API-assigned UID in their delete precondition, never a name alone.
func registerAPICleanup(rec *brine.Recorder, description string, remove func(context.Context) error) {
	TrackDisposer(rec, description, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return releasedIfGone(remove(ctx))
	})
}

// Desired placement and empty-address policy use API-created Nodes only.
// Address-dependent behavior reads real kubelet-populated Nodes in live tests.
func createRealNode(ctx context.Context, cs kubernetes.Interface, rec *brine.Recorder, name string) (*corev1.Node, error) {
	node, err := cs.CoreV1().Nodes().Create(ctx,
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create node %q: %w", name, err)
	}
	registerAPICleanup(rec, "node "+name, func(ctx context.Context) error {
		return cs.CoreV1().Nodes().Delete(ctx, node.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &node.UID}})
	})
	return node, nil
}

// Kubernetes rejects loopback EndpointSlice addresses. The real daemon
// already listens on all local interfaces; advertise an actual local IPv4
// address instead of changing the API's validation or inventing a pod IP.
func discoveryIPv4() (string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("find local discovery address: %w", err)
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("real daemon discovery needs a non-loopback local IPv4 interface")
}

// registerNamespacePodCleanup is for a newly generated, scenario-owned namespace.
// Include pods created by production, not only those installed by Givens.
func registerNamespacePodCleanup(rec *brine.Recorder, cs kubernetes.Interface, namespace string) {
	registerAPICleanup(rec, "pods in "+namespace, func(ctx context.Context) error {
		pods := cs.CoreV1().Pods(namespace)
		list, err := pods.List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		zero := int64(0)
		for _, pod := range list.Items {
			err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{
				GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID},
			})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	})
}
