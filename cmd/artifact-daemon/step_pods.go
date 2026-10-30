package main

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
)

// handleLabelKey is the Pod label the web stamps on every step pod with the
// step's container handle (atc/worker/jetbridge/config.go). The handle is the
// name of the step's directory under steps/, so it is how a directory on disk
// maps back to a Pod on this node.
const handleLabelKey = "concourse.ci/handle"

// StepPods is the sweeper's view of the step pods on this node.
type StepPods interface {
	// Live returns the handles of this node's step pods that have not
	// terminated: a container of each may still read its inputs or write its
	// outputs under steps/<handle>.
	Live(ctx context.Context) (map[string]struct{}, error)
}

// ProbeStepPods lists the view once so a daemon that cannot read its node's
// step pods says so at startup instead of sparing every step directory forever.
func ProbeStepPods(ctx context.Context, pods StepPods) error {
	if _, err := pods.Live(ctx); err != nil {
		return fmt.Errorf("the sweeper cannot read this node's step pods: %w", err)
	}
	return nil
}

// nodeStepPods reads this node's step pods from the API server with the same
// get/list grant the capture seal uses. It changes nothing: the sweeper
// spares what it finds alive and never ends a Pod to make room.
type nodeStepPods struct {
	client    kubernetes.Interface
	namespace string
	node      string
}

// NewNodeStepPods observes the step pods in namespace scheduled to node.
func NewNodeStepPods(client kubernetes.Interface, namespace, node string) StepPods {
	return &nodeStepPods{client: client, namespace: namespace, node: node}
}

func (pods *nodeStepPods) Live(ctx context.Context) (map[string]struct{}, error) {
	list, err := pods.client.CoreV1().Pods(pods.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + pods.node,
		LabelSelector: handleLabelKey,
	})
	if err != nil {
		return nil, fmt.Errorf("listing this node's step pods: %w", err)
	}
	live := map[string]struct{}{}
	for i := range list.Items {
		pod := &list.Items[i]
		handle := pod.Labels[handleLabelKey]
		if handle == "" || pod.Spec.NodeName != pods.node || outputplane.PodTerminated(pod) {
			continue
		}
		live[handle] = struct{}{}
	}
	return live, nil
}
