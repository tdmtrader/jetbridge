package jetbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNodeIPResolver_Resolve(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeExternalIP, Address: "34.1.2.3"},
			},
		},
	}

	cs := fake.NewSimpleClientset(node)
	resolver := NewNodeIPResolver(cs)

	ip, err := resolver.Resolve(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip != "10.0.0.5" {
		t.Errorf("expected 10.0.0.5, got %s", ip)
	}

	// Second call should hit cache.
	ip2, err := resolver.Resolve(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("Resolve (cached): %v", err)
	}
	if ip2 != "10.0.0.5" {
		t.Errorf("expected 10.0.0.5 from cache, got %s", ip2)
	}
}

func TestNodeIPResolver_NodeNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	resolver := NewNodeIPResolver(cs)

	_, err := resolver.Resolve(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent node")
	}
}

func TestNodeIPResolver_NoInternalIP(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeExternalIP, Address: "34.1.2.3"},
			},
		},
	}

	cs := fake.NewSimpleClientset(node)
	resolver := NewNodeIPResolver(cs)

	_, err := resolver.Resolve(context.Background(), "node-1")
	if err == nil {
		t.Error("expected error for node without InternalIP")
	}
}
