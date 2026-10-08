package jetbridge

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// ManagedInputTimeout covers every queued managed initializer and pod startup.
// The web bounds this aggregate by the reader's claim term.
func (s *OutputSource) ManagedInputTimeout(count int) time.Duration {
	return managedInputReadyTimeout(s.controls.config, count)
}

// ForResultRead needs an output reader, independent of the original producer
// or whether new workloads can be scheduled on this node.
func (s *OutputSource) ForResultRead(ctx context.Context, epoch executioncontrol.ActivationEpoch) (*OutputControlClient, error) {
	client, _, err := s.outputNode(ctx, epoch)
	return client, err
}

// ForInputUpload pins staging and publication to one currently ready node.
func (s *OutputSource) ForInputUpload(ctx context.Context, epoch executioncontrol.ActivationEpoch) (*OutputControlClient, executioncontrol.NodeUID, error) {
	return s.outputNode(ctx, epoch)
}

func (s *OutputSource) outputNode(ctx context.Context, epoch executioncontrol.ActivationEpoch) (*OutputControlClient, executioncontrol.NodeUID, error) {
	if epoch != s.controls.epoch {
		return nil, "", fmt.Errorf("%w: no output node for the retained epoch", output.ErrInfrastructure)
	}
	nodes, err := s.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labels.Set{executioncontrol.ReadyLabel: "ready", output.ReadyLabel: "ready"}.String()})
	if err != nil {
		return nil, "", err
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	for _, node := range nodes.Items {
		if node.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				client, err := s.exactClient(ctx, node.Name, string(node.UID))
				if err != nil {
					return nil, "", err
				}
				// Leave transport time beyond the daemon's operation budget.
				client.http.Timeout = output.ReadTransferTimeout(client.ManagedReadTimeout())
				client.node = executioncontrol.NodeUID(node.UID)
				return client, executioncontrol.NodeUID(node.UID), nil
			}
		}
	}
	return nil, "", fmt.Errorf("%w: no ready output node", output.ErrInfrastructure)
}
