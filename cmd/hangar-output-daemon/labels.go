package main

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// FacetLabeler advertises this node's facets to the scheduler.
//
// Two labels, not one, and they go on in order. The base label attests that
// this node's daemon, runtime and control key are a homogeneous attested cohort
// for the exact-execution protocol; the output label attests the capture
// extension ON TOP of it. A node advertising output without base would be
// claiming a capture cohort with no exact-execution protocol underneath, and a
// capture pod's affinity requires both, so a pod placed on that node would have
// its hold refused on arrival.
//
// Neither label is authority. The authenticated handshake is, and the activation
// epoch row is the authority above that. What the label buys is that the pod
// does not land somewhere the hold could never be acknowledged.
type FacetLabeler struct {
	nodes kubernetes.Interface
	node  string
}

// NewFacetLabeler returns nil when there is no node to label.
//
// Nil is a real answer rather than an error: this daemon runs outside Kubernetes
// in every test in this package and in the tier-2 conformance suite, and
// refusing to start there would be the chart's rule enforced in the wrong
// process. The chart is what makes --node-name present in a cluster.
func NewFacetLabeler(client kubernetes.Interface, node string) *FacetLabeler {
	if client == nil || node == "" {
		return nil
	}

	return &FacetLabeler{nodes: client, node: node}
}

// Advertise puts the base facet on, then the output facet if there is one.
//
// TWO patches and not one. They are two claims made at two different moments:
// the base facet is ready when the execution ledger, the control key, the
// protocol and the runtime handshake pass, and the output facet only once the
// source ledger, the publisher, the receipt key ring and the active output epoch
// pass as well. One patch would make the second claim true at the instant the
// first one became true, which is the thing the two labels exist to keep apart.
func (labeler *FacetLabeler) Advertise(ctx context.Context, output bool) error {
	if labeler == nil {
		return nil
	}
	if err := labeler.set(ctx, executioncontrol.ReadyLabel, ready()); err != nil {
		return err
	}
	if !output {
		return nil
	}

	return labeler.set(ctx, outputReadyLabel, ready())
}

// WithdrawOutput takes the output facet off and leaves the base facet on.
//
// Req 59's output-only downgrade: a node that stopped advertising exact control
// would strand every controlled execution the sibling track placed on it, so
// the base label is not this operation's business.
func (labeler *FacetLabeler) WithdrawOutput(ctx context.Context) error {
	if labeler == nil {
		return nil
	}

	return labeler.set(ctx, outputReadyLabel, nil)
}

// WithdrawAll takes both off, output first.
func (labeler *FacetLabeler) WithdrawAll(ctx context.Context) error {
	if labeler == nil {
		return nil
	}
	if err := labeler.WithdrawOutput(ctx); err != nil {
		return err
	}

	return labeler.set(ctx, executioncontrol.ReadyLabel, nil)
}

// outputReadyLabel is output.ReadyLabel, named here because the parameter of
// Advertise shadows the package.
const outputReadyLabel = output.ReadyLabel

func ready() *string {
	value := "ready"

	return &value
}

// set patches ONE label. A nil value removes it.
//
// A merge patch rather than a read-modify-write, because two daemons' labelers
// and the artifact daemon's all patch the same Node object, and a full update
// would be last-writer-wins over labels this process does not own.
func (labeler *FacetLabeler) set(ctx context.Context, label string, value *string) error {
	labels := map[string]any{label: nil}
	if value != nil {
		labels[label] = *value
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"labels": labels},
	})
	if err != nil {
		return fmt.Errorf("%w: building the node label patch: %v", output.ErrIncomplete, err)
	}

	if _, err := labeler.nodes.CoreV1().Nodes().Patch(ctx, labeler.node,
		types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("%w: patching %s on node %s: %v",
			output.ErrInfrastructure, label, labeler.node, err)
	}

	return nil
}
