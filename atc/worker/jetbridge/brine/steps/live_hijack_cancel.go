package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type HijackCancellationOutcome struct {
	Err             error
	preservationErr error
}

func HijackCancellationDefinitions() []brine.StepDefinition {
	const action = "a running hijack session on task {string} is cancelled"
	return []brine.StepDefinition{
		brine.DefineMap[WorkerReady, HijackCancellationOutcome](action,
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (HijackCancellationOutcome, error) {
				return applyAction(action, in, p, func(in WorkerReady, a Args) (HijackCancellationOutcome, error) {
					return cancelLiveHijack(in, rec, a.String(0))
				})
			}),
		CheckThat[HijackCancellationOutcome]("ending the hijack preserves the original task and pod",
			func(in HijackCancellationOutcome) error {
				if !errors.Is(in.Err, context.Canceled) {
					return fmt.Errorf("expected hijack context cancellation, got %v", in.Err)
				}
				return in.preservationErr
			}),
	}
}

// The owner and hijack use independent contexts and real production processes.
// An on-pod gate keeps the original task alive until after the session ends.
func cancelLiveHijack(w WorkerReady, rec *brine.Recorder, handle string) (out HijackCancellationOutcome, err error) {
	w, deletes, err := observePodDeletes(w, handle)
	if err != nil {
		return out, err
	}
	container, _, err := findTaskContainer(TaskCluster{WorkerReady: w}, handle)
	if err != nil {
		return out, err
	}
	ownerCtx, cancelOwner := context.WithCancel(w.Ctx)
	var ownerLog bytes.Buffer
	owner, err := container.Run(ownerCtx, runtime.ProcessSpec{ID: "original", Path: "sh", Args: []string{"-c",
		"echo $$ > /tmp/brine-hijack-original; while [ ! -f /tmp/brine-hijack-release ]; do sleep 0.1; done; echo original-finished"}},
		runtime.ProcessIO{Stdout: &ownerLog, Stderr: io.Discard})
	if err != nil {
		cancelOwner()
		return out, err
	}
	ownerDone := make(chan struct{})
	var ownerResult runtime.ProcessResult
	var ownerErr error
	go func() { defer close(ownerDone); ownerResult, ownerErr = owner.Wait(ownerCtx) }()
	rec.RegisterDisposer(func() {
		cancelOwner()
		select {
		case <-ownerDone:
			fmt.Printf("hijack original waiter drained in %s/%s\n", w.Namespace, handle)
		case <-time.After(10 * time.Second):
			panic("hijack original waiter did not drain")
		}
	})
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, handle)
	if err != nil {
		return out, err
	}
	if pod.UID == "" {
		return out, fmt.Errorf("original task has no pod UID")
	}
	ownerPID, err := liveHijackPID(w, handle, "/tmp/brine-hijack-original")
	if err != nil {
		return out, fmt.Errorf("original task did not start: %w", err)
	}
	select {
	case <-ownerDone:
		return out, fmt.Errorf("original task completed before hijack: %v", ownerErr)
	default:
	}

	// LookupContainer, not FindOrCreateContainer, is the operator's route.
	hijacked, found, err := w.Worker.LookupContainer(w.Ctx, handle)
	if err != nil || !found {
		return out, fmt.Errorf("look up original task: found=%t error=%v", found, err)
	}
	hijackCtx, endSession := context.WithCancel(w.Ctx)
	defer endSession()
	session, err := hijacked.Run(hijackCtx, runtime.ProcessSpec{ID: "hijack", Path: "sh", Args: []string{"-c",
		"echo $$ > /tmp/brine-hijack-session; while :; do sleep 1; done"}},
		runtime.ProcessIO{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		return out, err
	}
	sessionDone := make(chan struct{})
	var sessionErr error
	go func() { defer close(sessionDone); _, sessionErr = session.Wait(hijackCtx) }()
	defer func() {
		endSession()
		select {
		case <-sessionDone:
		case <-time.After(10 * time.Second):
			panic("hijack session waiter did not drain")
		}
	}()
	sessionPID, err := liveHijackPID(w, handle, "/tmp/brine-hijack-session")
	if err != nil {
		return out, fmt.Errorf("hijack did not start: %w", err)
	}
	if ownerPID == sessionPID {
		return out, fmt.Errorf("hijack reused original process %s", ownerPID)
	}
	select {
	case <-sessionDone:
		return out, fmt.Errorf("hijack completed before cancellation: %v", sessionErr)
	default:
	}
	fmt.Printf("real hijack armed pod %s/%s UID %s original PID %s session PID %s\n", w.Namespace, handle, pod.UID, ownerPID, sessionPID)
	endSession()
	select {
	case <-sessionDone:
		out.Err = sessionErr
	case <-time.After(10 * time.Second):
		return out, fmt.Errorf("cancelled hijack did not return")
	}

	// Observation failures are outcomes for Then, not fabricated setup failures.
	out.preservationErr = func() error {
		if err := deletes.requireNoDeletes(); err != nil {
			return err
		}
		if err := requireHijackPod(w, handle, pod); err != nil {
			return err
		}
		if state, err := childState(w.Ctx, w, handle, ownerPID); err != nil || state == "gone" || state == "Z" || state == "X" {
			return fmt.Errorf("hijack stopped original task PID %s: state=%s error=%v", ownerPID, state, err)
		}
		select {
		case <-ownerDone:
			return fmt.Errorf("hijack ended original task before its gate opened: %v", ownerErr)
		default:
		}
		if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"touch", "/tmp/brine-hijack-release"},
			nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "release-original-after-hijack"}); err != nil {
			return err
		}
		select {
		case <-ownerDone:
		case <-time.After(10 * time.Second):
			return fmt.Errorf("original task did not complete after hijack")
		}
		if ownerErr != nil || ownerResult.ExitStatus != 0 || ownerLog.String() != "original-finished\n" {
			return fmt.Errorf("original task failed after hijack: exit=%d error=%v stdout=%q", ownerResult.ExitStatus, ownerErr, ownerLog.String())
		}
		if err := deletes.requireNoDeletes(); err != nil {
			return err
		}
		if err := requireHijackPod(w, handle, pod); err != nil {
			return err
		}
		fmt.Printf("real hijack preserved original task and pod %s/%s UID %s; original completed after release, exit=0; DELETE count=0\n", w.Namespace, handle, pod.UID)
		return nil
	}()
	return out, nil
}

func liveHijackPID(w WorkerReady, handle, path string) (string, error) {
	ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	err := w.Executor.ExecInPod(ctx, w.Namespace, handle, "main",
		[]string{"sh", "-c", "until [ -s \"$1\" ]; do sleep 0.1; done; cat \"$1\"", "wait-for-command", path},
		nil, &output, nil, false, jetbridge.ExecAttrs{Purpose: "hijack-command-premise"})
	if err != nil {
		return "", err
	}
	pid := strings.TrimSpace(output.String())
	if n, err := strconv.Atoi(pid); err != nil || n <= 1 {
		return "", fmt.Errorf("invalid real command PID %q", pid)
	}
	state, err := childState(ctx, w, handle, pid)
	if err != nil || state == "gone" || state == "Z" || state == "X" {
		return "", fmt.Errorf("command is not alive: state=%s error=%v", state, err)
	}
	return pid, nil
}

func requireHijackPod(w WorkerReady, handle string, original *corev1.Pod) error {
	pods, err := w.Clientset.CoreV1().Pods(w.Namespace).List(w.Ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(pods.Items) != 1 {
		return fmt.Errorf("hijack left %d pods, want the original one", len(pods.Items))
	}
	pod := pods.Items[0]
	if pod.Name != handle || pod.UID != original.UID || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("hijack changed original pod: name=%s UID=%s phase=%s deleting=%t", pod.Name, pod.UID, pod.Status.Phase, pod.DeletionTimestamp != nil)
	}
	return nil
}

func (t *podDeleteObservation) requireNoDeletes() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) != 0 {
		return fmt.Errorf("hijack requested %d pod DELETEs, want zero", len(t.requests))
	}
	return nil
}
