package jetbridge

import (
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

const selectionNamespace = "steps"

// selectionNode is a node every readiness gate admits: the three plane labels,
// a hostname label equal to its name, and Ready.
func selectionNode(name, cpu, memory string, mutate ...func(*corev1.Node)) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: map[string]string{
			"concourse.dev/artifact-cache": "ready",
			executioncontrol.ReadyLabel:    "ready",
			output.ReadyLabel:              "ready",
			corev1.LabelHostname:           name,
		}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	for _, m := range mutate {
		m(node)
	}
	return node
}

func tainted(key, value string, effect corev1.TaintEffect) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Value: value, Effect: effect})
	}
}

func labelled(key, value string) func(*corev1.Node) {
	return func(n *corev1.Node) { n.Labels[key] = value }
}

// runningPod is a pod already bound to node that requests cpu and memory.
func runningPod(name, node, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: selectionNamespace},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name: "main",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory),
			}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func selectFrom(t *testing.T, config Config, spec runtime.ContainerSpec, objects ...k8sruntime.Object) string {
	t.Helper()
	config.Namespace = selectionNamespace
	name, uid, err := NewOutputSource(fake.NewSimpleClientset(objects...), config, nil).SelectNode(context.Background(), spec)
	if err != nil {
		t.Fatalf("SelectNode: %v", err)
	}
	if uid != "uid-"+name {
		t.Fatalf("SelectNode returned uid %q for node %q", uid, name)
	}
	return name
}

func requesting(cpuMillis, memoryBytes uint64) runtime.ContainerSpec {
	return runtime.ContainerSpec{Limits: runtime.ContainerLimits{CPURequest: &cpuMillis, MemoryRequest: &memoryBytes}}
}

const gib = 1 << 30

// The step pod tolerates the taint of the node it is steered to, so that node
// is a place a Run execution can be admitted. Skipping every NoSchedule node
// sent every Run step to the one untainted node however full it was.
func TestSelectNodeAdmitsTheTaintedNodeStepsAreSteeredTo(t *testing.T) {
	preferred := &StepNodeLabel{Key: "jetbridge.dev/battle-station", Value: "true"}
	got := selectFrom(t, Config{PreferredStepNode: preferred}, requesting(500, gib),
		selectionNode("a-services", "8", "16Gi"),
		selectionNode("b-station", "8", "16Gi",
			labelled("jetbridge.dev/battle-station", "true"),
			tainted("jetbridge.dev/battle-station", "true", corev1.TaintEffectNoSchedule)),
	)
	if got != "b-station" {
		t.Fatalf("selected %q; want the preferred node the step pod tolerates", got)
	}
}

func TestSelectNodeSkipsATaintTheStepPodDoesNotTolerate(t *testing.T) {
	for _, effect := range []corev1.TaintEffect{corev1.TaintEffectNoSchedule, corev1.TaintEffectNoExecute} {
		got := selectFrom(t, Config{}, requesting(500, gib),
			selectionNode("a-reserved", "64", "128Gi", tainted("dedicated", "gpu", effect)),
			selectionNode("b-open", "2", "4Gi"),
		)
		if got != "b-open" {
			t.Errorf("%s: selected %q; a pod that does not tolerate the taint can never bind there", effect, got)
		}
	}

	got := selectFrom(t, Config{StepTolerations: []corev1.Toleration{{
		Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule,
	}}}, requesting(500, gib),
		selectionNode("a-reserved", "64", "128Gi", tainted("dedicated", "gpu", corev1.TaintEffectNoSchedule)),
		selectionNode("b-open", "2", "4Gi"),
	)
	if got != "a-reserved" {
		t.Errorf("selected %q; a configured step toleration admits the tainted node", got)
	}
}

func TestSelectNodeHonoursTheRequiredStepNode(t *testing.T) {
	required := &StepNodeLabel{Key: "pool", Value: "builds"}
	got := selectFrom(t, Config{RequiredStepNode: required}, requesting(500, gib),
		selectionNode("a-big", "64", "128Gi"),
		selectionNode("b-small", "2", "4Gi", labelled("pool", "builds")),
	)
	if got != "b-small" {
		t.Fatalf("selected %q; the step pod requires the pool label", got)
	}
}

// The failure this exists for: the node chosen at admission had no room, and
// the pod waited on it while another node sat idle.
func TestSelectNodeChoosesANodeWithRoomOverAFullOne(t *testing.T) {
	preferred := &StepNodeLabel{Key: "jetbridge.dev/battle-station", Value: "true"}
	got := selectFrom(t, Config{PreferredStepNode: preferred}, requesting(2000, 4*gib),
		selectionNode("a-station", "8", "16Gi", labelled("jetbridge.dev/battle-station", "true")),
		selectionNode("b-other", "8", "16Gi"),
		runningPod("busy-1", "a-station", "4", "8Gi"),
		runningPod("busy-2", "a-station", "3", "6Gi"),
	)
	if got != "b-other" {
		t.Fatalf("selected %q; the preferred node has 1 CPU free and the step requests 2", got)
	}
}

func TestSelectNodePrefersTheEmptierNodeOverNameOrder(t *testing.T) {
	got := selectFrom(t, Config{}, requesting(500, gib),
		selectionNode("a-node", "8", "16Gi"),
		selectionNode("b-node", "8", "16Gi"),
		runningPod("busy", "a-node", "6", "12Gi"),
	)
	if got != "b-node" {
		t.Fatalf("selected %q; the first node by name is the busier one", got)
	}
}

// Finished pods hold nothing; the scheduler does not count them and neither
// does the selection.
func TestSelectNodeIgnoresFinishedPods(t *testing.T) {
	done := runningPod("done", "a-node", "7", "14Gi")
	done.Status.Phase = corev1.PodSucceeded
	failed := runningPod("failed", "a-node", "7", "14Gi")
	failed.Status.Phase = corev1.PodFailed
	got := selectFrom(t, Config{}, requesting(2000, 4*gib),
		selectionNode("a-node", "8", "16Gi"),
		selectionNode("b-node", "8", "16Gi"),
		done, failed,
		runningPod("small", "b-node", "1", "1Gi"),
	)
	if got != "a-node" {
		t.Fatalf("selected %q; a-node's only pods have finished", got)
	}
}

// With no node able to fit the step today, the step still gets a node -- the
// emptiest -- and waits there for capacity as it always has. Refusing would
// turn a busy cluster into a failed build.
func TestSelectNodeFallsBackToTheEmptiestNodeWhenNothingFits(t *testing.T) {
	got := selectFrom(t, Config{}, requesting(16000, 32*gib),
		selectionNode("a-node", "8", "16Gi"),
		selectionNode("b-node", "8", "16Gi"),
		runningPod("busy", "a-node", "4", "8Gi"),
	)
	if got != "b-node" {
		t.Fatalf("selected %q; want the emptiest node when nothing fits", got)
	}
}

func TestSelectNodeRefusesANodeThatCannotRunTheStep(t *testing.T) {
	notReady := selectionNode("a-down", "64", "128Gi")
	notReady.Status.Conditions[0].Status = corev1.ConditionUnknown
	cordoned := selectionNode("b-cordoned", "64", "128Gi", func(n *corev1.Node) { n.Spec.Unschedulable = true })
	deleting := selectionNode("c-deleting", "64", "128Gi", func(n *corev1.Node) {
		now := metav1.Now()
		n.DeletionTimestamp = &now
		n.Finalizers = []string{"x"}
	})
	renamed := selectionNode("d-renamed", "64", "128Gi", labelled(corev1.LabelHostname, "other"))
	got := selectFrom(t, Config{}, requesting(500, gib), notReady, cordoned, deleting, renamed, selectionNode("e-ok", "1", "2Gi"))
	if got != "e-ok" {
		t.Fatalf("selected %q; only e-ok can run a step", got)
	}

	config := Config{Namespace: selectionNamespace}
	_, _, err := NewOutputSource(fake.NewSimpleClientset(notReady, cordoned), config, nil).SelectNode(context.Background(), requesting(500, gib))
	if err == nil || !strings.Contains(err.Error(), "no ready output node") {
		t.Fatalf("err = %v; want no ready output node", err)
	}
}
