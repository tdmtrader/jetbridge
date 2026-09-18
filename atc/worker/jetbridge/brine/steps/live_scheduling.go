package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Only the owned namespace changes. Production builds a pod whose CPU
// request exceeds every observed node, even when empty. This fixture requires
// stable node membership/capacity during the run; no current node can fit it.
// https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#inter-pod-affinity-on-lower-priority-pods
func diagnoseLiveScheduling(in LiveTaskPlan, rec *brine.Recorder, startup, scheduling time.Duration) (StepOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec, 1)
	if err != nil {
		return StepOutcome{}, err
	}
	nodes, err := w.Clientset.CoreV1().Nodes().List(w.Ctx, metav1.ListOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	nodeCapacity := map[string]int64{}
	var maximum int64
	ready := false
	for _, node := range nodes.Items {
		cpu := node.Status.Allocatable.Cpu().MilliValue()
		if node.UID == "" || cpu <= 0 {
			return StepOutcome{}, fmt.Errorf("node %q has no identity or allocatable CPU", node.Name)
		}
		if cpu > maximum {
			maximum = cpu
		}
		nodeCapacity[node.Name+"/"+string(node.UID)] = cpu
		for _, condition := range node.Status.Conditions {
			ready = ready || !node.Spec.Unschedulable && condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
		}
	}
	if len(nodeCapacity) == 0 || !ready || maximum > 127000 {
		return StepOutcome{}, fmt.Errorf("scheduler fixture requires ready nodes and at most 127 allocatable CPUs per node")
	}
	requested := resource.NewMilliQuantity(maximum+1000, resource.DecimalSI)
	quotas := w.Clientset.CoreV1().ResourceQuotas(w.Namespace)
	quota, err := quotas.Get(w.Ctx, "bounded-test", metav1.GetOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	// This impossible request reserves no node CPU. Keep one pod and all existing
	// memory/storage bounds; raise only this owned namespace's CPU admission cap.
	quota.Spec.Hard[corev1.ResourceRequestsCPU] = *requested
	quota.Spec.Hard[corev1.ResourceLimitsCPU] = *requested
	if _, err = quotas.Update(w.Ctx, quota, metav1.UpdateOptions{}); err != nil {
		return StepOutcome{}, err
	}
	for {
		published, err := quotas.Get(w.Ctx, quota.Name, metav1.GetOptions{})
		if err != nil {
			return StepOutcome{}, err
		}
		requestCap, requestsOK := published.Status.Hard[corev1.ResourceRequestsCPU]
		limitCap, limitsOK := published.Status.Hard[corev1.ResourceLimitsCPU]
		if published.UID != quota.UID {
			return StepOutcome{}, fmt.Errorf("owned quota identity changed")
		}
		if requestsOK && limitsOK && requestCap.Cmp(*requested) == 0 && limitCap.Cmp(*requested) == 0 {
			break
		}
		select {
		case <-w.Ctx.Done():
			return StepOutcome{}, fmt.Errorf("quota controller did not publish CPU cap: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	const handle = "unschedulable-handle"
	image := jetbridge.DefaultResourceTypeImages["git"]
	if image == "" {
		return StepOutcome{}, fmt.Errorf("default git resource image is missing")
	}
	cpu, memory := uint64(requested.MilliValue()), uint64(64*1024*1024)
	w.Config.PodStartupTimeout, w.Config.PodSchedulingTimeout = startup, scheduling
	w = w.rebuild()
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeGet},
		runtime.ContainerSpec{TeamID: w.TeamID, ImageSpec: runtime.ImageSpec{ResourceType: "git"}, Type: db.ContainerTypeGet,
			Limits: runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}, nil)
	if err != nil {
		return StepOutcome{}, err
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{Path: "/opt/resource/in", Args: []string{"/tmp/build/get"}},
		runtime.ProcessIO{Stdin: strings.NewReader("{}"), Stdout: stdout, Stderr: stderr})
	if err != nil {
		return StepOutcome{}, err
	}
	if fmt.Sprintf("%T", process) != "*jetbridge.execProcess" {
		return StepOutcome{}, fmt.Errorf("expected resource execProcess, got %T", process)
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	if original.UID == "" || original.ResourceVersion == "" || original.Spec.Priority == nil || *original.Spec.Priority != 0 || original.Spec.PriorityClassName != "" {
		return StepOutcome{}, fmt.Errorf("scheduler pod lacks real identity or default priority")
	}
	if len(original.Spec.Containers) != 1 {
		return StepOutcome{}, fmt.Errorf("expected one runtime resource container")
	}
	main := original.Spec.Containers[0]
	if main.Name != "main" || main.Image != image || main.Resources.Requests.Cpu().Cmp(*requested) != 0 || main.Resources.Limits.Cpu().Cmp(*requested) != 0 ||
		main.Resources.Requests.Memory().Value() != int64(memory) || main.Resources.Limits.Memory().Value() != int64(memory) {
		return StepOutcome{}, fmt.Errorf("runtime did not build the bounded git resource pod: %+v", main)
	}
	fmt.Printf("runtime-built scheduling pod: %s/%s UID %s image %s CPU request/limit %s memory request/limit %d; created by resource Run\n", w.Namespace, handle, original.UID, main.Image, requested.String(), memory)
	var refusal string
	var observed *corev1.Pod
	for {
		observed, err = pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return StepOutcome{}, err
		}
		if err = unscheduledIdentity(observed, original); err != nil {
			return StepOutcome{}, err
		}
		for _, condition := range observed.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" {
				refusal = condition.Message
			}
		}
		if strings.Contains(refusal, "Insufficient cpu") &&
			(strings.Contains(refusal, "Preemption is not helpful for scheduling") || strings.Contains(refusal, "No preemption victims found for incoming pod")) {
			break
		}
		select {
		case <-w.Ctx.Done():
			return StepOutcome{}, fmt.Errorf("scheduler did not report actual CPU refusal: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Independently witness the scheduler, not merely a persisted status value.
	for {
		events, err := w.Clientset.CoreV1().Events(w.Namespace).List(w.Ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(original.UID)})
		if err != nil {
			return StepOutcome{}, err
		}
		witnessed := false
		for _, event := range events.Items {
			if event.InvolvedObject.UID == original.UID && event.Type == corev1.EventTypeWarning && event.Reason == "FailedScheduling" &&
				(event.Source.Component == "default-scheduler" || event.ReportingController == "default-scheduler") && event.Message == refusal {
				fmt.Printf("actual scheduler refusal: pod %s/%s UID %s requested CPU %s maximum allocatable %dm event %s message %q; no feasible preemption candidate\n", w.Namespace, handle, original.UID, requested.String(), maximum, event.UID, refusal)
				witnessed = true
				break
			}
		}
		if witnessed {
			break
		}
		select {
		case <-w.Ctx.Done():
			return StepOutcome{}, fmt.Errorf("no matching scheduler event: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	ctx, cancel := context.WithTimeout(w.Ctx, max(startup, scheduling)+5*time.Second)
	start := time.Now()
	result, waitErr := process.Wait(ctx)
	elapsed := time.Since(start)
	cancel()
	// Production has no test-only node affinity. Reject topology/capacity
	// changes: every node observed for this run must remain too small.
	currentNodes, err := w.Clientset.CoreV1().Nodes().List(w.Ctx, metav1.ListOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	if len(currentNodes.Items) != len(nodeCapacity) {
		return StepOutcome{}, fmt.Errorf("scheduling node membership changed")
	}
	for _, node := range currentNodes.Items {
		if nodeCapacity[node.Name+"/"+string(node.UID)] != node.Status.Allocatable.Cpu().MilliValue() {
			return StepOutcome{}, fmt.Errorf("scheduling node identity or CPU capacity changed")
		}
	}
	after, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	if err = unscheduledIdentity(after, original); err != nil {
		return StepOutcome{}, err
	}
	retained := false
	for _, condition := range after.Status.Conditions {
		retained = retained || condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" && condition.Message == refusal
	}
	if !retained || stdout.Len() != 0 {
		return StepOutcome{}, fmt.Errorf("scheduler refusal changed or the resource command ran")
	}
	fmt.Printf("actual scheduling timeout: pod %s/%s UID %s remains unbound Pending; resource %T Wait %s startup %s scheduling %s; no container execution\n", w.Namespace, handle, original.UID, process, elapsed, startup, scheduling)
	return StepOutcome{Err: waitErr, Message: errorMessage(waitErr), Stderr: stderr.String(), ExitStatus: result.ExitStatus, schedulingRefusal: refusal}, nil
}

func unscheduledIdentity(pod, original *corev1.Pod) error {
	if pod.UID != original.UID || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" ||
		len(pod.Status.ContainerStatuses) != 0 || len(pod.Status.InitContainerStatuses) != 0 || pod.Status.NominatedNodeName != "" {
		return fmt.Errorf("unschedulable pod changed identity, bound or started: UID %s node %q status %+v", pod.UID, pod.Spec.NodeName, pod.Status)
	}
	return nil
}
