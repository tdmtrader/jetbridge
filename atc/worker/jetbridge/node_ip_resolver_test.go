package jetbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestNodeIPResolver_Resolve is RESTORED 2026-09-18 (round 3, ledger-final),
// verbatim from core `b294dafc49` apart from one added assertion.
//
// The 2026-09-15 retirement paired it with "cached read", and the only brine
// scenario that asserts a cached read is
// `features/live/node-resolution.feature:7` "A node keeps resolving after its
// API route becomes unavailable", which is `@live-kubernetes` and so never
// runs under `make test-unit`. Nothing on a unit run carried either half of
// this leaf: neither the successful InternalIP resolution nor the cache.
//
// The added assertion is the API-read count. The original called Resolve
// twice and only checked that the second answer matched, which a resolver
// with no cache at all would also satisfy; the live scenario's own check is
// "with only one API read". Counting the clientset's Get actions makes the
// restored leaf pin the cache the row always claimed for it. Nothing is
// weakened — every original check is carried over unchanged.
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

	var gets int
	for _, action := range cs.Actions() {
		if action.Matches("get", "nodes") {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("expected exactly one Nodes.Get across two Resolve calls, got %d", gets)
	}
}

// Literal address lists exercise selection only; the Brine live resolver case
// covers actual Nodes.Get and caching without publishing Kubernetes status.
func TestNodeInternalAddressSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []corev1.NodeAddress
		want      string
	}{
		{"internal-after-external", []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "34.1.2.3"}, {Type: corev1.NodeInternalIP, Address: "10.0.0.5"}}, "10.0.0.5"},
		{"external-only", []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "34.1.2.3"}}, ""},
		{"unreported", nil, ""},
		{"first-internal", []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.5"}, {Type: corev1.NodeInternalIP, Address: "10.0.0.6"}}, "10.0.0.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &corev1.Node{Status: corev1.NodeStatus{Addresses: tc.addresses}}
			if got := nodeInternalIP(node); got != tc.want {
				t.Fatalf("internal address=%q, want %q", got, tc.want)
			}
		})
	}
}
