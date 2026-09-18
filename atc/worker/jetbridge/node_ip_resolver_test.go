package jetbridge

import (
	corev1 "k8s.io/api/core/v1"
	"testing"
)

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
