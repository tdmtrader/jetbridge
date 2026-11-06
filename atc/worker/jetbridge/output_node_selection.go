package jetbridge

import (
	"context"
	"fmt"
	"sort"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// selectExecutionNode chooses the node a Run execution is admitted on.
//
// The choice is made before the step pod exists, and the pod is then pinned to
// it by name, so the scheduler never gets a second opinion: a node chosen
// without room is a pod that waits Pending while another node sits idle. The
// choice therefore makes the scheduler's own checks first.
//
// A node is a candidate when its daemon serves the output plane (the three
// readiness labels), it is Ready, schedulable and not being deleted, its
// hostname label is its name (the pin is by name), every NoSchedule and
// NoExecute taint on it is one the step pod tolerates, and it carries the
// required step-node label when one is configured.
//
// Candidates are ranked: one with room for the step's requests first, then
// one carrying the preferred step-node label, then the one with the most
// capacity left after placing the step, then by name. Room is the node's
// allocatable less what the non-terminal pods in the worker namespace request.
// Pods in other namespaces are not visible to the web, so a node shared with
// the cluster's own services is fuller than it looks; reserving a node for
// steps (label, taint, PreferredStepNode) is what makes the count exact. With
// no candidate able to fit the step, the emptiest is still chosen and the pod
// waits there for capacity, as it did before selection looked.
func selectExecutionNode(ctx context.Context, client kubernetes.Interface, config Config, spec runtime.ContainerSpec) (string, string, error) {
	selector := labels.Set{
		"concourse.dev/artifact-cache": "ready",
		executioncontrol.ReadyLabel:    "ready", output.ReadyLabel: "ready",
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", "", err
	}
	tolerations := stepTolerations(config)
	var candidates []corev1.Node
	for _, node := range nodes.Items {
		if canRunStep(node, tolerations, config.RequiredStepNode) {
			candidates = append(candidates, node)
		}
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("%w: no ready output node", output.ErrInfrastructure)
	}

	used, err := namespaceRequestsByNode(ctx, client, config.Namespace)
	if err != nil {
		return "", "", fmt.Errorf("reading step pod requests for node selection: %w", err)
	}
	want := buildResourceRequirements(spec.Limits).Requests

	type ranked struct {
		node      corev1.Node
		fits      bool
		preferred bool
		free      float64
	}
	ranking := make([]ranked, 0, len(candidates))
	for _, node := range candidates {
		fits, free := placement(node.Status.Allocatable, used[node.Name], want)
		preferred := config.PreferredStepNode != nil && node.Labels[config.PreferredStepNode.Key] == config.PreferredStepNode.Value
		ranking = append(ranking, ranked{node: node, fits: fits, preferred: preferred, free: free})
	}
	sort.SliceStable(ranking, func(i, j int) bool {
		a, b := ranking[i], ranking[j]
		if a.fits != b.fits {
			return a.fits
		}
		if a.preferred != b.preferred {
			return a.preferred
		}
		if a.free != b.free {
			return a.free > b.free
		}
		return a.node.Name < b.node.Name
	})
	chosen := ranking[0].node
	return chosen.Name, string(chosen.UID), nil
}

func canRunStep(node corev1.Node, tolerations []corev1.Toleration, required *StepNodeLabel) bool {
	if node.Spec.Unschedulable || node.DeletionTimestamp != nil || node.Labels[corev1.LabelHostname] != node.Name {
		return false
	}
	if required != nil && node.Labels[required.Key] != required.Value {
		return false
	}
	for i := range node.Spec.Taints {
		taint := &node.Spec.Taints[i]
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		tolerated := false
		for j := range tolerations {
			if tolerations[j].ToleratesTaint(klog.Background(), taint, false) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeUsage is what the pods bound to one node request.
type nodeUsage struct {
	requests corev1.ResourceList
	pods     int64
}

// namespaceRequestsByNode sums, per node, the requests of the non-terminal
// pods in namespace -- the step pods and anything else the worker namespace
// runs. A finished pod holds nothing.
func namespaceRequestsByNode(ctx context.Context, client kubernetes.Interface, namespace string) (map[string]nodeUsage, error) {
	used := map[string]nodeUsage{}
	if namespace == "" {
		return used, nil
	}
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "status.phase!=Succeeded,status.phase!=Failed",
	})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		usage := used[pod.Spec.NodeName]
		if usage.requests == nil {
			usage.requests = corev1.ResourceList{}
		}
		for name, quantity := range podRequests(pod) {
			total := usage.requests[name]
			total.Add(quantity)
			usage.requests[name] = total
		}
		usage.pods++
		used[pod.Spec.NodeName] = usage
	}
	return used, nil
}

// podRequests is a pod's effective request, as the scheduler counts it: the
// larger of its containers' sum and its largest init container, plus overhead.
func podRequests(pod *corev1.Pod) corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, c := range pod.Spec.Containers {
		for name, quantity := range c.Resources.Requests {
			sum := total[name]
			sum.Add(quantity)
			total[name] = sum
		}
	}
	for _, c := range pod.Spec.InitContainers {
		for name, quantity := range c.Resources.Requests {
			if have, ok := total[name]; !ok || quantity.Cmp(have) > 0 {
				total[name] = quantity.DeepCopy()
			}
		}
	}
	for name, quantity := range pod.Spec.Overhead {
		sum := total[name]
		sum.Add(quantity)
		total[name] = sum
	}
	return total
}

// placement reports whether a pod requesting want fits beside used on a node
// with allocatable, and the share of the node's CPU and memory left after it:
// the scheduler's least-allocated score, without its weights.
func placement(allocatable corev1.ResourceList, used nodeUsage, want corev1.ResourceList) (bool, float64) {
	fits := true
	if capacity, ok := allocatable[corev1.ResourcePods]; ok && used.pods+1 > capacity.Value() {
		fits = false
	}
	var free float64
	var counted int
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		capacity, ok := allocatable[name]
		if !ok || capacity.IsZero() {
			continue
		}
		left := capacity.DeepCopy()
		left.Sub(used.requests[name])
		if request, ok := want[name]; ok {
			left.Sub(request)
		}
		if left.Sign() < 0 {
			fits = false
		}
		free += float64(left.MilliValue()) / float64(capacity.MilliValue())
		counted++
	}
	if counted > 0 {
		free /= float64(counted)
	}
	return fits, free
}
