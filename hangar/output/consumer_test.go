package output

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The compile-only output consumers the plan's Phase 0 asks for.
//
// Their job is to fail to compile the day one of these APIs needs Run
// vocabulary to be usable. Everything a consumer needs here is either its own
// opaque identifier or a value Hangar derived: there is no name, no
// authorization, no finality, no causation and no retention policy, because
// those are the consumer's half of the boundary and Hangar must never be able
// to read them.
//
// The one behavioural assertion below is the rule the whole extension design
// rests on, and it is cheap enough to state now rather than in Phase 4.

// neutralConsumer binds published content to something of its own, and it is
// deliberately impossible to tell what.
type neutralConsumer struct {
	claims  ClaimRepository
	leases  ReadLeaseRepository
	settler CancelSettler

	// bindingID is the consumer's own opaque handle. Hangar stores it, compares
	// it and hands it back; it never reads it.
	bindingID OpaqueID
}

// bind is Req 30's shape: the consumer's own write and the Hangar acquire in
// one caller-owned transaction, so a binding cannot become visible before
// publication verification and the active claim commit.
func (consumer neutralConsumer) bind(ctx context.Context, tx Tx, claimID ClaimID, ref hangar.TreeRef, now Timestamp) error {
	// The consumer's own binding write goes here, on the same tx. What it says
	// is none of Hangar's business.
	if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil {
		return err
	}

	return consumer.claims.AcquireClaim(ctx, tx, ClaimAcquisition{
		ProtocolVersion:   ProtocolVersion,
		ClaimID:           claimID,
		Ref:               ref,
		ConsumerBindingID: consumer.bindingID,
		RequestedAt:       now,
	})
}

// republish is the transition T6 owns: the same claim id is retained while the
// consumer moves its binding from hidden to published. There is no second
// acquire, and this file would not compile if the API required one.
func (consumer neutralConsumer) republish(ctx context.Context, tx Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT 1`)

	return err
}

// unbind is Req 31: make the binding unusable and release protection together,
// so neither a dangling visible binding nor a leaked claim is a valid crash
// outcome.
func (consumer neutralConsumer) unbind(ctx context.Context, tx Tx, claimID ClaimID, ref hangar.TreeRef, now Timestamp) error {
	if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil {
		return err
	}

	return consumer.claims.ReleaseClaim(ctx, tx, ClaimRelease{
		ProtocolVersion: ProtocolVersion,
		ClaimID:         claimID,
		Ref:             ref,
		RequestedAt:     now,
	})
}

func (consumer neutralConsumer) read(ctx context.Context, tx Tx, request ReadLeaseRequest) (ReadLease, error) {
	return consumer.leases.AcquireReadLease(ctx, tx, request)
}

func (consumer neutralConsumer) abandon(ctx context.Context, tx Tx, handoff HandoffID) (HandoffStatus, error) {
	return consumer.settler.CancelOrSettle(ctx, tx, handoff)
}

// The unimplemented roles. They exist so the interfaces above are provably
// satisfiable without any of them being implemented yet.

type unimplementedRoles struct{}

func (unimplementedRoles) DeleteExactGeneration(context.Context, hangar.TreeRef, DeletePrecondition) (DeleteOutcome, error) {
	panic("not implemented")
}

// The capture-side caller-owned transaction methods. Phase 1 implements them
// in atc/db; naming them here is what stops that phase inventing the shape
// outside the leaf, which is what this checkpoint is for.

func (unimplementedRoles) PredeclareHandoff(context.Context, Tx, CaptureAdmission) error {
	panic("not implemented")
}

func (unimplementedRoles) CommitCaptureReservation(context.Context, Tx, SuccessfulFinishDisposition) (ReservationID, error) {
	panic("not implemented")
}

func (unimplementedRoles) ResolveLogicalReservation(context.Context, Tx, LogicalResolution) error {
	panic("not implemented")
}

func (unimplementedRoles) RecordNoCaptureIntent(context.Context, Tx, NoCaptureDisposition) error {
	panic("not implemented")
}

func (unimplementedRoles) AcknowledgeNoCaptureRelease(context.Context, Tx, ReleaseAcknowledgement) error {
	panic("not implemented")
}

func (unimplementedRoles) RecordPreReservationCancelIntent(context.Context, Tx, PreReservationCancelDisposition) error {
	panic("not implemented")
}

func (unimplementedRoles) AcknowledgePreReservationCancelRelease(context.Context, Tx, ReleaseAcknowledgement) error {
	panic("not implemented")
}

func (unimplementedRoles) RegisterReceipt(context.Context, Tx, ReceiptAdmission) error {
	panic("not implemented")
}

func (unimplementedRoles) AcquireClaim(context.Context, Tx, ClaimAcquisition) error {
	panic("not implemented")
}
func (unimplementedRoles) ReleaseClaim(context.Context, Tx, ClaimRelease) error {
	panic("not implemented")
}

func (unimplementedRoles) AcquireReadLease(context.Context, Tx, ReadLeaseRequest) (ReadLease, error) {
	panic("not implemented")
}

func (unimplementedRoles) RenewReadLease(context.Context, Tx, ReadLease) (ReadLease, error) {
	panic("not implemented")
}
func (unimplementedRoles) ReleaseReadLease(context.Context, Tx, ReadLease) error {
	panic("not implemented")
}

func (unimplementedRoles) ClassifyHandoff(context.Context, Tx, HandoffID) (HandoffStatus, error) {
	panic("not implemented")
}

func (unimplementedRoles) CancelOrSettle(context.Context, Tx, HandoffID) (HandoffStatus, error) {
	panic("not implemented")
}

var (
	_ Reclaimer           = unimplementedRoles{}
	_ CaptureRepository   = unimplementedRoles{}
	_ ClaimRepository     = unimplementedRoles{}
	_ ReadLeaseRepository = unimplementedRoles{}
	_ CancelSettler       = unimplementedRoles{}
	_ Tx                  = (*sql.Tx)(nil)
)

func TestTheNeutralConsumerCompiles(t *testing.T) {
	consumer := neutralConsumer{
		claims:    unimplementedRoles{},
		leases:    unimplementedRoles{},
		settler:   unimplementedRoles{},
		bindingID: "opaque-consumer-binding-1",
	}

	_ = consumer.bind
	_ = consumer.republish
	_ = consumer.unbind
	_ = consumer.read
	_ = consumer.abandon

	t.Log("a consumer with no name, authorization, finality, causation or retention policy " +
		"can bind, republish, unbind, read and abandon durable content")
}

// TestTheCaptureExtensionCannotForkTheBaseExecution is the behavioural half of
// what execution_inventory_test.go asserts structurally: even with the right
// field types, a capture that names a different execution or a different epoch
// than the envelope it extends must be refused.
func TestTheCaptureExtensionCannotForkTheBaseExecution(t *testing.T) {
	identity := executioncontrol.Identity{
		ExecutionID: "0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b",
		Fence:       7,
	}
	other := executioncontrol.Identity{
		ExecutionID: "1a2b3c4d-5e6f-4708-9a1b-2c3d4e5f6071",
		Fence:       7,
	}
	envelope := executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		ActivationEpoch: 3,
		NodeUID:         "node-9f2b1d4c",
		PodUID:          "3f1b2c4d-5e6f-4708-9a1b-2c3d4e5f6071",
		Capability:      "control-capability-opaque-token",
	}
	admission := CaptureAdmission{
		ProtocolVersion: ProtocolVersion,
		Execution:       identity,
		ActivationEpoch: 3,
		HandoffID:       "6b1e9d40-2a77-4c11-8f3e-5d0a9c8b7e62",
		SourceLeaseID:   "a4d2c8f1-9e03-4b55-86ad-71f0c3e29b48",
		Output:          "built-image",
		CaptureDeadline: NewTimestamp(mustParse(t, "2026-09-09T21:47:03Z")),
	}

	t.Run("a base execution with no capture is representable", func(t *testing.T) {
		if err := (ControlledExecution{Envelope: envelope}).Validate(); err != nil {
			t.Fatalf("an execution that opted into nothing must still be valid: %v", err)
		}
	})

	t.Run("a capture on the same identity and epoch is accepted", func(t *testing.T) {
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: admission},
		}
		if err := execution.Validate(); err != nil {
			t.Fatalf("expected the extension to be accepted: %v", err)
		}
		if !execution.HasDurableOutputCapture() {
			t.Error("HasDurableOutputCapture reported false for an execution carrying one")
		}
	})

	t.Run("a capture naming a different execution is refused", func(t *testing.T) {
		forked := admission
		forked.Execution = other
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension naming a different exact execution was accepted; that " +
				"is a second execution wearing the first one's envelope")
		}
	})

	t.Run("a capture naming a different fence is refused", func(t *testing.T) {
		forked := admission
		forked.Execution.Fence = identity.Fence + 1
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension under a different fence was accepted")
		}
	})

	t.Run("a capture naming a different activation epoch is refused", func(t *testing.T) {
		forked := admission
		forked.ActivationEpoch = envelope.ActivationEpoch + 1
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension under a different activation epoch was accepted; one " +
				"epoch attests both facets")
		}
	})
}

func mustParse(t *testing.T, text string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatalf("parsing %q: %v", text, err)
	}

	return parsed
}
