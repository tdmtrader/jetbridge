package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

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
	logger := lagerctx.FromContext(ctx).Session("process-wait", lager.Data{
		"pod":        p.podName,
		"process-id": p.id,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.process.wait", tracing.Attrs{
		"pod-name":   p.podName,
		"process-id": p.id,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	type result struct {
		processResult runtime.ProcessResult
		err           error
	}

	sidecarWriters, err := p.sidecarLogWriters()
	if err != nil {
		spanErr = err
		return runtime.ProcessResult{}, err
	}

	// Stream Pod logs to ProcessIO.Stdout in the background. The log
	// stream follows the container and closes automatically when it
	// terminates, so it won't block beyond the Pod's lifetime.
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		p.streamLogs(ctx, sidecarWriters)
	}()

	waitCh := make(chan result, 1)

	go func() {
		r, err := p.pollUntilDone(ctx)
		waitCh <- result{processResult: r, err: err}
	}()

	select {
	case <-ctx.Done():
		// Attempt to clean up the Pod on cancellation with a bounded timeout
		// so we don't block indefinitely if the API server is unreachable.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
			cleanupCtx, p.podName, metav1.DeleteOptions{},
		); err != nil {
			logger.Error("failed-to-cleanup-pod-on-cancel", err)
		}
		spanErr = ctx.Err()
		return runtime.ProcessResult{}, ctx.Err()

	case r := <-waitCh:
		// Wait for log streaming to finish so all output is captured
		// before returning.
		<-logDone

		if r.err != nil {
			logger.Error("failed-to-wait-for-pod", r.err)
			spanErr = r.err
			return runtime.ProcessResult{}, wrapIfTransient(r.err)
		}
		// Store exit status in container properties for reattachment.
		if p.container != nil {
			p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(r.processResult.ExitStatus))
		}

		// Delete the pod after the process exits to release resources.
		// This handles both sidecar pods (which stay Running after main exits)
		// and single-container pods to ensure prompt cleanup.
		{
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if delErr := p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
				cleanupCtx, p.podName, metav1.DeleteOptions{},
			); delErr != nil {
				logger.Error("failed-to-cleanup-pod", delErr)
			}
		}

		span.SetAttributes(attribute.String("exit-code", strconv.Itoa(r.processResult.ExitStatus)))
		return r.processResult, nil
	}
}

func (p *Process) SetTTY(tty runtime.TTYSpec) error {
	// TTY is not supported for K8s Pods in this implementation.
	return nil
}

// pollUntilDone polls the Pod status until the main container terminates.
// Transient API errors are retried with exponential backoff up to
// maxConsecutiveAPIErrors consecutive failures.
func (p *Process) pollUntilDone(ctx context.Context) (runtime.ProcessResult, error) {
	watcher := NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
	defer watcher.Stop()

	var lastPhase corev1.PodPhase
	var unschedulableFirstSeen time.Time
	var unschedulableNotified bool
	tracker := newPodEventTracker()
	for {
		pod, err := watcher.Next(ctx)
		if err != nil {
			if errors.Is(err, ErrPodDeleted) && pod != nil {
				// Pod was deleted externally (eviction, node failure,
				// spot preemption, etc.). Write pod and node diagnostics
				// to surface the root cause.
				writePodDiagnostics(pod, p.processIO.Stderr)
				writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
				return runtime.ProcessResult{}, fmt.Errorf("pod deleted externally: %s", pod.Status.Phase)
			}
			return runtime.ProcessResult{}, err
		}

		if pod.Status.Phase != lastPhase {
			lastPhase = pod.Status.Phase
			oteltrace.SpanFromContext(ctx).AddEvent(
				"pod.phase."+strings.ToLower(string(pod.Status.Phase)),
				oteltrace.WithAttributes(attribute.String("pod.phase", string(pod.Status.Phase))),
			)
		}

		tracker.emitPodLifecycleEvents(ctx, pod)

		// Terminal failure states are checked before the exit code, OOM
		// first (pod_status.go): "OOMKilled" is more actionable than the
		// generic "CrashLoopBackOff" that often wraps it.
		state := podStateFor(pod)
		if err := reportPodFailure(ctx, p.clientset, pod, p.processIO.Stderr, state); err != nil {
			return runtime.ProcessResult{}, err
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			if unschedulableFirstSeen.IsZero() {
				unschedulableFirstSeen = time.Now()
			}
			if !unschedulableNotified {
				fmt.Fprintf(p.processIO.Stderr, "pod is Unschedulable: %s — waiting up to %s for cluster resources\n", message, podSchedulingTimeout(p.config))
				unschedulableNotified = true
			}
			if time.Since(unschedulableFirstSeen) > podSchedulingTimeout(p.config) {
				writePodDiagnostics(pod, p.processIO.Stderr)
				return runtime.ProcessResult{}, fmt.Errorf("pod scheduling timeout: Unschedulable for %s: %s", podSchedulingTimeout(p.config), message)
			}
		} else {
			unschedulableFirstSeen = time.Time{}
			unschedulableNotified = false
		}

		if state.complete {
			return state.result, nil
		}
	}
}

// sidecarLogWriters pairs each declared sidecar with the writer the engine
// handed this step for it. See sidecarLogWriters in sidecar_logs.go for the
// rule; a step with no Stdout streams nothing and needs none.
func (p *Process) sidecarLogWriters() (map[string]io.Writer, error) {
	if p.processIO.Stdout == nil || p.container == nil {
		return nil, nil
	}
	return sidecarLogWriters(p.container.containerSpec.Sidecars, p.processIO)
}

// streamLogs streams the Pod's container logs. The main container logs go to
// ProcessIO.Stdout directly. Each sidecar's logs go to its dedicated writer
// in ProcessIO.SidecarWriters, which emits Log events with the sidecar's own
// plan ID as origin; sidecarWriters is that map, already checked complete. If
// no Stdout writer is configured, this is a no-op.
func (p *Process) streamLogs(ctx context.Context, sidecarWriters map[string]io.Writer) {
	if p.processIO.Stdout == nil {
		return
	}

	// Stream sidecar containers in background goroutines.
	//
	// These are WAITED ON before returning. Wait() closes logDone when this
	// function returns and then deletes the pod, so a sidecar goroutine still
	// copying at that moment has its stream cut and its output never reaches
	// the build log. The caller's comment at `<-logDone` — "wait for log
	// streaming to finish so all output is captured" — was only true of the
	// main container.
	var sidecars sync.WaitGroup
	for name, w := range sidecarWriters {
		sidecars.Add(1)
		go func(name string, w io.Writer) {
			defer sidecars.Done()
			defer closeSidecarWriter(w)
			p.streamContainerLogsDirect(ctx, name, w)
		}(name, w)
	}

	// Stream main container logs directly (no prefix).
	p.streamContainerLogsMain(ctx, mainContainerName)

	// Give the sidecars a BOUNDED moment to flush, then proceed.
	//
	// An unbounded join here hangs the step. A sidecar outlives the main
	// container by design, and its Follow:true stream does not end until the
	// sidecar does, so waiting for it means waiting for the sidecar — which
	// may be forever. Losing a late sidecar line is bad; never returning from
	// Wait is worse.
	waitWithGrace(&sidecars, sidecarLogGrace)
}

// streamContainerLogsDirect streams logs from a named container directly to
// the given writer. Used for the main container and for sidecar containers
// with their dedicated per-sidecar event writers.
func (p *Process) streamContainerLogsDirect(ctx context.Context, containerName string, w io.Writer) {
	for {
		req := p.clientset.CoreV1().Pods(p.config.Namespace).GetLogs(p.podName, &corev1.PodLogOptions{
			Follow:    true,
			Container: containerName,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			if _, copyErr := io.Copy(w, stream); copyErr != nil {
				if p.processIO.Stderr != nil && ctx.Err() == nil {
					fmt.Fprintf(p.processIO.Stderr, "\nwarning: log stream interrupted for %s: %v\n", containerName, copyErr)
				}
			}
			stream.Close()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(logStreamRetryDelay):
		}
	}
}

// streamContainerLogsMain streams logs from the main container directly to
// ProcessIO.Stdout without any prefix.
func (p *Process) streamContainerLogsMain(ctx context.Context, containerName string) {
	p.streamContainerLogsDirect(ctx, containerName, p.processIO.Stdout)
}
