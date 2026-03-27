package main_test

import (
	"context"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestNodeLabeler_AddLabel(t *testing.T) {
	ctx := context.Background()
	logger := lagertest.NewTestLogger("node-labeler")
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
	})

	labeler := daemon.NewNodeLabeler(logger, client, "test-node", "concourse.dev/artifact-cache")

	if err := labeler.AddLabel(ctx); err != nil {
		t.Fatalf("AddLabel failed: %v", err)
	}

	node, err := client.CoreV1().Nodes().Get(ctx, "test-node", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	if node.Labels["concourse.dev/artifact-cache"] != "ready" {
		t.Errorf("expected label 'ready', got %q", node.Labels["concourse.dev/artifact-cache"])
	}
}

func TestNodeLabeler_RemoveLabel(t *testing.T) {
	ctx := context.Background()
	logger := lagertest.NewTestLogger("node-labeler")
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-node",
			Labels: map[string]string{"concourse.dev/artifact-cache": "ready"},
		},
	})

	labeler := daemon.NewNodeLabeler(logger, client, "test-node", "concourse.dev/artifact-cache")

	if err := labeler.RemoveLabel(ctx); err != nil {
		t.Fatalf("RemoveLabel failed: %v", err)
	}

	node, err := client.CoreV1().Nodes().Get(ctx, "test-node", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	if _, exists := node.Labels["concourse.dev/artifact-cache"]; exists {
		t.Errorf("expected label to be removed, but it still exists")
	}
}

func TestNodeLabeler_AddLabel_NodeNotFound(t *testing.T) {
	ctx := context.Background()
	logger := lagertest.NewTestLogger("node-labeler")
	client := fake.NewSimpleClientset() // no nodes

	labeler := daemon.NewNodeLabeler(logger, client, "missing-node", "concourse.dev/artifact-cache")

	if err := labeler.AddLabel(ctx); err == nil {
		t.Error("expected error for missing node, got nil")
	}
}
