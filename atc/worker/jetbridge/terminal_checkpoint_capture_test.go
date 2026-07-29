package jetbridge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTerminalCheckpointCaptureAcceptsCompletedAgentAndReattachedExit(t *testing.T) {
	for _, reattached := range []bool{false, true} {
		t.Run(map[bool]string{false: "original exec process", true: "reattached exited process"}[reattached], func(t *testing.T) {
			executor := &terminalCheckpointExecutor{}
			process, _ := terminalCheckpointTestProcess(executor)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var terminal runtime.TerminalCheckpointProcess = process
			if reattached {
				attached, err := process.container.Attach(ctx, "agent", runtime.ProcessIO{})
				if err != nil {
					t.Fatal(err)
				}
				var ok bool
				terminal, ok = attached.(runtime.TerminalCheckpointProcess)
				if !ok {
					t.Fatalf("reattached process lacks terminal checkpoint capability: %T", attached)
				}
			}
			lease, err := terminal.AcquireTerminalCheckpointCapture(ctx, 1024)
			if err != nil {
				t.Fatal(err)
			}
			target := lease.CaptureTarget()
			if target.PodName != "exact-pod" || target.PodUID != "uid-42" || target.NodeName != "node-a" || target.Archive.ContainerHandle != "agent-42" {
				t.Fatalf("terminal target = %#v", target)
			}
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			if executor.calls("checkpoint-terminal-evidence") != 1 || executor.calls("checkpoint-process-quiescence") != 1 || executor.released() != 1 {
				t.Fatalf("terminal calls evidence=%d quiescence=%d releases=%d", executor.calls("checkpoint-terminal-evidence"), executor.calls("checkpoint-process-quiescence"), executor.released())
			}
			if executor.calls("safe-boundary-lease") != 0 {
				t.Fatal("terminal capture invoked a live provider boundary")
			}
		})
	}
}

func TestTerminalCheckpointCaptureRejectsUntrustedCompletionEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*execProcess, *corev1.Pod, *terminalCheckpointExecutor)
	}{
		{"missing exit annotation", func(_ *execProcess, pod *corev1.Pod, _ *terminalCheckpointExecutor) {
			delete(pod.Annotations, exitStatusAnnotationKey)
		}},
		{"malformed exit annotation", func(_ *execProcess, pod *corev1.Pod, _ *terminalCheckpointExecutor) {
			pod.Annotations[exitStatusAnnotationKey] = "00"
		}},
		{"active supervisor child", func(_ *execProcess, _ *corev1.Pod, executor *terminalCheckpointExecutor) {
			executor.evidenceErr = errors.New("child still alive")
		}},
		{"mutated exact pod", func(_ *execProcess, pod *corev1.Pod, _ *terminalCheckpointExecutor) {
			pod.Labels[hermeticLabelKey] = "false"
		}},
		{"direct mode", func(p *execProcess, _ *corev1.Pod, _ *terminalCheckpointExecutor) { p.executor = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &terminalCheckpointExecutor{}
			process, _ := terminalCheckpointTestProcess(executor)
			pod, err := process.clientset.CoreV1().Pods(process.config.Namespace).Get(context.Background(), process.podName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(process, pod, executor)
			if _, err := process.clientset.CoreV1().Pods(process.config.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := process.AcquireTerminalCheckpointCapture(ctx, 1024); err == nil {
				t.Fatal("accepted unsafe terminal capture")
			}
			if executor.calls("checkpoint-process-quiescence") != 0 {
				t.Fatal("unsafe evidence quiesced residual processes")
			}
		})
	}
}

func terminalCheckpointTestProcess(executor PodExecutor) (*execProcess, string) {
	pod := checkpointTestPod("agent-42", "uid-42", "main")
	pod.Annotations = map[string]string{exitStatusAnnotationKey: "0"}
	process := checkpointTestProcess(fake.NewClientset(pod), executor)
	process.id = "agent"
	process.processSpec = runtime.ProcessSpec{Path: "agent-runner"}
	process.container.podName = process.podName
	stateDir, _ := supervisorState(process.id, process.processSpec)
	pod.Annotations[supervisorStateAnnotationKey] = stateDir
	_, _ = process.clientset.CoreV1().Pods(process.config.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	return process, stateDir
}

type terminalCheckpointExecutor struct {
	mu           sync.Mutex
	purposes     map[string]int
	evidenceErr  error
	releaseCount int
}

func (executor *terminalCheckpointExecutor) ExecInPod(_ context.Context, _ string, _ string, _ string, _ []string, stdin io.Reader, stdout, _ io.Writer, _ bool, attrs ExecAttrs) error {
	executor.mu.Lock()
	if executor.purposes == nil {
		executor.purposes = map[string]int{}
	}
	executor.purposes[attrs.Purpose]++
	evidenceErr := executor.evidenceErr
	executor.mu.Unlock()
	switch attrs.Purpose {
	case "checkpoint-terminal-evidence":
		if evidenceErr != nil {
			return evidenceErr
		}
		_, _ = io.WriteString(stdout, "TERMINAL\n")
		return nil
	case "checkpoint-process-quiescence":
		_, _ = io.WriteString(stdout, "READY 1\n")
		_, _ = io.Copy(io.Discard, stdin)
		executor.mu.Lock()
		executor.releaseCount++
		executor.mu.Unlock()
		return nil
	default:
		return errors.New("unexpected terminal checkpoint exec")
	}
}

func (executor *terminalCheckpointExecutor) calls(purpose string) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.purposes[purpose]
}
func (executor *terminalCheckpointExecutor) released() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.releaseCount
}
