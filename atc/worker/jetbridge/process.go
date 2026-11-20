package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Compile-time checks that Process types satisfy runtime.Process.
var _ runtime.Process = (*Process)(nil)
var _ runtime.Process = (*execProcess)(nil)

const (
	// maxConsecutiveAPIErrors is the number of consecutive K8s API errors
	// tolerated before failing the task. Used by PodWatcher for both initial
	// sync and watch fallback.
	maxConsecutiveAPIErrors = 3

	// logStreamRetryDelay is how long to wait before retrying log stream
	// attachment when the container isn't ready yet.
	logStreamRetryDelay = 500 * time.Millisecond
)

// sidecarLogGrace is how long the step will wait for sidecar log streams to
// drain once the main container has finished.
const sidecarLogGrace = 2 * time.Second

// execSidecarLogGrace is how long an exec step waits for its sidecar log
// streams to end on their own once the command has finished. A variable so a
// test can shorten it.
var execSidecarLogGrace = 5 * time.Second

// sidecarFlushGrace is how long an exec step waits, once it has cancelled the
// sidecar log streams still following, for them to end and close their
// writers.
const sidecarFlushGrace = time.Second

// waitWithGrace waits for wg, giving up after grace. It reports whether the
// group finished, so the bound is observable rather than implicit.
func waitWithGrace(wg *sync.WaitGroup, grace time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

// exitedProcess is returned by Attach when the process has already completed.
type exitedProcess struct {
	id     string
	result runtime.ProcessResult
}

func (p *exitedProcess) ID() string {
	return p.id
}

func (p *exitedProcess) Wait(_ context.Context) (runtime.ProcessResult, error) {
	return p.result, nil
}

func (p *exitedProcess) SetTTY(_ runtime.TTYSpec) error {
	return nil
}

// interruptionReasonForPod uses structured Kubernetes lifecycle state only.
// Status messages are diagnostic text, never recovery evidence.
func interruptionReasonForPod(pod *corev1.Pod, deleted bool) (runtime.InterruptionReason, bool) {
	if pod == nil {
		return "", false
	}

	if pod.Status.Phase == corev1.PodFailed || deleted {
		if deleted && pod.Annotations[preemptionAnnotation] == "true" {
			return runtime.InterruptionPreempted, true
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.DisruptionTarget || condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Reason {
			case "PreemptionByScheduler":
				return runtime.InterruptionPreempted, true
			case "DeletionByPodGC":
				return runtime.InterruptionNodeLost, true
			case "DeletionByTaintManager", "EvictionByEvictionAPI", "TerminationByKubelet":
				return runtime.InterruptionEvicted, true
			}
		}
	}

	if pod.Status.Phase == corev1.PodFailed {
		switch pod.Status.Reason {
		case "Evicted":
			return runtime.InterruptionEvicted, true
		case "NodeLost", "Shutdown":
			return runtime.InterruptionNodeLost, true
		}
	}
	if deleted {
		return runtime.InterruptionPodDeleted, true
	}
	return "", false
}

func interruptionErrorForPod(pod *corev1.Pod, deleted bool, cause error) runtime.InterruptionError {
	reason, ok := interruptionReasonForPod(pod, deleted)
	if !ok {
		return nil
	}
	if cause == nil {
		cause = fmt.Errorf("pod interrupted: %s", reason)
	}
	return runtime.NewInterruptionError(reason, cause)
}

func interruptionErrorForPodFailure(pod *corev1.Pod, fetchErr, cause error) runtime.InterruptionError {
	if interruption := interruptionErrorForPod(pod, false, cause); interruption != nil {
		return interruption
	}
	if apierrors.IsNotFound(fetchErr) {
		return runtime.NewInterruptionError(
			runtime.InterruptionPodDeleted,
			fmt.Errorf("pod deleted while the process transport was active: %v: %w", fetchErr, cause),
		)
	}
	return nil
}

func preferContextCancellation(ctx context.Context, err error) error {
	var interruption runtime.InterruptionError
	if errors.As(err, &interruption) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
