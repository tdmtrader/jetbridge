// Package policy is the output-plane role that reads bucket lifetime policy and
// IAM, and touches no object at all.
//
// That absence is the whole design. The attestor is the workload whose word the
// plane trusts about whether the bucket is safe to publish into, and a
// workload that could also read, create or delete an object would be a
// workload whose compromise costs the data rather than the assessment. So this
// package takes a Source with two methods, neither of which names an object,
// and there is no object-store type in its signature anywhere.
//
// What it can and cannot prove is stated plainly because Req 41 and AC 16
// require it: this package reports what a bucket's policy and IAM bindings
// *say*. It does not prove that IAM enforces anything -- no fake enforces a
// binding, and a real assertion about enforcement is a real-GCS observation
// recorded with its date and project. AC 17's lifecycle-rule behaviour is the
// same: this reads the rules, and Phase 7 and Phase 9 own what happens when one
// takes effect.
package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Source is what an attestor reads. Note that it has no object method.
type Source interface {
	ReadLifetimePolicy(ctx context.Context) (output.PolicySnapshot, error)
	ReadPrincipalBindings(ctx context.Context) (output.PrincipalBindings, error)
}

// Attestor turns a source's reading into an attestation for one epoch.
type Attestor struct {
	namespace output.OutputNamespace
	source    Source
	clock     output.Clock
}

// New builds an attestor.
//
// The clock is injected because every deadline in this plane is measured on a
// clock the caller supplies -- and because a policy observation carries the
// instant it was observed at, which the admission gate then compares against a
// detection bound. A wall clock here would make that bound untestable.
func New(namespace output.OutputNamespace, source Source, clock output.Clock) (*Attestor, error) {
	if namespace.IsZero() {
		return nil, fmt.Errorf("%w: the attestor needs a derived output namespace",
			output.ErrIncomplete)
	}
	if source == nil {
		return nil, fmt.Errorf("%w: the attestor needs a policy source", output.ErrIncomplete)
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: the attestor needs a clock", output.ErrIncomplete)
	}

	return &Attestor{namespace: namespace, source: source, clock: clock}, nil
}

var _ output.PolicyReader = (*Attestor)(nil)

// ReadLifetimePolicy observes the bucket's lifetime policy.
//
// A failure to read is not "unknown and therefore fine": the snapshot comes
// back at_risk with the reason, because "we have not checked" and "the check
// failed" are the same amount of evidence, and the schema's admission gate
// treats both the same way.
func (attestor *Attestor) ReadLifetimePolicy(ctx context.Context) (output.PolicySnapshot, error) {
	snapshot, err := attestor.source.ReadLifetimePolicy(ctx)
	if err != nil {
		return output.PolicySnapshot{}, fmt.Errorf("%w: reading the output bucket's lifetime "+
			"policy: %v", output.ErrAtRisk, err)
	}
	if snapshot.ActivationEpoch != attestor.namespace.ActivationEpoch() {
		return output.PolicySnapshot{}, fmt.Errorf("%w: the observation is for epoch %d and this "+
			"namespace was derived under %d", output.ErrConflict,
			snapshot.ActivationEpoch, attestor.namespace.ActivationEpoch())
	}
	if err := snapshot.Validate(); err != nil {
		return output.PolicySnapshot{}, err
	}

	return snapshot, nil
}

// ReadPrincipalBindings observes who may do what.
//
// The result is the raw grant list, deliberately: Req 41 is explicit that IAM
// provides neither a metadata-only object permission nor a prefix-scoped list,
// and a struct of booleans computed here would quietly assert otherwise.
func (attestor *Attestor) ReadPrincipalBindings(ctx context.Context) (output.PrincipalBindings, error) {
	bindings, err := attestor.source.ReadPrincipalBindings(ctx)
	if err != nil {
		return output.PrincipalBindings{}, fmt.Errorf("%w: reading the output bucket's IAM "+
			"bindings: %v", output.ErrAtRisk, err)
	}
	if len(bindings.Permissions) == 0 {
		return output.PrincipalBindings{}, fmt.Errorf("%w: the bucket reported no principal "+
			"bindings at all; an empty reading is an unread bucket, not an unbound one",
			output.ErrAtRisk)
	}

	return bindings, nil
}

// ObservedAt is the attestor's clock reading, exported so an attestation and
// the admission gate that reads it agree about which clock they are on.
func (attestor *Attestor) ObservedAt() output.Timestamp {
	return output.NewTimestamp(attestor.clock.Now().UTC())
}

// Epoch is the epoch this attestor speaks for.
func (attestor *Attestor) Epoch() executioncontrol.ActivationEpoch {
	return attestor.namespace.ActivationEpoch()
}

// StaleAfter is the detection bound the schema enforces, restated here so the
// attestor knows how often it must run rather than learning it from a refusal.
const StaleAfter = 15 * time.Minute
