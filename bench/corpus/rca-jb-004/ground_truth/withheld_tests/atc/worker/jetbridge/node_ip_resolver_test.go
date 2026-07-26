package jetbridge

import (
	"context"
	"errors"
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

// TestNodeIPResolver_IPShapedInputRejected guards against a regression where
// callers accidentally pass a pod or node IP (e.g. "10.0.0.5") as the
// nodeName argument. The K8s Nodes API is keyed by Node object name, never
// by IP, so such calls would always 404 with a misleading "nodes \"<IP>\"
// not found" error. We short-circuit before hitting the API.
func TestNodeIPResolver_IPShapedInputRejected(t *testing.T) {
	for _, name := range []string{"10.0.0.5", "127.0.0.1", "100.68.228.107", "::1", "2001:db8::1"} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			resolver := NewNodeIPResolver(cs)

			_, err := resolver.Resolve(context.Background(), name)
			if err == nil {
				t.Fatalf("Resolve(%q): expected error, got nil", name)
			}
			if !errors.Is(err, ErrNodeNameIsIP) {
				t.Errorf("Resolve(%q): expected ErrNodeNameIsIP, got %v", name, err)
			}

			if actions := cs.Actions(); len(actions) != 0 {
				t.Errorf("Resolve(%q): expected no K8s API calls, got %d: %+v",
					name, len(actions), actions)
			}
		})
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
