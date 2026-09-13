package main

import (
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The two node labels, and the order they go on and come off in.
//
// A ready label is a scheduling HINT and never authority -- the authenticated
// handshake is that -- but the hint decides where the scheduler PUTS a pod, so
// a label that outlives the facet it advertises is a pod pending forever on a
// node that cannot admit it, and a label that appears before the facet is a pod
// whose hold is refused on arrival.
//
// So the order is asserted rather than assumed: base goes on first and comes
// off last, because the output facet is an extension of it and a node
// advertising output without base would be claiming a capture cohort with no
// exact-execution protocol underneath.

func nodeLabels(t *testing.T, client *fake.Clientset, name string) map[string]string {
	t.Helper()

	node, err := client.CoreV1().Nodes().Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the node back: %v", err)
	}

	return node.Labels
}

func freshNode(name string) *fake.Clientset {
	return fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
	})
}

func TestTheDaemonAdvertisesTheBaseFacetAloneWhenThatIsAllItHas(t *testing.T) {
	client := freshNode("node-a")
	labeler := NewFacetLabeler(client, "node-a")

	if err := labeler.Advertise(t.Context(), false); err != nil {
		t.Fatalf("advertising the base facet: %v", err)
	}

	labels := nodeLabels(t, client, "node-a")
	if labels[executioncontrol.ReadyLabel] != "ready" {
		t.Errorf("the base ready label is %q, not \"ready\"", labels[executioncontrol.ReadyLabel])
	}
	if _, found := labels[output.ReadyLabel]; found {
		t.Error("a base-control-only daemon advertised the output facet; a capture pod " +
			"scheduled onto it would find no publisher")
	}
	// And it never claims the strict-input capability, which attests inputs and
	// belongs to the other daemon entirely.
	if _, found := labels["concourse.dev/hangar-v1"]; found {
		t.Error("the output daemon advertised the strict-input capability")
	}
}

func TestTheOutputFacetIsAdvertisedOnlyOnTopOfTheBaseOne(t *testing.T) {
	client := freshNode("node-a")

	var patched []map[string]string
	client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patch, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		patched = append(patched, labelsInPatch(t, patch.GetPatch()))

		return false, nil, nil
	})

	if err := NewFacetLabeler(client, "node-a").Advertise(t.Context(), true); err != nil {
		t.Fatalf("advertising both facets: %v", err)
	}

	labels := nodeLabels(t, client, "node-a")
	for _, label := range []string{executioncontrol.ReadyLabel, output.ReadyLabel} {
		if labels[label] != "ready" {
			t.Errorf("%s is %q, not \"ready\"", label, labels[label])
		}
	}

	if len(patched) != 2 {
		t.Fatalf("the two facets were advertised in %d patches; they are two claims and a "+
			"single patch would make the output label appear at the same instant as the base "+
			"one it depends on", len(patched))
	}
	if _, base := patched[0][executioncontrol.ReadyLabel]; !base {
		t.Errorf("the FIRST patch is %v; the base facet goes on first, because the output "+
			"facet is an extension of it", patched[0])
	}
	if _, out := patched[1][output.ReadyLabel]; !out {
		t.Errorf("the SECOND patch is %v; it should be the output facet", patched[1])
	}
}

// Withdrawal is the mirror, and the mirror is what makes shutdown safe: the
// output label comes off before output shutdown and the base label before the
// daemon's.
func TestWithdrawalTakesTheOutputLabelOffFirstAndTheBaseLabelOffLast(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{
			executioncontrol.ReadyLabel: "ready",
			output.ReadyLabel:           "ready",
			"concourse.dev/other":       "keep-me",
		}},
	})

	var order []string
	client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patch, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		for label := range labelsInPatch(t, patch.GetPatch()) {
			order = append(order, label)
		}

		return false, nil, nil
	})

	labeler := NewFacetLabeler(client, "node-a")
	if err := labeler.WithdrawOutput(t.Context()); err != nil {
		t.Fatalf("withdrawing the output facet: %v", err)
	}

	labels := nodeLabels(t, client, "node-a")
	if _, found := labels[output.ReadyLabel]; found {
		t.Error("the output ready label survived an output-only withdrawal")
	}
	if labels[executioncontrol.ReadyLabel] != "ready" {
		t.Error("an output-only withdrawal took the BASE label too. Req 59: output-only " +
			"downgrade leaves base control available, and a node that stopped advertising " +
			"exact control would strand every controlled execution the sibling track placed " +
			"on it.")
	}

	if err := labeler.WithdrawAll(t.Context()); err != nil {
		t.Fatalf("withdrawing everything: %v", err)
	}
	labels = nodeLabels(t, client, "node-a")
	for _, label := range []string{executioncontrol.ReadyLabel, output.ReadyLabel} {
		if _, found := labels[label]; found {
			t.Errorf("%s survived a full withdrawal", label)
		}
	}
	if labels["concourse.dev/other"] != "keep-me" {
		t.Error("withdrawal removed a label this daemon does not own")
	}

	if len(order) < 3 {
		t.Fatalf("only %d labels were patched; the reactor saw too little for the order "+
			"assertion to mean anything: %v", len(order), order)
	}
	if order[0] != output.ReadyLabel {
		t.Errorf("the first label withdrawn was %q; the output facet comes off first", order[0])
	}
	if order[len(order)-1] != executioncontrol.ReadyLabel {
		t.Errorf("the last label withdrawn was %q; the base facet comes off last",
			order[len(order)-1])
	}
}

// A daemon that cannot advertise says so rather than serving a cohort nothing
// can be scheduled onto while believing it is ready.
func TestAFailedLabelPatchIsAnError(t *testing.T) {
	client := freshNode("node-a")
	client.PrependReactor("patch", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server said no")
	})

	if err := NewFacetLabeler(client, "node-a").Advertise(t.Context(), true); err == nil {
		t.Error("a failed patch was swallowed; a daemon whose readiness never reached the " +
			"node is one the scheduler will never send work to, and it should say so")
	}
}

// No node name is not an error: the daemon runs outside Kubernetes in every
// test in this package and in the conformance tier, and refusing to start there
// would be the chart's rule enforced in the wrong process.
func TestNoNodeNameMeansNoLabeling(t *testing.T) {
	if NewFacetLabeler(nil, "") != nil {
		t.Error("a labeler was built with no node to label")
	}
}

// labelsInPatch reads the labels out of a merge patch, so the ORDER assertions
// above are over what went to the API server rather than over what ended up on
// the object.
func labelsInPatch(t *testing.T, patch []byte) map[string]string {
	t.Helper()

	var decoded struct {
		Metadata struct {
			Labels map[string]*string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(patch, &decoded); err != nil {
		t.Fatalf("decoding the patch %s: %v", patch, err)
	}

	labels := map[string]string{}
	for name, value := range decoded.Metadata.Labels {
		if value == nil {
			labels[name] = ""

			continue
		}
		labels[name] = *value
	}

	return labels
}
