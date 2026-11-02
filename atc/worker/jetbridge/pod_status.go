package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type podState struct {
	result        runtime.ProcessResult
	complete      bool
	failureReason string
	err           error
}

// podStateFor is the shared policy for reported status, including incomplete
// combinations that need not be manufactured on a running Kubernetes cluster.
// Failure priority precedes exit-code fallback on both runtime paths.
func podStateFor(pod *corev1.Pod) podState {
	if name, oom := isPodOOMKilled(pod); oom {
		return podState{failureReason: "OOMKilled",
			err: fmt.Errorf("pod failed: OOMKilled: container %q exceeded memory limit", name)}
	}
	if reason, message, failed := isPodFailedFast(pod); failed {
		return podState{failureReason: reason, err: fmt.Errorf("pod failed: %s: %s", reason, message)}
	}
	if interruption := interruptionErrorForPod(pod, false, nil); interruption != nil {
		return podState{failureReason: string(interruption.InterruptionReason()), err: interruption}
	}
	exit, done := podExitCode(pod)
	return podState{result: runtime.ProcessResult{ExitStatus: exit}, complete: done}
}

// Keep metrics, diagnostics and cancellation at the I/O boundary. The status
// policy itself neither invents a lifecycle transition nor reads API state.
func reportPodFailure(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, stderr io.Writer, state podState) error {
	if state.err == nil {
		return nil
	}
	if state.failureReason == "ImagePullBackOff" || state.failureReason == "ErrImagePull" {
		metric.Metrics.K8sImagePullFailures.Inc()
	}
	metric.RecordK8sPodFailure(ctx, state.failureReason)
	writePodDiagnostics(pod, stderr)
	var interruption runtime.InterruptionError
	if errors.As(state.err, &interruption) {
		writeNodeDiagnostics(ctx, client, pod, stderr)
		return preferContextCancellation(ctx, state.err)
	}
	return state.err
}
