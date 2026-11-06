package jetbridge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// relocationHarness is a Run step whose pause pod is pinned to a node that
// cannot take it: the pod is Pending, never bound, and Unschedulable.
type relocationHarness struct {
	t        *testing.T
	client   *fake.Clientset
	process  *execProcess
	stderr   *bytes.Buffer
	mu       sync.Mutex
	asked    [][]string
	created  int
	refuse   error
	conflict bool
}

func controlOn(node string) *runtime.ExecutionControl {
	return &runtime.ExecutionControl{
		Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted,
		Identity: executioncontrol.Identity{ExecutionID: "11111111-1111-4111-8111-111111111111", Fence: 1},
		Endpoint: "https://" + node, Capability: executioncontrol.ControlCapability("base-" + node),
		Node: &runtime.ExecutionNode{Name: node, UID: executioncontrol.NodeUID(node + "-uid")},
	}
}

func newRelocationHarness(t *testing.T) *relocationHarness {
	t.Helper()
	previous := relocationGrace
	relocationGrace = 0
	t.Cleanup(func() { relocationGrace = previous })

	h := &relocationHarness{t: t, stderr: &bytes.Buffer{}}
	config := NewConfig("test-ns", "")
	config.PodSchedulingTimeout = 10 * time.Second
	spec := runtime.ContainerSpec{
		TeamID: 1, Type: db.ContainerTypeTask, Dir: "/tmp/build/step",
		ImageSpec:        runtime.ImageSpec{ImageURL: "docker:///busybox"},
		ExecutionControl: controlOn("full"),
	}
	h.client = fake.NewSimpleClientset()
	container := newContainer("step-handle", db.ContainerMetadata{}, spec, nil, h.client, config, "worker",
		nil, nil, nil, stepPodIdentities{}, false, false)
	container.relocate = func(_ context.Context, exclude []string) (runtime.ContainerSpec, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.asked = append(h.asked, append([]string{}, exclude...))
		if h.refuse != nil {
			return container.containerSpec, h.refuse
		}
		for _, tried := range exclude {
			if tried == "roomy" {
				return container.containerSpec, errors.New("no candidate node")
			}
		}
		moved := container.containerSpec
		moved.ExecutionControl = controlOn("roomy")
		return moved, nil
	}

	// Every pod the runtime creates is Unschedulable until a spec binds it.
	h.client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		h.mu.Lock()
		h.created++
		pod.UID = types.UID("pod-" + string(rune('0'+h.created)))
		h.mu.Unlock()
		pod.ResourceVersion = "1"
		pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
		if pinnedNode(pod) != "full" {
			return false, nil, nil
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/2 nodes are available: 1 Insufficient cpu",
		}}}
		return false, nil, nil
	})
	// The API server's preconditions: the fake tracker does not check them.
	h.client.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		h.mu.Lock()
		conflict := h.conflict
		h.mu.Unlock()
		if conflict {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, action.(k8stesting.DeleteAction).GetName(),
				errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	if _, err := container.createPausePod(context.Background(), runtime.ProcessSpec{}); err != nil {
		t.Fatalf("creating the first pod: %v", err)
	}
	h.process = &execProcess{config: config, clientset: h.client, podName: container.podName,
		processIO: runtime.ProcessIO{Stderr: h.stderr}, container: container, control: container.containerSpec.ExecutionControl}
	return h
}

// bindWhen binds and starts the pod once ready says so, as the scheduler and
// kubelet would.
func (h *relocationHarness) bindWhen(ctx context.Context, ready func(*corev1.Pod) bool) chan error {
	done := make(chan error, 1)
	go func() {
		for {
			pod, err := h.client.CoreV1().Pods("test-ns").Get(ctx, h.process.podName, metav1.GetOptions{})
			if err == nil && ready(pod) {
				// The fake's watch does not replay from a resourceVersion as
				// the API server does: give the step's watcher, restarted on
				// the replacement pod, time to be watching before it changes.
				time.Sleep(100 * time.Millisecond)
				pod.Spec.NodeName = pinnedNode(pod)
				pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
				_, err = h.client.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
				done <- err
				return
			}
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return done
}

// pinnedNode is the node a pod's required affinity names by metadata.name.
func pinnedNode(pod *corev1.Pod) string {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil ||
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return ""
	}
	for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, field := range term.MatchFields {
			if field.Key == "metadata.name" && len(field.Values) == 1 {
				return field.Values[0]
			}
		}
	}
	return ""
}

func TestAnUnboundPodMovesToAnotherNode(t *testing.T) {
	h := newRelocationHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bound := h.bindWhen(ctx, func(pod *corev1.Pod) bool { return pinnedNode(pod) == "roomy" })

	if err := h.process.waitForRunning(ctx); err != nil {
		t.Fatalf("waitForRunning: %v (relocate asked %d times, %d pods created)", err, len(h.asked), h.created)
	}
	if err := <-bound; err != nil {
		t.Fatal(err)
	}
	if len(h.asked) != 1 || len(h.asked[0]) != 0 {
		t.Fatalf("relocate asked %v; want one move with nothing tried before", h.asked)
	}
	if h.process.control.Node.Name != "roomy" || h.process.container.containerSpec.ExecutionControl.Node.Name != "roomy" {
		t.Fatalf("the process still runs under the full node's control: %+v", h.process.control.Node)
	}
	if strings.Join(h.process.relocatedFrom, ",") != "full" {
		t.Fatalf("relocatedFrom = %v", h.process.relocatedFrom)
	}
	if h.created != 2 {
		t.Fatalf("%d pods created; want the original and one on the new node", h.created)
	}
	if !strings.Contains(h.stderr.String(), "moved the step to node roomy") {
		t.Fatalf("the build log does not say the step moved: %q", h.stderr.String())
	}
}

// A refused delete means the pod changed after it was seen unbound -- it may
// have been bound in between -- so it is never moved.
func TestAPodThatChangedIsNeverMoved(t *testing.T) {
	h := newRelocationHarness(t)
	h.conflict = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		<-h.bindWhen(ctx, func(*corev1.Pod) bool { return true })
	}()

	if err := h.process.waitForRunning(ctx); err != nil {
		t.Fatalf("waitForRunning: %v (relocate asked %d times, %d pods created)", err, len(h.asked), h.created)
	}
	if len(h.asked) != 0 {
		t.Fatalf("relocate was asked %v for a pod the delete refused", h.asked)
	}
	if h.process.control.Node.Name != "full" || h.created != 1 {
		t.Fatalf("the pod moved (node %s, %d pods)", h.process.control.Node.Name, h.created)
	}
}

// A move the Run refuses -- it started elsewhere, or no other node can take it
// -- puts the pod back where it was, and it waits there.
func TestARefusedMoveLeavesThePodWhereItWas(t *testing.T) {
	h := newRelocationHarness(t)
	h.refuse = errors.New("no other node")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bound := h.bindWhen(ctx, func(pod *corev1.Pod) bool { return string(pod.UID) == "pod-2" })

	if err := h.process.waitForRunning(ctx); err != nil {
		t.Fatalf("waitForRunning: %v (relocate asked %d times, %d pods created)", err, len(h.asked), h.created)
	}
	if err := <-bound; err != nil {
		t.Fatal(err)
	}
	if len(h.asked) != 1 {
		t.Fatalf("relocate asked %d times; a refusal is final", len(h.asked))
	}
	if h.process.control.Node.Name != "full" || !h.process.relocationSpent {
		t.Fatalf("after a refusal: node %s, spent %v", h.process.control.Node.Name, h.process.relocationSpent)
	}
	pod, err := h.client.CoreV1().Pods("test-ns").Get(ctx, h.process.podName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pinnedNode(pod) != "full" {
		t.Fatalf("the replacement pod is pinned to %q; want the node the execution is still admitted on", pinnedNode(pod))
	}
}
