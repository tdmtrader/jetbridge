package executioncontrol

import (
	"context"
	"testing"
)

// This file is the compile-only generic consumer the plan's Phase 0 asks for.
//
// Its job is to fail to compile if the protocol ever needs an output, a source
// path, a consumer lifecycle or a product-domain value to be usable. It is
// written the way a caller with no interest in durable capture would write it:
// admit an execution, wait for the durable outcome, and ask whether the remains
// may be deleted. If a future edit made any of that impossible without naming a
// capture, this file would stop building — which is a cheaper alarm than
// discovering it in the sibling `exact_execution_control` track.
//
// It deliberately asserts almost nothing at runtime. The behaviour belongs to
// Phases 3 and 4; what is being pinned here is the shape.

// ordinaryConsumer controls an execution that has no output plane at all.
type ordinaryConsumer struct {
	control Client
}

// supervise is the whole ordinary lifecycle, expressed in base vocabulary only.
func (consumer ordinaryConsumer) supervise(ctx context.Context, envelope Envelope) (Classification, error) {
	if err := envelope.Validate(); err != nil {
		return "", err
	}

	observed, err := consumer.control.ObserveFinishOrStop(ctx, ObserveFinishOrStopRequest{
		ProtocolVersion:  ProtocolVersion,
		Identity:         envelope.Identity,
		WaitMilliseconds: 30_000,
	})
	if err != nil {
		return "", err
	}
	if err := observed.Validate(); err != nil {
		return "", err
	}
	if !observed.Classification.Terminal() {
		// Not knowing is a legitimate answer, and it is not a failure to
		// convert into one. Requirement 4's unresolved/lost distinction is this
		// branch.
		return observed.Classification, nil
	}

	eligible, err := consumer.control.DestructiveCleanupEligible(ctx, DestructiveCleanupEligibleRequest{
		ProtocolVersion: ProtocolVersion,
		Identity:        envelope.Identity,
	})
	if err != nil {
		return "", err
	}
	if err := eligible.Validate(); err != nil {
		return "", err
	}

	return observed.Classification, nil
}

// interrupt shows that stopping needs no reason on the wire: a drain, a
// deadline and a user cancellation are the same operation to this protocol.
func (consumer ordinaryConsumer) interrupt(ctx context.Context, envelope Envelope) error {
	result, err := consumer.control.RequestSourcePreservingStop(ctx, RequestSourcePreservingStopRequest{
		ProtocolVersion: ProtocolVersion,
		Identity:        envelope.Identity,
		Capability:      envelope.Capability,
	})
	if err != nil {
		return err
	}

	return result.Validate()
}

// classify is here so that all four operations are exercised by the compile.
func (consumer ordinaryConsumer) classify(ctx context.Context, identity Identity) (ClassifyResult, error) {
	return consumer.control.Classify(ctx, ClassifyRequest{
		ProtocolVersion: ProtocolVersion,
		Identity:        identity,
	})
}

// unimplementedClient is the minimum a Client can be. Its only purpose is the
// assertion below.
type unimplementedClient struct{}

func (unimplementedClient) Classify(context.Context, ClassifyRequest) (ClassifyResult, error) {
	panic("not implemented: this client exists to prove the interface is satisfiable")
}

func (unimplementedClient) ObserveFinishOrStop(context.Context, ObserveFinishOrStopRequest) (ObserveFinishOrStopResult, error) {
	panic("not implemented: this client exists to prove the interface is satisfiable")
}

func (unimplementedClient) RequestSourcePreservingStop(context.Context, RequestSourcePreservingStopRequest) (RequestSourcePreservingStopResult, error) {
	panic("not implemented: this client exists to prove the interface is satisfiable")
}

func (unimplementedClient) DestructiveCleanupEligible(context.Context, DestructiveCleanupEligibleRequest) (DestructiveCleanupEligibleResult, error) {
	panic("not implemented: this client exists to prove the interface is satisfiable")
}

var (
	_ Client           = unimplementedClient{}
	_ ordinaryConsumer = ordinaryConsumer{control: unimplementedClient{}}
)

// TestTheGenericConsumerCompiles states in the test output what the compiler
// already proved, so that a reader of a green run can see the claim.
func TestTheGenericConsumerCompiles(t *testing.T) {
	consumer := ordinaryConsumer{control: unimplementedClient{}}

	// Referencing every method keeps a rename from silently orphaning one.
	_ = consumer.supervise
	_ = consumer.interrupt
	_ = consumer.classify

	t.Log("a consumer with no output, source, capture or product-domain vocabulary " +
		"can drive the whole protocol")
}
