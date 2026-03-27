package jetbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"golang.org/x/sync/errgroup"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

const (
	// maxConsecutiveAPIErrors is the number of consecutive K8s API errors
	// tolerated before failing the task. Used by PodWatcher for both initial
	// sync and watch fallback.
	maxConsecutiveAPIErrors = 3

	// logStreamRetryDelay is how long to wait before retrying log stream
	// attachment when the container isn't ready yet.
	logStreamRetryDelay = 500 * time.Millisecond
)

// pollUntilDone polls the Pod status until the main container terminates.
// Transient API errors are retried with exponential backoff up to
// maxConsecutiveAPIErrors consecutive failures.
func (p *Process) pollUntilDone(ctx context.Context) (runtime.ProcessResult, error) {
	watcher := NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
	defer watcher.Stop()

	var lastPhase corev1.PodPhase
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

		// Check for terminal failure states before checking exit code.
		// OOM check runs first — "OOMKilled" is more actionable than the
		// generic "CrashLoopBackOff" that often wraps it.
		if containerName, oom := isPodOOMKilled(pod); oom {
			metric.RecordK8sPodFailure(ctx, "OOMKilled")
			writePodDiagnostics(pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: OOMKilled: container %q exceeded memory limit", containerName)
		}
		if reason, message, failed := isPodFailedFast(pod); failed {
			if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				metric.Metrics.K8sImagePullFailures.Inc()
			}
			metric.RecordK8sPodFailure(ctx, reason)
			writePodDiagnostics(pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: %s: %s", reason, message)
		}
		if isPodEvicted(pod) {
			metric.RecordK8sPodFailure(ctx, "Evicted")
			writePodDiagnostics(pod, p.processIO.Stderr)
			writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: Evicted: %s", pod.Status.Message)
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return runtime.ProcessResult{}, fmt.Errorf("pod failed: Unschedulable: %s", message)
		}

		exitCode, done := podExitCode(pod)
		if done {
			return runtime.ProcessResult{ExitStatus: exitCode}, nil
		}
	}
}

// streamLogs streams the Pod's container logs. The main container logs go to
// ProcessIO.Stdout directly. Sidecar container logs are sent to their
// dedicated writer in ProcessIO.SidecarWriters (one per sidecar), which emits
// Log events with the sidecar's own plan ID as origin. If no dedicated writer
// exists, sidecar logs fall back to the legacy [name]-prefixed output on
// Stdout. If no Stdout writer is configured, this is a no-op.
func (p *Process) streamLogs(ctx context.Context) {
	if p.processIO.Stdout == nil {
		return
	}

	// Stream sidecar containers in background goroutines.
	if p.container != nil {
		for _, sc := range p.container.containerSpec.Sidecars {
			if w, ok := p.processIO.SidecarWriters[sc.Name]; ok {
				// Dedicated writer: stream directly to the per-sidecar event writer.
				go p.streamContainerLogsDirect(ctx, sc.Name, w)
			} else {
				// Fallback: prefix sidecar output into shared stdout.
				go p.streamContainerLogsPrefixed(ctx, sc.Name)
			}
		}
	}

	// Stream main container logs directly (no prefix).
	p.streamContainerLogsMain(ctx, mainContainerName)
}

// streamContainerLogsPrefixed streams logs from a named container, prefixing
// each line with [containerName]. Legacy fallback for sidecar containers when
// no dedicated writer is available.
func (p *Process) streamContainerLogsPrefixed(ctx context.Context, containerName string) {
	prefix := fmt.Sprintf("[%s] ", containerName)
	for {
		req := p.clientset.CoreV1().Pods(p.config.Namespace).GetLogs(p.podName, &corev1.PodLogOptions{
			Follow:    true,
			Container: containerName,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			p.copyWithPrefix(stream, prefix)
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

// streamContainerLogsDirect streams logs from a named container directly to
// the given writer without any prefix. Used for sidecar containers with
// dedicated per-sidecar event writers.
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

// copyWithPrefix reads lines from r and writes them to stdout with a prefix.
func (p *Process) copyWithPrefix(r io.Reader, prefix string) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// Split into lines and prefix each one.
			lines := strings.Split(string(buf[:n]), "\n")
			for i, line := range lines {
				if i == len(lines)-1 && line == "" {
					// Trailing empty string from split — don't prefix.
					continue
				}
				fmt.Fprintf(p.processIO.Stdout, "%s%s\n", prefix, line)
			}
		}
		if err != nil {
			return
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
// reason and message if a terminal state is found. If the main container has
// already terminated, sidecar failures are ignored — the exit code path
// handles the result instead.
func isPodFailedFast(pod *corev1.Pod) (reason, message string, failed bool) {
	// If the main container has already terminated, don't fail on sidecar
	// issues — the exit code path will handle the result.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == mainContainerName && cs.State.Terminated != nil {
			return "", "", false
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
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

// isPodOOMKilled checks whether any container in the pod was terminated due
// to an OOM kill. Returns the container name if found, empty string otherwise.
func isPodOOMKilled(pod *corev1.Pod) (containerName string, oomKilled bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return cs.Name, true
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			return cs.Name, true
		}
	}
	return "", false
}

// writePodDiagnostics writes human-readable pod failure diagnostics to the
// given writer. This includes pod phase, reason, conditions, container
// states (including OOM kills and restart history), and node information
// so they appear in the Concourse build log.
func writePodDiagnostics(pod *corev1.Pod, w io.Writer) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "\n--- Pod Failure Diagnostics ---\n")
	fmt.Fprintf(w, "Pod: %s/%s\n", pod.Namespace, pod.Name)
	if pod.Spec.NodeName != "" {
		fmt.Fprintf(w, "Node: %s\n", pod.Spec.NodeName)
	}
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
			if cs.State.Terminated.Message != "" {
				fmt.Fprintf(w, "  Message: %s\n", cs.State.Terminated.Message)
			}
		}
		if cs.RestartCount > 0 {
			fmt.Fprintf(w, "Container %q: RestartCount: %d\n", cs.Name, cs.RestartCount)
			if cs.LastTerminationState.Terminated != nil {
				last := cs.LastTerminationState.Terminated
				fmt.Fprintf(w, "  Last termination: %s (exit code %d)\n", last.Reason, last.ExitCode)
				if last.Message != "" {
					fmt.Fprintf(w, "  Last termination message: %s\n", last.Message)
				}
			}
		}
	}
}

// writeNodeDiagnostics fetches and writes node-level diagnostics (pressure
// conditions, preemption) to help diagnose why a pod was evicted or deleted.
func writeNodeDiagnostics(ctx context.Context, clientset kubernetes.Interface, pod *corev1.Pod, w io.Writer) {
	if w == nil || pod.Spec.NodeName == "" {
		return
	}
	// Use a short timeout — this is best-effort diagnostics and must not
	// block the build for long if the node is already gone.
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	node, err := clientset.CoreV1().Nodes().Get(fetchCtx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(w, "Node %s: unable to fetch details: %v\n", pod.Spec.NodeName, err)
		return
	}

	// Surface node pressure conditions (MemoryPressure, DiskPressure, PIDPressure).
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				fmt.Fprintf(w, "Node %s: %s=True: %s\n", pod.Spec.NodeName, cond.Type, cond.Message)
			}
		case corev1.NodeReady:
			if cond.Status != corev1.ConditionTrue {
				fmt.Fprintf(w, "Node %s: NotReady: %s: %s\n", pod.Spec.NodeName, cond.Reason, cond.Message)
			}
		}
	}

	// Check for spot/preemptible node labels (GKE and generic K8s).
	if v, ok := node.Labels["cloud.google.com/gke-spot"]; ok && v == "true" {
		fmt.Fprintf(w, "Node %s: spot/preemptible instance (cloud.google.com/gke-spot=true)\n", pod.Spec.NodeName)
	}
	if v, ok := node.Labels["cloud.google.com/gke-preemptible"]; ok && v == "true" {
		fmt.Fprintf(w, "Node %s: preemptible instance (cloud.google.com/gke-preemptible=true)\n", pod.Spec.NodeName)
	}
	if _, ok := node.Labels["kubernetes.azure.com/scalesetpriority"]; ok {
		fmt.Fprintf(w, "Node %s: spot instance (kubernetes.azure.com/scalesetpriority=%s)\n", pod.Spec.NodeName, node.Labels["kubernetes.azure.com/scalesetpriority"])
	}
	if v, ok := node.Labels["eks.amazonaws.com/capacityType"]; ok && v == "SPOT" {
		fmt.Fprintf(w, "Node %s: spot instance (eks.amazonaws.com/capacityType=SPOT)\n", pod.Spec.NodeName)
	}

	// Check if node is being drained / cordoned (unschedulable).
	if node.Spec.Unschedulable {
		fmt.Fprintf(w, "Node %s: cordoned (unschedulable) — node may be draining\n", pod.Spec.NodeName)
	}
}

// fetchPodFailureContext is a best-effort diagnostic helper for exec-mode
// operations. When an exec/upload/stream operation fails, this function
// fetches the pod's current status and writes diagnostics (pod + node) to
// stderr so the build log shows why the pod vanished.
func fetchPodFailureContext(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, w io.Writer) {
	if w == nil {
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(namespace).Get(fetchCtx, podName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(w, "\n--- Pod Failure Context ---\n")
		fmt.Fprintf(w, "Pod %s/%s: pod no longer exists (likely deleted by kubelet or GC): %v\n", namespace, podName, err)
		return
	}
	writePodDiagnostics(pod, w)
	writeNodeDiagnostics(ctx, clientset, pod, w)
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

// podExitCode extracts the exit code from the Pod's main container status.
// Returns the exit code and whether the main container has terminated.
// When sidecars are present, the pod phase may still be Running even after
// the main container exits, so we also check for main container termination
// in that phase.
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

	case corev1.PodRunning:
		// When sidecars are present, the pod stays Running after main exits.
		// Check if the main container has terminated.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == mainContainerName && cs.State.Terminated != nil {
				return int(cs.State.Terminated.ExitCode), true
			}
		}
	}
	return 0, false
}

// podEventTracker tracks which pod lifecycle events have been emitted to
// avoid duplicate span events across poll iterations.
type podEventTracker struct {
	completedInits  map[string]bool
	startedSidecars map[string]bool
	scheduled       bool
	initialized     bool
	pullingImages   map[string]bool
}

func newPodEventTracker() *podEventTracker {
	return &podEventTracker{
		completedInits:  make(map[string]bool),
		startedSidecars: make(map[string]bool),
		pullingImages:   make(map[string]bool),
	}
}

// emitPodLifecycleEvents emits span events for init container completion,
// sidecar startup, pod scheduling, and image pull states.
func (t *podEventTracker) emitPodLifecycleEvents(ctx context.Context, pod *corev1.Pod) {
	span := oteltrace.SpanFromContext(ctx)

	// Emit pod.scheduled when PodScheduled condition becomes True.
	if !t.scheduled {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue {
				t.scheduled = true
				span.AddEvent("pod.scheduled",
					oteltrace.WithAttributes(attribute.String("node.name", pod.Spec.NodeName)),
				)
				break
			}
		}
	}

	// Emit pod.initialized when Initialized condition becomes True.
	if !t.initialized {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue {
				t.initialized = true
				span.AddEvent("pod.initialized")
				break
			}
		}
	}

	// Emit image.pulling when a container enters ContainerCreating.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" && !t.pullingImages[cs.Name] {
			t.pullingImages[cs.Name] = true
			span.AddEvent("image.pulling",
				oteltrace.WithAttributes(
					attribute.String("container.name", cs.Name),
					attribute.String("container.image", cs.Image),
				),
			)
		}
	}

	// Emit image.pulled when a container transitions out of ContainerCreating.
	for _, cs := range pod.Status.ContainerStatuses {
		if t.pullingImages[cs.Name] && (cs.State.Running != nil || cs.State.Terminated != nil) {
			delete(t.pullingImages, cs.Name)
			span.AddEvent("image.pulled",
				oteltrace.WithAttributes(
					attribute.String("container.name", cs.Name),
					attribute.String("container.image", cs.Image),
				),
			)
		}
	}

	// Emit init container completion/failure events.
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && !t.completedInits[cs.Name] {
			t.completedInits[cs.Name] = true
			if cs.State.Terminated.ExitCode == 0 {
				span.AddEvent("init.container.completed",
					oteltrace.WithAttributes(
						attribute.String("container.name", cs.Name),
						attribute.String("container.image", cs.Image),
					),
				)
			} else {
				span.AddEvent("init.container.failed",
					oteltrace.WithAttributes(
						attribute.String("container.name", cs.Name),
						attribute.String("container.image", cs.Image),
						attribute.String("reason", cs.State.Terminated.Reason),
						attribute.Int64("exit.code", int64(cs.State.Terminated.ExitCode)),
					),
				)
			}
		}
	}

	// Emit sidecar.started for non-main containers that reach Running.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == mainContainerName {
			continue
		}
		if cs.State.Running != nil && !t.startedSidecars[cs.Name] {
			t.startedSidecars[cs.Name] = true
			span.AddEvent("sidecar.started",
				oteltrace.WithAttributes(attribute.String("container.name", cs.Name)),
			)
		}
	}
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

// Wait waits for the pause Pod to reach Running state, streams input
// artifacts into the pod, then exec-s the actual command with ProcessIO
// piped through.
func (p *execProcess) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	logger := lagerctx.FromContext(ctx).Session("exec-process-wait", lager.Data{
		"pod":        p.podName,
		"process-id": p.id,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.exec-process.wait", tracing.Attrs{
		"pod-name":   p.podName,
		"process-id": p.id,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// NOTE: The pause Pod is intentionally NOT deleted on context cancellation.
	// Pod cleanup is handled by the GC system (reaper), which enables
	// fly hijack to exec into the still-running pod for debugging.

	// Wait for the Pod to be running before exec-ing.
	waitCtx, waitSpan := tracing.StartSpan(ctx, "k8s.exec-process.wait-for-running", tracing.Attrs{
		"pod-name": p.podName,
	})
	if err := p.waitForRunning(waitCtx); err != nil {
		tracing.End(waitSpan, err)
		logger.Error("failed-to-wait-for-pod-running", err)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("waiting for pod running: %w", err))
	}
	tracing.End(waitSpan, nil)

	// Stream input artifacts into the pod before executing the command.
	streamCtx, streamSpan := tracing.StartSpan(ctx, "k8s.exec-process.stream-inputs", tracing.Attrs{
		"pod-name": p.podName,
	})
	if err := p.streamInputs(streamCtx); err != nil {
		tracing.End(streamSpan, err)
		logger.Error("failed-to-stream-inputs", err)
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("streaming inputs: %w", err))
	}
	tracing.End(streamSpan, nil)

	// Stream sidecar container logs in parallel with the exec command.
	// Sidecar logs are written to dedicated per-sidecar event writers so
	// fly watch can render them. The WaitGroup ensures all sidecar log
	// streams finish before we return (preventing log loss).
	var sidecarWg sync.WaitGroup
	if p.container != nil && len(p.processIO.SidecarWriters) > 0 {
		for _, sc := range p.container.containerSpec.Sidecars {
			if w, ok := p.processIO.SidecarWriters[sc.Name]; ok {
				sidecarWg.Add(1)
				go func(name string, writer io.Writer) {
					defer sidecarWg.Done()
					p.streamSidecarLogs(ctx, name, writer)
				}(sc.Name, w)
			}
		}
	}

	// Build the command: [path, arg1, arg2, ...]
	command := append([]string{p.processSpec.Path}, p.processSpec.Args...)

	execCtx, execSpan := tracing.StartSpan(ctx, "k8s.exec-process.exec", tracing.Attrs{
		"pod-name": p.podName,
	})
	err := p.executor.ExecInPod(
		execCtx,
		p.config.Namespace,
		p.podName,
		mainContainerName,
		command,
		p.processIO.Stdin,
		p.processIO.Stdout,
		p.processIO.Stderr,
		p.processSpec.TTY != nil,
		ExecAttrs{Purpose: "step-command"},
	)
	tracing.End(execSpan, err)

	// Wait for sidecar log streams to finish (bounded to 5s to avoid
	// blocking indefinitely if a sidecar hangs).
	sidecarDone := make(chan struct{})
	go func() {
		sidecarWg.Wait()
		close(sidecarDone)
	}()
	select {
	case <-sidecarDone:
	case <-time.After(5 * time.Second):
	}

	// NOTE: The pause Pod is intentionally NOT deleted on normal completion.
	// Pod cleanup is handled by the GC system (reaper), which enables
	// fly hijack to exec into the still-running pod for debugging.
	// However, on context cancellation (abort), the pod is deleted immediately
	// since the build is being abandoned.

	if err != nil {
		var exitErr *ExecExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode
			// Upload outputs even on non-zero exit (some steps produce
			// useful artifacts on failure).
			if uploadErr := p.uploadOutputsToArtifactStore(ctx); uploadErr != nil {
				fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
				return runtime.ProcessResult{}, fmt.Errorf("uploading artifacts: %w", uploadErr)
			}
			p.uploadCachesToArtifactStore(ctx)
			if p.container != nil {
				p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exitCode))
			}
			p.annotateExitStatus(ctx, exitCode)
			span.SetAttributes(attribute.String("exit-code", strconv.Itoa(exitCode)))
			return runtime.ProcessResult{ExitStatus: exitCode}, nil
		}
		logger.Error("failed-to-exec-in-pod", err)
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("exec in pod: %w", err))
	}

	// Upload step outputs to the artifact store PVC for cross-node access.
	if err := p.uploadOutputsToArtifactStore(ctx); err != nil {
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
		return runtime.ProcessResult{}, fmt.Errorf("uploading artifacts: %w", err)
	}
	p.uploadCachesToArtifactStore(ctx)

	if p.container != nil {
		p.container.SetProperty(exitStatusPropertyName, "0")
	}
	p.annotateExitStatus(ctx, 0)
	span.SetAttributes(attribute.String("exit-code", "0"))
	return runtime.ProcessResult{ExitStatus: 0}, nil
}

// streamInputs streams each input artifact into the corresponding volume
// on the running pause pod. This must happen after waitForRunning() and
// before the command is exec'd, so the command sees the input data.
//
// When ArtifactStoreClaim is configured, ALL inputs are handled by init
// containers that extract from the artifact PVC, so streaming is skipped.
func (p *execProcess) streamInputs(ctx context.Context) error {
	if p.container == nil {
		return nil
	}
	// When artifact store is configured, ALL inputs are handled by init containers.
	if p.container.hasArtifactStore() {
		return nil
	}
	for _, input := range p.container.containerSpec.Inputs {
		if input.Artifact == nil {
			continue
		}
		vol := p.container.volumeForPath(input.DestinationPath)
		if vol == nil {
			continue
		}

		out, err := input.Artifact.StreamOut(ctx, ".", nil)
		if err != nil {
			return fmt.Errorf("stream out artifact %q: %w", input.Artifact.Handle(), err)
		}

		err = vol.StreamIn(ctx, ".", nil, 0, out)
		out.Close()
		if err != nil {
			return fmt.Errorf("stream in to %s: %w", input.DestinationPath, err)
		}
	}
	return nil
}

// uploadOutputsToArtifactStore execs tar commands in the artifact-helper
// sidecar to upload step outputs from emptyDir to the artifact PVC.
// Only volumes corresponding to declared outputs are uploaded — inputs
// (already on the PVC from init containers) and the working directory
// are skipped. Cache volumes are handled separately by uploadCachesToArtifactStore.
// Uploads run concurrently via errgroup. Upload failures are fatal — they
// propagate as build failures so downstream steps don't silently run with
// missing inputs.
func (p *execProcess) uploadOutputsToArtifactStore(ctx context.Context) error {
	if p.container == nil || !p.container.hasArtifactStore() {
		return nil
	}

	// DaemonSet mode: outputs are already on hostPath — no upload needed.
	// Record artifact locations for scheduling affinity.
	if p.container.config.IsDaemonSetBackend() {
		nodeName := p.fetchPodNodeName(ctx)
		logger := lagerctx.FromContext(ctx).Session("record-output-locations", lager.Data{
			"handle":    p.container.handle,
			"pod":       p.podName,
			"node":      nodeName,
			"volumes":   len(p.container.volumes),
			"type":      string(p.container.containerSpec.Type),
		})
		logger.Info("recording-daemonset-artifacts")
		p.recordOutputLocations(nodeName)
		return nil
	}

	outputPaths := p.container.outputPaths()
	if len(outputPaths) == 0 {
		return nil
	}

	// Count volumes with mount paths for the span attribute.
	var volumeCount int64
	for _, vol := range p.container.volumes {
		if vol.MountPath() != "" {
			volumeCount++
		}
	}

	ctx, span := tracing.StartSpan(ctx, "k8s.artifact.upload-outputs", tracing.Attrs{
		"pod-name":     p.podName,
		"volume.count": fmt.Sprintf("%d", volumeCount),
	})
	span.SetAttributes(attribute.Int64("volume.count", volumeCount))
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	g, gctx := errgroup.WithContext(ctx)

	for _, vol := range p.container.volumes {
		if vol.MountPath() == "" {
			continue
		}
		if !outputPaths[vol.MountPath()] {
			continue
		}

		key := ArtifactKey(vol.Handle())
		mountPath := vol.MountPath()

		g.Go(func() error {
			if err := p.uploadArtifact(gctx, key, mountPath, "output"); err != nil {
				return fmt.Errorf("upload artifact %s: %w", key, err)
			}
			return nil
		})
	}
	spanErr = g.Wait()
	return spanErr
}

// uploadCachesToArtifactStore execs tar commands in the artifact-helper
// sidecar to save task cache directories to the artifact PVC as tar files.
// This only runs for CacheStoreArtifact mode — cacheEntries is only populated
// when caches are backed by the artifact PVC (see buildVolumeMounts in
// container.go). For CacheStoreHostPath, CacheStorePVC, and CacheStoreEmptyDir,
// cacheEntries is empty and this function is a no-op.
// Uploads run concurrently. Cache upload failures are non-fatal — they are
// logged but do not fail the build, since a missing cache only causes a cold
// start on the next build.
func (p *execProcess) uploadCachesToArtifactStore(ctx context.Context) {
	if p.container == nil || len(p.container.cacheEntries) == 0 {
		return
	}

	// DaemonSet mode: caches are direct hostPath mounts — no upload needed.
	if p.container.config.IsDaemonSetBackend() {
		return
	}

	cacheCount := int64(len(p.container.cacheEntries))
	ctx, span := tracing.StartSpan(ctx, "k8s.artifact.upload-caches", tracing.Attrs{
		"pod-name":    p.podName,
		"cache.count": fmt.Sprintf("%d", cacheCount),
	})
	span.SetAttributes(attribute.Int64("cache.count", cacheCount))
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	logger := lagerctx.FromContext(ctx).Session("upload-caches")

	var wg sync.WaitGroup
	for _, entry := range p.container.cacheEntries {
		entry := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.uploadArtifact(ctx, entry.artifactKey, entry.mountPath, "cache"); err != nil {
				spanErr = err
				logger.Error("failed-to-upload-cache", err, lager.Data{
					"cache-key":  entry.artifactKey,
					"mount-path": entry.mountPath,
				})
			}
		}()
	}
	wg.Wait()
}

// ArtifactUploadStats holds telemetry data parsed from the two-phase upload
// command's stdout output.
type ArtifactUploadStats struct {
	FileCount     int64
	SizeBytes     int64
	TarMillis     int64
	TransferMillis int64
}

// ParseArtifactUploadStats parses the structured output from the upload shell
// script. Expected format: "FILES=<n> TAR_MS=<n> SIZE=<n> TRANSFER_MS=<n>"
func ParseArtifactUploadStats(output string) ArtifactUploadStats {
	var stats ArtifactUploadStats
	for _, field := range strings.Fields(output) {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		val, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		switch parts[0] {
		case "FILES":
			stats.FileCount = val
		case "TAR_MS":
			stats.TarMillis = val
		case "SIZE":
			stats.SizeBytes = val
		case "TRANSFER_MS":
			stats.TransferMillis = val
		}
	}
	return stats
}

// uploadArtifact performs a two-phase artifact upload in the artifact-helper
// sidecar: (1) tar to a temp file (measuring tar duration), (2) move to the
// PVC (measuring transfer duration). File count, tar size, and phase timings
// are captured as span attributes for observability.
func (p *execProcess) uploadArtifact(ctx context.Context, key, mountPath, artifactType string) error {
	ctx, span := tracing.StartSpan(ctx, "k8s.artifact.upload", tracing.Attrs{
		"artifact.key":        key,
		"artifact.type":       artifactType,
		"volume.mount_path":   mountPath,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	dest := fmt.Sprintf("%s/%s", ArtifactMountPath, key)
	tmpFile := fmt.Sprintf("/tmp/%s", strings.ReplaceAll(key, "/", "_"))

	// Two-phase upload:
	//   Phase 1: count files, tar to /tmp (local I/O)
	//   Phase 2: mv from /tmp to PVC (storage I/O)
	// Timings are reported via stdout for span attribute capture.
	// Uses /proc/uptime for millisecond-precision timing because Alpine's
	// BusyBox date does not support %%N (nanoseconds). /proc/uptime gives
	// centisecond precision (~10ms granularity), which is sufficient.
	shellScript := fmt.Sprintf(`
now_ms() { read up _ < /proc/uptime; echo "${up%%%%.*}${up#*.}0"; }
fc=$(find %s -type f 2>/dev/null | wc -l)
mkdir -p $(dirname %s)
t0=$(now_ms)
tar cf %s -C %s .
t1=$(now_ms)
sz=$(stat -c %%s %s 2>/dev/null || stat -f %%z %s 2>/dev/null || echo 0)
mkdir -p $(dirname %s)
mv %s %s
t2=$(now_ms)
echo "FILES=$fc TAR_MS=$((t1-t0)) SIZE=$sz TRANSFER_MS=$((t2-t1))"
`,
		mountPath,
		tmpFile,
		tmpFile, mountPath,
		tmpFile, tmpFile,
		dest,
		tmpFile, dest,
	)

	var stdout bytes.Buffer
	cmd := []string{"sh", "-c", shellScript}

	purpose := "artifact-upload"
	if artifactType == "cache" {
		purpose = "cache-upload"
	}
	spanErr = p.executor.ExecInPod(
		ctx, p.config.Namespace, p.podName,
		artifactHelperContainerName,
		cmd, nil, &stdout, nil, false,
		ExecAttrs{Purpose: purpose, ArtifactKey: key, VolumeMountPath: mountPath},
	)
	if spanErr != nil {
		return spanErr
	}

	stats := ParseArtifactUploadStats(stdout.String())

	span.SetAttributes(
		attribute.Int64("artifact.file_count", stats.FileCount),
		attribute.Int64("artifact.size_bytes", stats.SizeBytes),
		attribute.Int64("artifact.tar_duration_ms", stats.TarMillis),
		attribute.Int64("artifact.transfer_duration_ms", stats.TransferMillis),
	)

	if stats.TarMillis > 0 {
		span.AddEvent("artifact.tar.completed", oteltrace.WithAttributes(
			attribute.Int64("duration_ms", stats.TarMillis),
			attribute.Int64("size_bytes", stats.SizeBytes),
		))
	}
	if stats.TransferMillis > 0 {
		span.AddEvent("artifact.transfer.completed", oteltrace.WithAttributes(
			attribute.Int64("duration_ms", stats.TransferMillis),
		))
	}

	// Record OTel metrics for dashboards and alerting.
	tarDuration := time.Duration(stats.TarMillis) * time.Millisecond
	transferDuration := time.Duration(stats.TransferMillis) * time.Millisecond
	totalDuration := tarDuration + transferDuration
	metric.RecordArtifactUpload(ctx, artifactType, totalDuration, stats.SizeBytes, stats.FileCount, tarDuration, transferDuration)

	return nil
}

// annotateExitStatus persists the exit status as a pod annotation so that
// Attach() can recover the result after a web restart (when in-memory
// container properties are lost).
func (p *execProcess) annotateExitStatus(ctx context.Context, exitCode int) {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%d"}}}`, exitStatusAnnotationKey, exitCode)
	_, err := p.clientset.CoreV1().Pods(p.config.Namespace).Patch(
		ctx, p.podName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		logger := lagerctx.FromContext(ctx).Session("annotate-exit-status")
		logger.Error("failed-to-annotate-exit-status", err, lager.Data{
			"pod":       p.podName,
			"exit-code": exitCode,
		})
	}
}

func (p *execProcess) SetTTY(_ runtime.TTYSpec) error {
	return nil
}

// recordOutputLocations records each output volume's artifact key → node name
// in the ArtifactLocator. This enables scheduling affinity and local/remote
// fetch decisions for downstream steps in DaemonSet mode.
//
// When nodeName is empty (pod not found or API error), recordings still
// happen with an empty node — the consuming step's init container will fall
// through to the local cp -a branch (SOURCE_NODE unset), which works when
// the scheduler places the consumer on the same node via affinity.
func (p *execProcess) recordOutputLocations(nodeName string) {
	if p.container == nil || p.container.artifactLocator == nil {
		return
	}

	outputPaths := p.container.outputPaths()
	hostPath := p.container.config.ArtifactDaemonHostPath

	// Build reverse map: mount path → output name (used as hostPath subdir).
	mountToOutputName := make(map[string]string)
	for name, path := range p.container.containerSpec.Outputs {
		mountToOutputName[path] = name
	}
	// Dir volume gets subdir "dir".
	if p.container.containerSpec.Dir != "" {
		mountToOutputName[p.container.containerSpec.Dir] = "dir"
	}

	recorded := 0
	for _, vol := range p.container.volumes {
		if vol.MountPath() == "" || !outputPaths[vol.MountPath()] {
			continue
		}
		key := ArtifactKey(vol.Handle())
		subdir := mountToOutputName[vol.MountPath()]
		if subdir == "" {
			subdir = "unknown"
		}
		hostDir := filepath.Join(hostPath, "steps", p.container.handle, subdir)
		p.container.artifactLocator.Record(key, nodeName, hostDir)
		recorded++
	}
	if recorded == 0 && len(p.container.volumes) > 0 {
		// Log when we have volumes but none matched output paths — helps
		// diagnose locator-miss issues in DaemonSet mode.
		fmt.Fprintf(os.Stderr, "WARNING: recordOutputLocations: %d volumes but 0 matched outputPaths %v (handle=%s type=%s)\n",
			len(p.container.volumes), outputPaths, p.container.handle, p.container.containerSpec.Type)
	}
}

// fetchPodNodeName retrieves the node name where this pod is running.
func (p *execProcess) fetchPodNodeName(ctx context.Context) string {
	if p.clientset == nil {
		return ""
	}
	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
	if err != nil {
		logger := lagerctx.FromContext(ctx).Session("fetch-pod-node-name")
		logger.Error("failed-to-get-pod", err, lager.Data{"pod": p.podName, "namespace": p.config.Namespace})
		return ""
	}
	return pod.Spec.NodeName
}

// streamSidecarLogs streams logs from a sidecar container to the given writer
// using the K8s log API. Retries until the container is ready or the context
// is cancelled.
func (p *execProcess) streamSidecarLogs(ctx context.Context, containerName string, w io.Writer) {
	for {
		req := p.clientset.CoreV1().Pods(p.config.Namespace).GetLogs(p.podName, &corev1.PodLogOptions{
			Follow:    true,
			Container: containerName,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			io.Copy(w, stream)
			stream.Close()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitForRunning uses the Watch API to wait for the Pod to reach the Running
// phase. It enforces a startup timeout from Config.PodStartupTimeout.
func (p *execProcess) waitForRunning(ctx context.Context) error {
	timeout := p.config.PodStartupTimeout
	if timeout == 0 {
		timeout = DefaultPodStartupTimeout
	}
	startTime := time.Now()

	// Create a timeout context for the startup deadline.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher := NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
	defer watcher.Stop()

	var lastPod *corev1.Pod
	var lastPhase corev1.PodPhase
	countsSet := false
	tracker := newPodEventTracker()
	for {
		pod, err := watcher.Next(timeoutCtx)
		if err != nil {
			if errors.Is(err, ErrPodDeleted) && pod != nil {
				writePodDiagnostics(pod, p.processIO.Stderr)
				writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
				return fmt.Errorf("pod deleted externally before reaching Running: %s", pod.Status.Phase)
			}
			// Check if this was a timeout vs other error.
			if timeoutCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				if lastPod != nil {
					writePodDiagnostics(lastPod, p.processIO.Stderr)
					return fmt.Errorf("timed out waiting for pod to start (timeout: %s, phase: %s)", timeout, lastPod.Status.Phase)
				}
				return fmt.Errorf("timed out waiting for pod to start (timeout: %s)", timeout)
			}
			return err
		}
		lastPod = pod

		// Set container count span attributes from pod spec on first event.
		if !countsSet {
			span := oteltrace.SpanFromContext(ctx)
			span.SetAttributes(
				attribute.Int64("init.container.count", int64(len(pod.Spec.InitContainers))),
				attribute.Int64("container.count", int64(len(pod.Spec.Containers))),
			)
			countsSet = true
		}

		if pod.Status.Phase != lastPhase {
			lastPhase = pod.Status.Phase
			oteltrace.SpanFromContext(ctx).AddEvent(
				"pod.phase."+strings.ToLower(string(pod.Status.Phase)),
				oteltrace.WithAttributes(attribute.String("pod.phase", string(pod.Status.Phase))),
			)
		}

		tracker.emitPodLifecycleEvents(ctx, pod)

		// Check for terminal failure states BEFORE checking Running phase,
		// because CrashLoopBackOff can occur while the pod phase is Running.
		// OOM check first — more actionable than generic CrashLoopBackOff.
		if containerName, oom := isPodOOMKilled(pod); oom {
			metric.RecordK8sPodFailure(ctx, "OOMKilled")
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: OOMKilled: container %q exceeded memory limit", containerName)
		}
		if reason, message, failed := isPodFailedFast(pod); failed {
			if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				metric.Metrics.K8sImagePullFailures.Inc()
			}
			metric.RecordK8sPodFailure(ctx, reason)
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: %s: %s", reason, message)
		}
		if isPodEvicted(pod) {
			metric.RecordK8sPodFailure(ctx, "Evicted")
			writePodDiagnostics(pod, p.processIO.Stderr)
			writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: Evicted: %s", pod.Status.Message)
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return fmt.Errorf("pod failed: Unschedulable: %s", message)
		}

		if pod.Status.Phase == corev1.PodRunning {
			metric.Metrics.K8sPodStartupDuration.Set(time.Since(startTime).Milliseconds())
			return nil
		}

		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("pod terminated before exec could run (phase: %s)", pod.Status.Phase)
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
