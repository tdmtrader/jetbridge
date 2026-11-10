package jetbridge

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// A pod's activeDeadlineSeconds must be positive. A start time ahead of the
// web's clock (skew between the kubelet's node and the web) must still cut
// the deadline to the least the API accepts, never to zero or below.
func TestStoppingAStepJobPodNeverSetsADeadlineBelowOne(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started time.Time
		want    int64
	}{
		{"a start the web's clock has not reached yet", time.Now().Add(10 * time.Minute), 1},
		{"a start just now", time.Now(), 1},
		{"a start a minute ago", time.Now().Add(-time.Minute), 61},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := runningJobPod("job-pod", "node-1")
			pod.Namespace = "test-ns"
			pod.Status.StartTime = &metav1.Time{Time: tc.started}
			clientset := fake.NewSimpleClientset(pod)
			if err := stopStepJobPod(context.Background(), clientset, "test-ns", "job-pod", types.UID("uid-job-pod")); err != nil {
				t.Fatalf("stopStepJobPod: %v", err)
			}
			stopped, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), "job-pod", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got := stopped.Spec.ActiveDeadlineSeconds
			if got == nil || *got < tc.want || *got > tc.want+1 {
				t.Fatalf("activeDeadlineSeconds = %v, want %d", deref(got), tc.want)
			}
		})
	}
}

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
