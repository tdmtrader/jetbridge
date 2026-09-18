package jetbridge

import (
	"bytes"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Literal status is input to a pure decision, never a fabricated API response.
// Real Brine cases cover pod creation, startup/dial retries and exec transport.
func TestPauseReplacementPolicy(t *testing.T) {
	preempted := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed, Reason: "Preempted",
		Conditions: []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "PreemptionByScheduler"}},
	}}
	type policyCase struct {
		name           string
		pod            *corev1.Pod
		live, replaced bool
		want           string
	}
	tests := []policyCase{
		{name: "absent pod"},
		{name: "clean pause exit", pod: &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}},
		{name: "preempted pause", pod: preempted},
		{name: "command already wrote", pod: preempted, live: true, want: "pause pod task died after the step's command started — not re-running it"},
		{name: "replacement already spent", pod: preempted, replaced: true, want: "pause pod task has already been replaced once"},
		{name: "live guard precedes spent budget", live: true, replaced: true, want: "pause pod task died after the step's command started — not re-running it"},
	}
	for _, phase := range []corev1.PodPhase{corev1.PodFailed, corev1.PodSucceeded} {
		tests = append(tests, policyCase{
			name: "failed input despite phase " + string(phase),
			pod: &corev1.Pod{Status: corev1.PodStatus{Phase: phase, InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "already-complete", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
				{Name: "stage-inputs", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}},
			}}},
			want: "pause pod task did not die, its init container \"stage-inputs\" exited 1",
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pauseReplacementError("task", tt.live, tt.replaced, tt.pod)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("replacement refused: %v", err)
				}
			} else if err == nil || err.Error() != tt.want {
				t.Fatalf("replacement error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPauseExecStreamsMarkCommandStarted(t *testing.T) {
	p := &execProcess{}
	if p.watchExecReader(nil) != nil || p.watchExecWriter(nil) != nil {
		t.Fatal("nil streams changed")
	}
	var output bytes.Buffer
	writer := p.watchExecWriter(&output)
	if n, err := writer.Write(nil); n != 0 || err != nil || p.execTransportLive.Load() {
		t.Fatal("empty write started command")
	}
	const text = "half of the deploy\n"
	if n, err := writer.Write([]byte(text)); n != len(text) || err != nil || output.String() != text {
		t.Fatal("output was not forwarded intact")
	}
	if err := pauseReplacementError("task", p.execTransportLive.Load(), false, &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}); err == nil {
		t.Fatal("output did not prevent replacement")
	}
	p = &execProcess{}
	in := p.watchExecReader(strings.NewReader("input"))
	data, err := io.ReadAll(in)
	if err != nil || string(data) != "input" || !p.execTransportLive.Load() {
		t.Fatalf("stdin not preserved/marked: %q %v", data, err)
	}
}
