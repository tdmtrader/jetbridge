package steps

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestOOMPriorityObservationDiagnostic(t *testing.T) {
	for _, tt := range []struct {
		name  string
		armed bool
		pod   *corev1.Pod
		want  []string
	}{
		{name: "before first read", want: []string{"before any pod read"}},
		{name: "unscheduled", pod: &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			want: []string{"armed=false", "node=\"\"", "\"phase\":\"Pending\""}},
		{name: "armed crash loop", armed: true,
			pod: &corev1.Pod{Spec: corev1.PodSpec{NodeName: "observed-node"}, Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "main", RestartCount: 1,
					State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
				}},
			}},
			want: []string{"armed=true", "node=\"observed-node\"", "\"phase\":\"Running\"", "\"reason\":\"CrashLoopBackOff\"", "\"reason\":\"OOMKilled\"", "\"exitCode\":137", "\"restartCount\":1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := describeOOMObservation(tt.armed, tt.pod, context.DeadlineExceeded)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost original cause: %v", err)
			}
			for _, part := range tt.want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("diagnostic %q missing %q", err, part)
				}
			}
		})
	}
}
