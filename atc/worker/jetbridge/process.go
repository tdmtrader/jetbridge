package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
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
				// etc.). Write diagnostics and return a clear error.
				writePodDiagnostics(pod, p.processIO.Stderr)
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
				span.AddEvent("pod.scheduled")
				break
			}
		}
	}

	// Emit image.pulling when a container enters ContainerCreating.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" && !t.pullingImages[cs.Name] {
			t.pullingImages[cs.Name] = true
			span.AddEvent("image.pulling",
				oteltrace.WithAttributes(attribute.String("container.name", cs.Name)),
			)
		}
	}

	// Emit init container completion/failure events.
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && !t.completedInits[cs.Name] {
			t.completedInits[cs.Name] = true
			if cs.State.Terminated.ExitCode == 0 {
				span.AddEvent("init.container.completed",
					oteltrace.WithAttributes(attribute.String("container.name", cs.Name)),
				)
			} else {
				span.AddEvent("init.container.failed",
					oteltrace.WithAttributes(
						attribute.String("container.name", cs.Name),
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
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("streaming inputs: %w", err))
	}
	tracing.End(streamSpan, nil)

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
	)
	tracing.End(execSpan, err)

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
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("exec in pod: %w", err))
	}

	// Upload step outputs to the artifact store PVC for cross-node access.
	if err := p.uploadOutputsToArtifactStore(ctx); err != nil {
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
	if p.container.config.ArtifactStoreClaim != "" {
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
// Upload failures are fatal — they propagate as build failures so that
// downstream steps don't silently run with missing inputs.
func (p *execProcess) uploadOutputsToArtifactStore(ctx context.Context) error {
	if p.container == nil || p.container.config.ArtifactStoreClaim == "" {
		return nil
	}

	for _, vol := range p.container.volumes {
		if vol.MountPath() == "" {
			continue
		}
		key := ArtifactKey(vol.Handle())
		cmd := []string{"sh", "-c",
			fmt.Sprintf("mkdir -p $(dirname %s/%s) && tar cf %s/%s -C %s .",
				ArtifactMountPath, key, ArtifactMountPath, key, vol.MountPath()),
		}

		err := p.executor.ExecInPod(
			ctx, p.config.Namespace, p.podName,
			artifactHelperContainerName,
			cmd, nil, nil, nil, false,
		)
		if err != nil {
			return fmt.Errorf("upload artifact %s: %w", key, err)
		}
	}
	return nil
}

// uploadCachesToArtifactStore execs tar commands in the artifact-helper
// sidecar to save task cache directories to the artifact PVC as tar files.
// This enables cross-node cache sharing when the PVC is backed by GCS Fuse.
// Cache upload failures are non-fatal — they are logged but do not fail the
// build, since a missing cache only causes a cold start on the next build.
func (p *execProcess) uploadCachesToArtifactStore(ctx context.Context) {
	if p.container == nil || len(p.container.cacheEntries) == 0 {
		return
	}

	logger := lagerctx.FromContext(ctx).Session("upload-caches")

	for _, entry := range p.container.cacheEntries {
		cmd := []string{"sh", "-c",
			fmt.Sprintf("mkdir -p $(dirname %s/%s) && tar cf %s/%s -C %s .",
				ArtifactMountPath, entry.artifactKey,
				ArtifactMountPath, entry.artifactKey, entry.mountPath),
		}

		err := p.executor.ExecInPod(
			ctx, p.config.Namespace, p.podName,
			artifactHelperContainerName,
			cmd, nil, nil, nil, false,
		)
		if err != nil {
			logger.Error("failed-to-upload-cache", err, lager.Data{
				"cache-key":  entry.artifactKey,
				"mount-path": entry.mountPath,
			})
		}
	}
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
	tracker := newPodEventTracker()
	for {
		pod, err := watcher.Next(timeoutCtx)
		if err != nil {
			if errors.Is(err, ErrPodDeleted) && pod != nil {
				writePodDiagnostics(pod, p.processIO.Stderr)
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
