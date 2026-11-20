package jetbridge

import (
	"context"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// terminalWaitingReasons is the set of container waiting reasons that indicate
// a terminal failure from which the pod will never recover.
var terminalWaitingReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CrashLoopBackOff":           true,
	"InvalidImageName":           true,
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

// preemptionAnnotation marks a pod the node-preemption watcher has already
// identified as being reclaimed, so a subsequent deletion is classified as a
// preemption rather than an ordinary pod-deleted event.
const preemptionAnnotation = "concourse-ci.org/preemption-notice"

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
		// PodScheduled is printed even when True with no Reason: it tells
		// the reader the failure happened on a node the scheduler had
		// already chosen, rather than the pod never being placed.
		if cond.Type == corev1.PodScheduled || cond.Status == corev1.ConditionFalse || cond.Reason != "" {
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

	writeNodeStatus(node, w)
}

// writeNodeStatus formats reported input without fetching or modifying API state.
func writeNodeStatus(node *corev1.Node, w io.Writer) {
	// Surface node pressure conditions (MemoryPressure, DiskPressure, PIDPressure).
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				fmt.Fprintf(w, "Node %s: %s=True: %s\n", node.Name, cond.Type, cond.Message)
			}
		case corev1.NodeReady:
			if cond.Status != corev1.ConditionTrue {
				fmt.Fprintf(w, "Node %s: NotReady: %s: %s\n", node.Name, cond.Reason, cond.Message)
			}
		}
	}

	// Check for spot/preemptible node labels (GKE and generic K8s).
	if v, ok := node.Labels["cloud.google.com/gke-spot"]; ok && v == "true" {
		fmt.Fprintf(w, "Node %s: spot/preemptible instance (cloud.google.com/gke-spot=true)\n", node.Name)
	}
	if v, ok := node.Labels["cloud.google.com/gke-preemptible"]; ok && v == "true" {
		fmt.Fprintf(w, "Node %s: preemptible instance (cloud.google.com/gke-preemptible=true)\n", node.Name)
	}
	if _, ok := node.Labels["kubernetes.azure.com/scalesetpriority"]; ok {
		fmt.Fprintf(w, "Node %s: spot instance (kubernetes.azure.com/scalesetpriority=%s)\n", node.Name, node.Labels["kubernetes.azure.com/scalesetpriority"])
	}
	if v, ok := node.Labels["eks.amazonaws.com/capacityType"]; ok && v == "SPOT" {
		fmt.Fprintf(w, "Node %s: spot instance (eks.amazonaws.com/capacityType=SPOT)\n", node.Name)
	}

	// Check if node is being drained / cordoned (unschedulable).
	if node.Spec.Unschedulable {
		fmt.Fprintf(w, "Node %s: cordoned (unschedulable) — node may be draining\n", node.Name)
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

// podSchedulingTimeout returns the configured scheduling timeout, falling
// back to DefaultPodSchedulingTimeout when unconfigured.
func podSchedulingTimeout(cfg Config) time.Duration {
	if cfg.PodSchedulingTimeout > 0 {
		return cfg.PodSchedulingTimeout
	}
	return DefaultPodSchedulingTimeout
}

// podStartupTimeout returns the configured startup timeout, falling back to
// DefaultPodStartupTimeout when unconfigured — the same 0-means-default rule
// waitForRunning enforces. The resolve-capability floor is computed from these
// EFFECTIVE values, so a zero timeout in Config does not make the floor an
// underestimate of the wait a pod actually experiences.
func podStartupTimeout(cfg Config) time.Duration {
	if cfg.PodStartupTimeout > 0 {
		return cfg.PodStartupTimeout
	}
	return DefaultPodStartupTimeout
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
// A completed main may coexist with Running sidecars or a Pending sidecar
// whose image cannot be pulled. In either phase, main owns the step result.
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

	case corev1.PodRunning, corev1.PodPending:
		// A running or not-yet-started sidecar must not hide main completion.
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
		if cs.State.Waiting == nil || cs.State.Waiting.Reason != "ContainerCreating" || t.pullingImages[cs.Name] {
			continue
		}
		t.pullingImages[cs.Name] = true
		span.AddEvent("image.pulling",
			oteltrace.WithAttributes(
				attribute.String("container.name", cs.Name),
				attribute.String("container.image", cs.Image),
			),
		)
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
		if cs.State.Terminated == nil || t.completedInits[cs.Name] {
			continue
		}
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

	// Emit sidecar.started for non-main containers that reach Running.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == mainContainerName {
			continue
		}
		if cs.State.Running == nil || t.startedSidecars[cs.Name] {
			continue
		}
		t.startedSidecars[cs.Name] = true
		span.AddEvent("sidecar.started",
			oteltrace.WithAttributes(attribute.String("container.name", cs.Name)),
		)
	}
}

// failedInitContainer reports the first init container that terminated
// non-zero, which is the step's own setup failing rather than the pause pod
// being taken away from it.
func failedInitContainer(pod *corev1.Pod) (name string, exitCode int32, failed bool) {
	if pod == nil {
		return "", 0, false
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return cs.Name, cs.State.Terminated.ExitCode, true
		}
	}
	return "", 0, false
}
