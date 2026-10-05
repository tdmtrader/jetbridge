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

// A node reserved for builds by a NoSchedule taint admits the fixture's pinned
// step pods; its condition taints -- a cordon above all -- never do.
func TestReservationTolerations(t *testing.T) {
	node := &corev1.Node{}
	node.Spec.Taints = []corev1.Taint{
		{Key: "jetbridge.dev/battle-station", Value: "true", Effect: corev1.TaintEffectNoSchedule},
		{Key: "example.com/dedicated", Effect: corev1.TaintEffectNoSchedule},
		{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule},
		{Key: corev1.TaintNodeNotReady, Effect: corev1.TaintEffectNoExecute},
		{Key: "example.com/evict", Value: "x", Effect: corev1.TaintEffectNoExecute},
		{Key: "example.com/soft", Value: "x", Effect: corev1.TaintEffectPreferNoSchedule},
	}
	want := []corev1.Toleration{
		{Key: "jetbridge.dev/battle-station", Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule},
		{Key: "example.com/dedicated", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule},
	}
	got := reservationTolerations(node)
	if len(got) != len(want) {
		t.Fatalf("tolerations = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("toleration %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := reservationTolerations(&corev1.Node{}); got != nil {
		t.Errorf("an untainted node: tolerations = %+v, want none", got)
	}
}
