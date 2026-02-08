package k8sruntime

import (
	"context"
	"errors"
	"fmt"
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
// completion.
type Process struct {
	id        string
	podName   string
	clientset kubernetes.Interface
	config    Config
	container *Container
}

func newProcess(id, podName string, clientset kubernetes.Interface, config Config, container *Container) *Process {
	return &Process{
		id:        id,
		podName:   podName,
		clientset: clientset,
		config:    config,
		container: container,
	}
}

func (p *Process) ID() string {
	return p.id
}

// Wait watches the Pod until the main container terminates and returns the
// exit code. If the context is cancelled, the Pod is deleted and the context
// error is returned.
func (p *Process) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	type result struct {
		processResult runtime.ProcessResult
		err           error
	}

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

// pollUntilDone polls the Pod status until the main container terminates.
func (p *Process) pollUntilDone(ctx context.Context) (runtime.ProcessResult, error) {
	for {
		pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if err != nil {
			return runtime.ProcessResult{}, fmt.Errorf("get pod status: %w", err)
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
	)

	// Clean up the pause Pod.
	_ = p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
		context.Background(), p.podName, metav1.DeleteOptions{},
	)

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
func (p *execProcess) waitForRunning(ctx context.Context) error {
	for {
		pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get pod: %w", err)
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
