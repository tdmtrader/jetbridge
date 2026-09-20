package jetbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Literal Pod values test a pure decision, not simulated API responses.
// container-lifecycle.feature covers Attach's wiring to an actual API pod;
// live/task-command.feature covers completion records from real commands.
func TestExecRecoveryPolicy(t *testing.T) {
	for _, phase := range []corev1.PodPhase{"", corev1.PodPending, corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed} {
		name := string(phase)
		if name == "" {
			name = "unreported"
		}
		t.Run(name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
			process, err := recoverExecProcess("attach-unannotated", "some-process-id", pod)
			const want = `attach: exec-mode pod "attach-unannotated" has no completion status`
			if process != nil || err == nil || err.Error() != want {
				t.Fatalf("Attach must refuse immediately with %q; process=%T error=%v", want, process, err)
			}
		})
	}
	for _, tc := range []struct {
		record string
		exit   int
		valid  bool
	}{{"0", 0, true}, {"3", 3, true}, {"not-an-exit", 0, false}, {"", 0, false}} {
		t.Run("record="+tc.record, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{exitStatusAnnotationKey: tc.record}}}
			process, err := recoverExecProcess("recorded", "process-id", pod)
			if !tc.valid {
				if process != nil || err == nil {
					t.Fatalf("invalid record accepted: process=%T error=%v", process, err)
				}
				return
			}
			if err != nil || process == nil || process.ID() != "process-id" {
				t.Fatalf("valid record not recovered: process=%T error=%v", process, err)
			}
			result, err := process.Wait(context.Background())
			if err != nil || result.ExitStatus != tc.exit {
				t.Fatalf("recovered result=%+v error=%v, want exit %d", result, err, tc.exit)
			}
		})
	}
}
