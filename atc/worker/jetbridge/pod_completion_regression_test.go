package jetbridge

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodDiagnosticsIncludesReasonlessSuccessfulScheduling(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
		},
	}}
	var log bytes.Buffer
	writePodDiagnostics(pod, &log)
	if !strings.Contains(log.String(), "Condition: PodScheduled=True") {
		t.Fatalf("successful scheduling missing from failure diagnostics: %q", log.String())
	}
}

func TestPendingPodCompletionUsesMainContainer(t *testing.T) {
	for _, tc := range []struct {
		name string
		main *corev1.ContainerStateTerminated
		want int
		done bool
	}{
		{name: "main has not finished"},
		{name: "main succeeded", main: &corev1.ContainerStateTerminated{ExitCode: 0}, done: true},
		{name: "main failed", main: &corev1.ContainerStateTerminated{ExitCode: 42}, want: 42, done: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
					{Name: "main", State: corev1.ContainerState{Terminated: tc.main}},
				},
			}}
			got, done := podExitCode(pod)
			if got != tc.want || done != tc.done {
				t.Fatalf("podExitCode = (%d, %t), want (%d, %t)", got, done, tc.want, tc.done)
			}
		})
	}
}
