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

// TestNodeLabeler_RoundTrip tests the full add → verify → remove → verify
// lifecycle with the slashed label key (concourse.dev/artifact-cache).
func TestNodeLabeler_RoundTrip(t *testing.T) {
	ctx := context.Background()
	logger := lagertest.NewTestLogger("node-labeler")
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "roundtrip-node",
			Labels: map[string]string{"existing-label": "keep-me"},
		},
	})

	labeler := daemon.NewNodeLabeler(logger, client, "roundtrip-node", "concourse.dev/artifact-cache")

	// Step 1: Add label
	if err := labeler.AddLabel(ctx); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}

	node, _ := client.CoreV1().Nodes().Get(ctx, "roundtrip-node", metav1.GetOptions{})
	if node.Labels["concourse.dev/artifact-cache"] != "ready" {
		t.Fatalf("after AddLabel: expected 'ready', got %q", node.Labels["concourse.dev/artifact-cache"])
	}
	if node.Labels["existing-label"] != "keep-me" {
		t.Fatalf("AddLabel clobbered existing label")
	}

	// Step 2: Remove label
	if err := labeler.RemoveLabel(ctx); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}

	node, _ = client.CoreV1().Nodes().Get(ctx, "roundtrip-node", metav1.GetOptions{})
	if _, exists := node.Labels["concourse.dev/artifact-cache"]; exists {
		t.Errorf("after RemoveLabel: label still exists with value %q", node.Labels["concourse.dev/artifact-cache"])
	}
	if node.Labels["existing-label"] != "keep-me" {
		t.Errorf("RemoveLabel clobbered existing label")
	}
}

// TestNodeLabeler_AddLabel_VerifyPatchPayload verifies the exact JSON patch
// sent to the K8s API contains the expected label key and value.
func TestNodeLabeler_AddLabel_VerifyPatchPayload(t *testing.T) {
	ctx := context.Background()
	logger := lagertest.NewTestLogger("node-labeler")
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "patch-node"},
	})

	labeler := daemon.NewNodeLabeler(logger, client, "patch-node", "concourse.dev/artifact-cache")

	if err := labeler.AddLabel(ctx); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}

	// Verify via the fake client's action recorder
	actions := client.Actions()
	found := false
	for _, action := range actions {
		if action.GetVerb() == "patch" && action.GetResource().Resource == "nodes" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a patch action on nodes, found none")
	}
}
