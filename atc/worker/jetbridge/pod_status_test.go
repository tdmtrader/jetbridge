package jetbridge

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These are literal inputs to the production policy, not fabricated API state.
// Real Brine startup, eviction and deletion cases cover both runtime callers.
func TestPodStatusPolicy(t *testing.T) {
	type policyCase struct {
		name, node, reason, message string
		phase                       corev1.PodPhase
		statuses                    []corev1.ContainerStatus
		failure                     string
		interruption                runtime.InterruptionReason
		complete                    bool
		wantLog                     []string
	}
	tests := []policyCase{
		{name: "image-pull-before-failed-exit", phase: corev1.PodFailed,
			statuses: []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}}}},
			failure:  "ImagePullBackOff"},
		{name: "pending-crash-loop-without-history", phase: corev1.PodPending,
			statuses: []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 10s restarting failed container"}}}},
			failure:  "CrashLoopBackOff"},
		{name: "succeeded-without-container-status", phase: corev1.PodSucceeded, complete: true},
		{name: "evicted-with-node", node: "gke-pool-spot-a1b2c3", phase: corev1.PodFailed, reason: "Evicted",
			message: "The node was low on resource: ephemeral-storage.", failure: "evicted", interruption: runtime.InterruptionEvicted,
			wantLog: []string{"Node: gke-pool-spot-a1b2c3", "Evicted", "low on resource: ephemeral-storage"}},
		{name: "evicted-without-node", phase: corev1.PodFailed, reason: "Evicted",
			message: "The node was low on resource: memory.", failure: "evicted", interruption: runtime.InterruptionEvicted,
			wantLog: []string{"Pod Failure Diagnostics", "Evicted"}},
		{name: "repeated-oom", node: "node-1", phase: corev1.PodFailed,
			statuses: []corev1.ContainerStatus{{Name: "main", RestartCount: 2,
				State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", Message: "container exceeded 512Mi memory limit"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}},
			}},
			failure: "OOMKilled", wantLog: []string{"Node: node-1", "OOMKilled (exit code 137)", "container exceeded 512Mi memory limit", "RestartCount: 2", "Last termination: OOMKilled"}},
	}
	for _, sidecar := range []struct{ name, image string }{
		{"my-sidecar", "bad-sidecar:latest"}, {"redis-sidecar", "redis:bad-tag"}, {"bad-image", "nonexistent:latest"},
	} {
		tests = append(tests, policyCase{name: "sidecar-" + sidecar.name, phase: corev1.PodPending,
			statuses: []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating", Message: "waiting for container"}}},
				{Name: sidecar.name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image \"" + sidecar.image + "\""}}},
			},
			failure: "ImagePullBackOff", wantLog: []string{sidecar.name, sidecar.image},
		})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: tc.node}, Status: corev1.PodStatus{
				Phase: tc.phase, Reason: tc.reason, Message: tc.message, ContainerStatuses: tc.statuses,
			}}
			got := podStateFor(pod)
			if got.complete != tc.complete || got.result.ExitStatus != 0 || got.failureReason != tc.failure {
				t.Fatalf("state=%+v; want complete=%t exit=0 failure=%q", got, tc.complete, tc.failure)
			}
			if tc.failure == "" {
				if got.err != nil {
					t.Fatalf("unexpected failure: %v", got.err)
				}
			} else if got.err == nil || !strings.Contains(got.err.Error(), tc.failure) {
				t.Fatalf("failure=%v, want %q", got.err, tc.failure)
			}
			if tc.interruption != "" {
				var interrupted runtime.InterruptionError
				if !errors.As(got.err, &interrupted) || interrupted.InterruptionReason() != tc.interruption {
					t.Fatalf("expected typed %q interruption, got %T: %v", tc.interruption, got.err, got.err)
				}
			}
			var log bytes.Buffer
			writePodDiagnostics(pod, &log)
			for _, text := range tc.wantLog {
				if !strings.Contains(log.String(), text) {
					t.Errorf("missing %q from diagnostics %q", text, log.String())
				}
			}
		})
	}
}

func TestNodeStatusDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		node corev1.Node
		want []string
	}{
		{name: "pressured-spot", node: corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "gke-spot-node-1", Labels: map[string]string{"cloud.google.com/gke-spot": "true"}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Message: "disk usage exceeds threshold"},
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			}},
		}, want: []string{"DiskPressure=True", "disk usage exceeds threshold", "spot/preemptible instance", "cloud.google.com/gke-spot=true"}},
		{name: "cordoned", node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "draining-node-1"},
			Spec: corev1.NodeSpec{Unschedulable: true}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
		}, want: []string{"cordoned (unschedulable)", "node may be draining"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			writeNodeStatus(&tc.node, &log)
			for _, text := range tc.want {
				if !strings.Contains(log.String(), text) {
					t.Errorf("missing %q from diagnostics %q", text, log.String())
				}
			}
		})
	}
}
