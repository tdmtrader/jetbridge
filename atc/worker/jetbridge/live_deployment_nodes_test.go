//go:build live
// +build live

package jetbridge_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A node that is asleep still counts; one the daemon cannot be placed on --
// unselected, or reserved by a taint it does not tolerate -- does not.
func TestCountDaemonNodes(t *testing.T) {
	node := func(labels map[string]string, taints ...corev1.Taint) corev1.Node {
		n := corev1.Node{}
		n.Labels = labels
		n.Spec.Taints = taints
		return n
	}
	reserved := corev1.Taint{Key: "jetbridge.dev/battle-station", Value: "true", Effect: corev1.TaintEffectNoSchedule}
	asleep := corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}
	nodes := []corev1.Node{
		node(map[string]string{"pool": "ci"}),
		node(map[string]string{"pool": "ci"}, reserved, asleep),
		node(map[string]string{"pool": "gpu"}),
	}

	if got := countDaemonNodes(corev1.PodSpec{}, nodes); got != 2 {
		t.Errorf("no selector, no toleration: %d nodes, want 2 (the reserved node is not placeable)", got)
	}
	tolerating := corev1.PodSpec{Tolerations: []corev1.Toleration{
		{Key: "jetbridge.dev/battle-station", Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule},
	}}
	if got := countDaemonNodes(tolerating, nodes); got != 3 {
		t.Errorf("tolerating the reservation: %d nodes, want 3 (asleep still counts)", got)
	}
	tolerating.NodeSelector = map[string]string{"pool": "ci"}
	if got := countDaemonNodes(tolerating, nodes); got != 2 {
		t.Errorf("selecting pool=ci: %d nodes, want 2", got)
	}
}
