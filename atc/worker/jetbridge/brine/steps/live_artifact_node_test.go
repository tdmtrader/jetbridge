package steps

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestArtifactNodeUnfit(t *testing.T) {
	node := func(ready corev1.ConditionStatus, cordoned bool, cache string) *corev1.Node {
		n := &corev1.Node{}
		n.Spec.Unschedulable = cordoned
		n.Labels = map[string]string{"concourse.dev/artifact-cache": cache}
		n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}
		return n
	}
	for _, tc := range []struct {
		name string
		node *corev1.Node
		want string
	}{
		{"fit", node(corev1.ConditionTrue, false, "ready"), ""},
		{"asleep", node(corev1.ConditionUnknown, false, "ready"), "is not Ready"},
		{"cordoned for a game", node(corev1.ConditionTrue, true, "ready"), "is cordoned"},
		{"no artifact daemon", node(corev1.ConditionTrue, false, ""), "has no ready artifact cache"},
		{"no Ready condition at all", &corev1.Node{ObjectMeta: node(corev1.ConditionTrue, false, "ready").ObjectMeta}, "is not Ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := artifactNodeUnfit(tc.node); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
