package jetbridge

// A pause Pod destroyed while its step's command is running is invisible to
// the exec transport, in both directions.
//
// Deleted with nothing left to say, it closes the SPDY error stream empty, and
// the transport returns the same nil it returns for a command that exited 0.
// Deleted while the command is still in the kernel, it kills the command, and
// the transport carries out the signal's exit code -- 137 -- which is what a
// command failing on its own terms looks like. Neither is the command's answer,
// and the Pod's own state is the only thing that can say so.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// destroyingExecutor runs onExec while the command is "running" and then
// reports exitCode the way the real transport does: nil for 0, an
// ExecExitError otherwise.
type destroyingExecutor struct {
	onExec   func()
	exitCode int
	// statusMissing reports the way the status-checking transport
	// (exec_status.go) does when the error stream closed with nothing on it:
	// client-go's wrapping of that stream error, text and all.
	statusMissing bool
}

func (e *destroyingExecutor) ExecInPod(_ context.Context, _, _, _ string, _ []string,
	_ io.Reader, stdout, _ io.Writer, _ bool, _ ExecAttrs) error {
	if stdout != nil {
		_, _ = io.WriteString(stdout, "running\n")
	}
	if e.onExec != nil {
		e.onExec()
	}
	if e.statusMissing {
		return fmt.Errorf("error reading from error stream: %w",
			fmt.Errorf("%w: %w", errExecStatusMissing, io.ErrUnexpectedEOF))
	}
	if e.exitCode != 0 {
		return &ExecExitError{ExitCode: e.exitCode}
	}

	return nil
}

func runningPausePod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "destroyed-pod", Namespace: "test-ns"},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: mainContainerName}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: mainContainerName, Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func TestExecProcessDoesNotReportExitZeroWhenPodIsDestroyed(t *testing.T) {
	ctx := context.Background()

	newWaitWithStderr := func(clientset *fake.Clientset, executor PodExecutor, stderr io.Writer) (runtime.ProcessResult, error) {
		config := NewConfig("test-ns", "")
		container := &Container{
			handle:        "destroyed-handle",
			podName:       "destroyed-pod",
			metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{Type: db.ContainerTypeTask},
			clientset:     clientset,
			config:        config,
			properties:    map[string]string{},
		}
		process := newExecProcess("proc-1", "destroyed-pod", clientset, config, container,
			executor, runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "echo running && sleep 30"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: stderr}, nil)

		return process.Wait(ctx)
	}
	newWait := func(clientset *fake.Clientset, executor PodExecutor) (runtime.ProcessResult, error) {
		return newWaitWithStderr(clientset, executor, &bytes.Buffer{})
	}

	// The control: nothing happened to the Pod, so the nil the transport
	// returned is the exit 0 it looks like. This arm is what keeps the check
	// below from being satisfied by failing every step.
	t.Run("a surviving pod still reports exit 0", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		result, err := newWait(clientset, &destroyingExecutor{})
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("Wait() ExitStatus = %d, want 0", result.ExitStatus)
		}
	})

	// What `kubectl delete pod` leaves behind: phase Failed, no Reason, and a
	// main container the kubelet could no longer locate.
	t.Run("a pod destroyed mid-command does not report exit 0", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		executor := &destroyingExecutor{onExec: func() {
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "destroyed-pod", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting the pause pod: %v", err)
			}
			pod.Status.Phase = corev1.PodFailed
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: mainContainerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason:   "ContainerStatusUnknown",
					ExitCode: 137,
					Message:  "The container could not be located when the pod was terminated",
				}},
			}}
			if _, err := clientset.CoreV1().Pods("test-ns").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatalf("updating the pause pod status: %v", err)
			}
		}}

		result, err := newWait(clientset, executor)
		if err == nil {
			t.Fatalf("Wait() = (%+v, nil), want an error: the pod was destroyed while the command ran", result)
		}
		if !strings.Contains(err.Error(), string(runtime.InterruptionPodDeleted)) {
			t.Fatalf("Wait() error = %q, want it to name %q", err, runtime.InterruptionPodDeleted)
		}

		// The load-bearing half. A runtime.InterruptionError is RETRYABLE, and
		// a retryable step error leaves the build started for the tracker to
		// resume -- which re-runs the whole plan, command side effects and
		// all. A pod destroyed before its command ran may be retried; this one
		// may not.
		var retryable runtime.RetryableError
		if errors.As(err, &retryable) && retryable.IsRetryable() {
			t.Fatalf("Wait() error = %T (%v), want a NON-retryable error: "+
				"the command had already run, so the build must not be re-run", err, err)
		}
	})

	// The same destruction seen a moment later, once the Pod object itself is
	// gone from the API server.
	t.Run("a pod deleted mid-command does not report exit 0", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		executor := &destroyingExecutor{onExec: func() {
			if err := clientset.CoreV1().Pods("test-ns").Delete(ctx, "destroyed-pod", metav1.DeleteOptions{}); err != nil {
				t.Fatalf("deleting the pause pod: %v", err)
			}
		}}

		result, err := newWait(clientset, executor)
		if err == nil {
			t.Fatalf("Wait() = (%+v, nil), want an error: the pod was deleted while the command ran", result)
		}
	})

	// The other direction, and the one the cluster actually produces: a
	// graceful delete SIGKILLs the command and the supervisor carries 137 out
	// over the error stream before the Pod goes. Nothing about that 137 is the
	// command's own answer, and a build that ends `failed` on it has been told
	// the wrong story about why.
	t.Run("a pod being deleted does not let the signal's exit code stand", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		executor := &destroyingExecutor{exitCode: 137, onExec: func() {
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, "destroyed-pod", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting the pause pod: %v", err)
			}
			// Deletion in flight: the object is still there, with a grace
			// period left to run, and the container is already being killed.
			deleting := metav1.Now()
			pod.DeletionTimestamp = &deleting
			if _, err := clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatalf("marking the pause pod as deleting: %v", err)
			}
		}}

		result, err := newWait(clientset, executor)
		if err == nil {
			t.Fatalf("Wait() = (%+v, nil), want an error: the pod was being deleted while the command ran", result)
		}
		if !strings.Contains(err.Error(), string(runtime.InterruptionPodDeleted)) {
			t.Fatalf("Wait() error = %q, want it to name %q", err, runtime.InterruptionPodDeleted)
		}
		if !strings.Contains(err.Error(), "137") {
			t.Fatalf("Wait() error = %q, want it to carry the exit code the transport reported", err)
		}
		var retryable runtime.RetryableError
		if errors.As(err, &retryable) && retryable.IsRetryable() {
			t.Fatalf("Wait() error = %T (%v), want a NON-retryable error: "+
				"the command had already run, so the build must not be re-run", err, err)
		}
	})

	// The transport now says so itself (exec_status.go): an error stream that
	// closed with no status is reported as an error rather than the nil above.
	// The Pod is still asked, so the destruction is still what the step is
	// told about, still not retried, and the build log still says what went.
	t.Run("a transport reporting no status still names the destroyed pod", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())
		stderr := &bytes.Buffer{}

		executor := &destroyingExecutor{statusMissing: true, onExec: func() {
			if err := clientset.CoreV1().Pods("test-ns").Delete(ctx, "destroyed-pod", metav1.DeleteOptions{}); err != nil {
				t.Fatalf("deleting the pause pod: %v", err)
			}
		}}

		result, err := newWaitWithStderr(clientset, executor, stderr)
		if err == nil {
			t.Fatalf("Wait() = (%+v, nil), want an error: the pod was deleted while the command ran", result)
		}
		for _, want := range []string{
			"exec in pod",
			"the step's command was running when its pod was destroyed",
			string(runtime.InterruptionPodDeleted),
			"reported no exit status",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Wait() error = %q, want it to say %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "exit code 0") {
			t.Errorf("Wait() error = %q claims an exit code the transport never reported", err)
		}
		if !strings.Contains(stderr.String(), "pod no longer exists") {
			t.Errorf("build log = %q, want it to say the pod no longer exists", stderr.String())
		}
		var retryable runtime.RetryableError
		if errors.As(err, &retryable) && retryable.IsRetryable() {
			t.Fatalf("Wait() error = %T (%v), want a NON-retryable error", err, err)
		}
	})

	// The control: the stream broke but the Pod is fine. That is not an exit
	// 0 either -- the command's status never arrived -- and it is not a
	// destroyed Pod. It is the transport's error, in the transport's words.
	t.Run("a transport reporting no status on a surviving pod is not exit 0", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		result, err := newWait(clientset, &destroyingExecutor{statusMissing: true})
		if err == nil {
			t.Fatalf("Wait() = (%+v, nil), want an error: no exit status ever arrived", result)
		}
		if !errors.Is(err, errExecStatusMissing) {
			t.Errorf("Wait() error = %q, want it to wrap %q", err, errExecStatusMissing)
		}
		if strings.Contains(err.Error(), "destroyed") {
			t.Errorf("Wait() error = %q blames a destruction that did not happen", err)
		}
	})

	// The control for the arm above, and the one that keeps it from being
	// satisfied by failing every unhappy step. A command that really exited
	// non-zero on a Pod nothing touched keeps its own exit code, and the step
	// fails rather than errors.
	t.Run("a surviving pod keeps its command's own non-zero exit", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(runningPausePod())

		result, err := newWait(clientset, &destroyingExecutor{exitCode: 2})
		if err != nil {
			t.Fatalf("Wait() = %v, want no error: the command failed, the pod did not", err)
		}
		if result.ExitStatus != 2 {
			t.Fatalf("Wait() ExitStatus = %d, want 2", result.ExitStatus)
		}
	})
}
