package main

import (
	"context"
	"encoding/json"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// NodeLabeler manages the node label that indicates a healthy DaemonSet pod.
type NodeLabeler struct {
	logger    lager.Logger
	client    kubernetes.Interface
	nodeName  string
	labelKey  string
	labelValue string
}

// NewNodeLabeler creates a new NodeLabeler.
func NewNodeLabeler(logger lager.Logger, client kubernetes.Interface, nodeName, labelKey string) *NodeLabeler {
	return &NodeLabeler{
		logger:     logger,
		client:     client,
		nodeName:   nodeName,
		labelKey:   labelKey,
		labelValue: "ready",
	}
}

// AddLabel sets the node label to indicate the artifact cache is ready.
func (n *NodeLabeler) AddLabel(ctx context.Context) error {
	logger := n.logger.Session("add-node-label", lager.Data{"node": n.nodeName, "label": n.labelKey})

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				n.labelKey: n.labelValue,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		logger.Error("failed-to-marshal-patch", err)
		return fmt.Errorf("marshal patch: %w", err)
	}

	_, err = n.client.CoreV1().Nodes().Patch(ctx, n.nodeName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		logger.Error("failed-to-patch-node", err)
		return fmt.Errorf("patch node %s: %w", n.nodeName, err)
	}

	logger.Info("labeled")
	return nil
}

// RemoveLabel removes the node label on shutdown.
func (n *NodeLabeler) RemoveLabel(ctx context.Context) error {
	logger := n.logger.Session("remove-node-label", lager.Data{"node": n.nodeName, "label": n.labelKey})

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				n.labelKey: nil,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		logger.Error("failed-to-marshal-patch", err)
		return fmt.Errorf("marshal patch: %w", err)
	}

	_, err = n.client.CoreV1().Nodes().Patch(ctx, n.nodeName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		logger.Error("failed-to-patch-node", err)
		return fmt.Errorf("patch node %s: %w", n.nodeName, err)
	}

	logger.Info("unlabeled")
	return nil
}
