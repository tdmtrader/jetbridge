package k8sruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Compile-time checks that Process types satisfy runtime.Process.
var _ runtime.Process = (*Process)(nil)
var _ runtime.Process = (*execProcess)(nil)

// Process implements runtime.Process by watching a Kubernetes Pod for
// completion. It streams the Pod's container logs to ProcessIO.Stdout
// so that build output appears in the Concourse build log.
type Process struct {
	id        string
	podName   string
	clientset kubernetes.Interface
	config    Config
	container *Container
	processIO runtime.ProcessIO
}

func newProcess(id, podName string, clientset kubernetes.Interface, config Config, container *Container, pio runtime.ProcessIO) *Process {
	return &Process{
		id:        id,
		podName:   podName,
		clientset: clientset,
		config:    config,
		container: container,
		processIO: pio,
	}
}

func (p *Process) ID() string {
	return p.id
}

// Wait watches the Pod until the main container terminates and returns the
// exit code. Pod container logs are streamed to ProcessIO.Stdout so they
// appear in the Concourse build log. If the context is cancelled, the Pod
// is deleted and the context error is returned.
func (p *Process) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	type result struct {
		processResult runtime.ProcessResult
		err           error
	}

	// Stream Pod logs to ProcessIO.Stdout in the background. The log
	// stream follows the container and closes automatically when it
	// terminates, so it won't block beyond the Pod's lifetime.
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		p.streamLogs(ctx)
	}()

	waitCh := make(chan result, 1)

	go func() {
		r, err := p.pollUntilDone(ctx)
		waitCh <- result{processResult: r, err: err}
	}()

	select {
	case <-ctx.Done():
		// Attempt to clean up the Pod on cancellation.
		_ = p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
			context.Background(), p.podName, metav1.DeleteOptions{},
		)
		return runtime.ProcessResult{}, ctx.Err()

	case r := <-waitCh:
		// Wait for log streaming to finish so all output is captured
		// before returning.
		<-logDone

		if r.err != nil {
			return runtime.ProcessResult{}, r.err
		}
		// Store exit status in container properties for reattachment.
		if p.container != nil {
			p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(r.processResult.ExitStatus))
		}
		return r.processResult, nil
	}
}

func (p *Process) SetTTY(tty runtime.TTYSpec) error {
	// TTY is not supported for K8s Pods in this implementation.
	return nil
}

// maxConsecutiveAPIErrors is the number of consecutive K8s API errors
// tolerated before failing the task.
const maxConsecutiveAPIErrors = 3

// pollUntilDone polls the Pod status until the main container terminates.
// Transient API errors are retried with exponential backoff up to
// maxConsecutiveAPIErrors consecutive failures.
func (p *Process) pollUntilDone(ctx context.Context) (runtime.ProcessResult, error) {
	consecutiveErrors := 0
	for {
		pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveAPIErrors {
				return runtime.ProcessResult{}, fmt.Errorf("%d consecutive API errors getting pod status: %w", consecutiveErrors, err)
			}
			backoff := time.Duration(consecutiveErrors) * time.Second
			select {
			case <-ctx.Done():
				return runtime.ProcessResult{}, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}
		consecutiveErrors = 0

		// Check for terminal failure states before checking exit code.
		if reason, message, failed := isPodFailedFast(pod); failed {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: %s: %s", reason, message)
		}
		if isPodEvicted(pod) {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: Evicted: %s", pod.Status.Message)
		}

		exitCode, done := podExitCode(pod)
		if done {
			return runtime.ProcessResult{ExitStatus: exitCode}, nil
		}

		select {
		case <-ctx.Done():
			return runtime.ProcessResult{}, ctx.Err()
		case <-time.After(time.Second):
			// Poll interval
		}
	}
}

// streamLogs streams the Pod's container logs to ProcessIO.Stdout. It retries
// until the container is ready, then follows the log stream until the
// container terminates. If no Stdout writer is configured, this is a no-op.
func (p *Process) streamLogs(ctx context.Context) {
	if p.processIO.Stdout == nil {
		return
	}

	// Retry log streaming until the container is ready or the context is done.
	// GetLogs can fail if the container hasn't started yet.
	for {
		req := p.clientset.CoreV1().Pods(p.config.Namespace).GetLogs(p.podName, &corev1.PodLogOptions{
			Follow:    true,
			Container: mainContainerName,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			io.Copy(p.processIO.Stdout, stream)
			stream.Close()
			return
		}

		// If the context is done, stop retrying.
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
			// Retry after a short delay.
		}
	}
}

// terminalWaitingReasons is the set of container waiting reasons that indicate
// a terminal failure from which the pod will never recover.
var terminalWaitingReasons = map[string]bool{
	"ImagePullBackOff":  true,
	"ErrImagePull":      true,
	"CrashLoopBackOff":  true,
	"InvalidImageName":  true,
	"CreateContainerConfigError": true,
}

// isPodFailedFast checks if any container in the pod is stuck in a terminal
// waiting state (e.g. ImagePullBackOff, CrashLoopBackOff). Returns the
// reason and message if a terminal state is found.
func isPodFailedFast(pod *corev1.Pod) (reason, message string, failed bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != mainContainerName {
			continue
		}
		if cs.State.Waiting != nil && terminalWaitingReasons[cs.State.Waiting.Reason] {
			return cs.State.Waiting.Reason, cs.State.Waiting.Message, true
		}
	}
	return "", "", false
}

// isPodEvicted checks whether the pod has been evicted by the kubelet.
func isPodEvicted(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted"
}

// writePodDiagnostics writes human-readable pod failure diagnostics to the
// given writer. This includes pod phase, reason, conditions, and container
// waiting reasons so they appear in the Concourse build log.
func writePodDiagnostics(pod *corev1.Pod, w io.Writer) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "\n--- Pod Failure Diagnostics ---\n")
	fmt.Fprintf(w, "Pod: %s/%s\n", pod.Namespace, pod.Name)
	fmt.Fprintf(w, "Phase: %s\n", pod.Status.Phase)
	if pod.Status.Reason != "" {
		fmt.Fprintf(w, "Reason: %s\n", pod.Status.Reason)
	}
	if pod.Status.Message != "" {
		fmt.Fprintf(w, "Message: %s\n", pod.Status.Message)
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Status == corev1.ConditionFalse || cond.Reason != "" {
			fmt.Fprintf(w, "Condition: %s=%s Reason=%s Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			fmt.Fprintf(w, "Container %q: Waiting: %s: %s\n",
				cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
		if cs.State.Terminated != nil {
			fmt.Fprintf(w, "Container %q: Terminated: %s (exit code %d)\n",
				cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
		}
	}
}

// isPodUnschedulable checks whether the pod has an Unschedulable condition.
func isPodUnschedulable(pod *corev1.Pod) (message string, unschedulable bool) {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == "Unschedulable" {
			return cond.Message, true
		}
	}
	return "", false
}

// podExitCode extracts the exit code from the Pod's container status.
// Returns the exit code and whether the Pod has terminated.
func podExitCode(pod *corev1.Pod) (int, bool) {
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == mainContainerName && cs.State.Terminated != nil {
				return int(cs.State.Terminated.ExitCode), true
			}
		}
		// Pod phase indicates completion but no container status found.
		// Default to 0 for Succeeded, 1 for Failed.
		if pod.Status.Phase == corev1.PodSucceeded {
			return 0, true
		}
		return 1, true
	}
	return 0, false
}

// execProcess implements runtime.Process using the exec API. Instead of
// baking the command into the Pod spec, it creates a pause Pod and exec-s
// the real command with full stdin/stdout/stderr separation.
type execProcess struct {
	id          string
	podName     string
	clientset   kubernetes.Interface
	config      Config
	container   *Container
	executor    PodExecutor
	processSpec runtime.ProcessSpec
	processIO   runtime.ProcessIO
}

func newExecProcess(
	id, podName string,
	clientset kubernetes.Interface,
	config Config,
	container *Container,
	executor PodExecutor,
	spec runtime.ProcessSpec,
	io runtime.ProcessIO,
) *execProcess {
	return &execProcess{
		id:          id,
		podName:     podName,
		clientset:   clientset,
		config:      config,
		container:   container,
		executor:    executor,
		processSpec: spec,
		processIO:   io,
	}
}

func (p *execProcess) ID() string {
	return p.id
}

// Wait waits for the pause Pod to reach Running state, then exec-s the
// actual command with ProcessIO piped through. After the exec completes,
// the Pod is cleaned up.
func (p *execProcess) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	// Wait for the Pod to be running before exec-ing.
	if err := p.waitForRunning(ctx); err != nil {
		return runtime.ProcessResult{}, fmt.Errorf("waiting for pod running: %w", err)
	}

	// Build the command: [path, arg1, arg2, ...]
	command := append([]string{p.processSpec.Path}, p.processSpec.Args...)

	err := p.executor.ExecInPod(
		ctx,
		p.config.Namespace,
		p.podName,
		mainContainerName,
		command,
		p.processIO.Stdin,
		p.processIO.Stdout,
		p.processIO.Stderr,
		p.processSpec.TTY != nil,
	)

	// NOTE: The pause Pod is intentionally NOT deleted here.
	// Pod cleanup is handled by the GC system (reaper), which enables
	// fly hijack to exec into the still-running pod for debugging.

	if err != nil {
		var exitErr *ExecExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode
			if p.container != nil {
				p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exitCode))
			}
			return runtime.ProcessResult{ExitStatus: exitCode}, nil
		}
		return runtime.ProcessResult{}, fmt.Errorf("exec in pod: %w", err)
	}

	if p.container != nil {
		p.container.SetProperty(exitStatusPropertyName, "0")
	}
	return runtime.ProcessResult{ExitStatus: 0}, nil
}

func (p *execProcess) SetTTY(_ runtime.TTYSpec) error {
	return nil
}

// waitForRunning polls the Pod until it reaches the Running phase.
// It enforces a startup timeout from Config.PodStartupTimeout and
// retries transient API errors up to maxConsecutiveAPIErrors.
func (p *execProcess) waitForRunning(ctx context.Context) error {
	timeout := p.config.PodStartupTimeout
	if timeout == 0 {
		timeout = DefaultPodStartupTimeout
	}
	deadline := time.After(timeout)

	consecutiveErrors := 0
	var lastPod *corev1.Pod
	for {
		pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveAPIErrors {
				return fmt.Errorf("%d consecutive API errors getting pod: %w", consecutiveErrors, err)
			}
			backoff := time.Duration(consecutiveErrors) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline:
				return fmt.Errorf("timed out waiting for pod to start (timeout: %s)", timeout)
			case <-time.After(backoff):
				continue
			}
		}
		consecutiveErrors = 0
		lastPod = pod

		// Check for terminal failure states BEFORE checking Running phase,
		// because CrashLoopBackOff can occur while the pod phase is Running.
		if reason, message, failed := isPodFailedFast(pod); failed {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: %s: %s", reason, message)
		}
		if isPodEvicted(pod) {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: Evicted: %s", pod.Status.Message)
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: Unschedulable: %s", message)
		}

		if pod.Status.Phase == corev1.PodRunning {
			return nil
		}

		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("pod terminated before exec could run (phase: %s)", pod.Status.Phase)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			writePodDiagnostics(lastPod, p.processIO.Stderr)
			return fmt.Errorf("timed out waiting for pod to start (timeout: %s, phase: %s)", timeout, lastPod.Status.Phase)
		case <-time.After(500 * time.Millisecond):
		}
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
