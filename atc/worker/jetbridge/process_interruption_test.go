package jetbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestInterruptionReasonForPodUsesOnlyTerminalStructuredKubernetesState(t *testing.T) {
	tests := []struct {
		name    string
		pod     *corev1.Pod
		deleted bool
		want    runtime.InterruptionReason
		ok      bool
	}{
		{
			name: "failed eviction",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}},
			want: runtime.InterruptionEvicted,
			ok:   true,
		},
		{
			name: "failed node shutdown",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Shutdown"}},
			want: runtime.InterruptionNodeLost,
			ok:   true,
		},
		{
			name: "terminal disruption target",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				Conditions: []corev1.PodCondition{{
					Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "PreemptionByScheduler",
				}},
			}},
			want: runtime.InterruptionPreempted,
			ok:   true,
		},
		{
			name: "explicit preemption annotation on deletion",
			pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				PreemptionAnnotation: "true",
			}}},
			deleted: true,
			want:    runtime.InterruptionPreempted,
			ok:      true,
		},
		{
			name:    "ordinary deletion",
			pod:     &corev1.Pod{},
			deleted: true,
			want:    runtime.InterruptionPodDeleted,
			ok:      true,
		},
		{
			name: "free form message is not lifecycle evidence",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed, Reason: "Error", Message: "node lost then evicted by preemption",
			}},
		},
		{
			name: "impending annotation is not interruption",
			pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				PreemptionAnnotation: "true",
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := interruptionReasonForPod(test.pod, test.deleted)
			if got != test.want || ok != test.ok {
				t.Fatalf("interruptionReasonForPod() = (%q, %t), want (%q, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestPreferContextCancellationOverInterruption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := preferContextCancellation(ctx, runtime.NewInterruptionError(runtime.InterruptionPodDeleted, ErrPodDeleted))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	var interruption runtime.InterruptionError
	if errors.As(err, &interruption) {
		t.Fatalf("cancellation was classified as interruption %q", interruption.InterruptionReason())
	}
}

func TestInterruptionErrorForPodFailureRequiresAuthoritativePodLoss(t *testing.T) {
	cause := errors.New("transport failed")
	tests := []struct {
		name     string
		pod      *corev1.Pod
		fetchErr error
		want     runtime.InterruptionReason
	}{
		{
			name: "structured terminal status wins over transport failure",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "NodeLost"}},
			want: runtime.InterruptionNodeLost,
		},
		{
			name:     "API not found means pod deletion",
			fetchErr: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "missing"),
			want:     runtime.InterruptionPodDeleted,
		},
		{
			name:     "API timeout is not a pod-loss classification",
			fetchErr: apierrors.NewTimeoutError("timed out", 1),
		},
		{
			name:     "ordinary fetch failure is not a pod-loss classification",
			fetchErr: errors.New("connection reset"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := interruptionErrorForPodFailure(test.pod, test.fetchErr, cause)
			if test.want == "" {
				if err != nil {
					t.Fatalf("interruptionErrorForPodFailure() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("interruptionErrorForPodFailure() = nil")
			}
			if err.InterruptionReason() != test.want {
				t.Fatalf("reason = %q, want %q", err.InterruptionReason(), test.want)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("error does not preserve cause: %v", err)
			}
		})
	}
}
