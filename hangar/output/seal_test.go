package output

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// Sealing is two halves owned by two actors, and the daemon half cannot
// complete without the ATC half.
//
// Requirement 14: after sealing wins, JetBridge revokes or terminates every
// admitted writer and waits until all tickets *in the captured drain set* are
// durably closed; for pod writers it must also observe `terminated` status for
// every container before deleting that UID, and "a zero-looking ticket count
// without the captured drain set is not proof". Requirement 15: no
// canonicalization until both halves hold.
//
// A single blocking Seal cannot express that. The ATC cannot learn the captured
// drain set — which is what tells it *which* pod writers to terminate — until
// the call returns, and the call cannot return confirmed until the ATC has
// terminated them. So the boundary is two operations, the source ledger has two
// statements for it (`seal_started`, `seal_confirmed`), and the acknowledgement
// kind is bound to the state it proves rather than carried alongside a boolean.
//
// Reqs 14, 15, 17; AC 4.

const (
	testExecutionID     = executioncontrol.ExecutionID("0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b")
	testNodeUID         = executioncontrol.NodeUID("node-9f2b1d4c")
	testPodUID          = executioncontrol.PodUID("3f1b2c4d-5e6f-4708-9a1b-2c3d4e5f6071")
	testHandoffID       = HandoffID("6b1e9d40-2a77-4c11-8f3e-5d0a9c8b7e62")
	testSourceLeaseID   = SourceLeaseID("a4d2c8f1-9e03-4b55-86ad-71f0c3e29b48")
	testWriterTicketA   = WriterTicketID("d0c7b6a5-4e3f-4210-9876-543210fedcba")
	testWriterTicketB   = WriterTicketID("b1c2d3e4-f506-4172-8394-a5b6c7d8e9f0")
	testUnknownTicketID = WriterTicketID("11112222-3333-4444-8555-666677778888")
)

func testIdentity() executioncontrol.Identity {
	return executioncontrol.Identity{ExecutionID: testExecutionID, Fence: 7}
}

func testIncarnation() SourceIncarnation {
	return SourceIncarnation{
		ExecutionID:      testExecutionID,
		NodeUID:          testNodeUID,
		HandleGeneration: 4,
		Output:           "built-image",
	}
}

// testAcknowledgement builds a structurally valid source-ledger statement of
// the given kind, so that every assertion below fails for the reason it names
// rather than for a missing field.
func testAcknowledgement(t *testing.T, kind CaptureAcknowledgementKind, ticket WriterTicketID) CaptureAcknowledgement {
	t.Helper()

	ack := CaptureAcknowledgement{
		ProtocolVersion: ProtocolVersion,
		Kind:            kind,
		Execution:       testIdentity(),
		ActivationEpoch: 3,
		LedgerSequence:  40,
		NodeUID:         testNodeUID,
		PodUID:          testPodUID,
		HandoffID:       testHandoffID,
		SourceLeaseID:   testSourceLeaseID,
		Incarnation:     testIncarnation(),
		WriterFence:     2,
		ObservedAt:      NewTimestamp(mustParse(t, "2026-09-08T21:47:04Z")),
		Signature:       "c2lnbmF0dXJlLXNlYWw",
	}
	if kind.concernsAWriterTicket() {
		ack.WriterTicketID = ticket
	}
	if kind == CaptureHoldAcknowledged {
		ack.WriterFence = 0
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("the test's own %s acknowledgement does not validate: %v", kind, err)
	}

	return ack
}

func testDrained(t *testing.T, ticket WriterTicketID) DrainedWriter {
	t.Helper()

	return DrainedWriter{
		WriterTicketID:       ticket,
		Closed:               testAcknowledgement(t, CaptureWriterTicketClosed, ticket),
		PodUID:               testPodUID,
		ContainersTerminated: true,
	}
}

func testSealStarted(t *testing.T, drainSet ...WriterTicketID) SealStarted {
	t.Helper()

	return SealStarted{
		Acknowledgement: testAcknowledgement(t, CaptureSealStarted, ""),
		DrainSet:        drainSet,
	}
}

// TestTheSealAcknowledgementKindIsBoundToTheSealState is the reviewer's M-L
// probe in the shape the split gives it. Before the split, `SealResult`
// carried an acknowledgement of any kind beside a `Confirmed` boolean, and
// `SealResult{<hold_acknowledged>, Confirmed: true}.Validate()` returned nil:
// a "confirmed" seal backed by a pre-start hold statement.
func TestTheSealAcknowledgementKindIsBoundToTheSealState(t *testing.T) {
	hold := testAcknowledgement(t, CaptureHoldAcknowledged, "")

	t.Run("a pre-start hold cannot stand in for seal_started", func(t *testing.T) {
		started := SealStarted{Acknowledgement: hold, DrainSet: []WriterTicketID{testWriterTicketA}}
		if err := started.Validate(); err == nil {
			t.Fatal("a seal-started result backed by a hold_acknowledged statement was accepted. " +
				"The hold authorizes no writing at all and fences no admission; a seal that " +
				"cites it has not started.")
		} else {
			t.Logf("refused as required: %v", err)
		}
	})

	t.Run("a pre-start hold cannot stand in for seal_confirmed", func(t *testing.T) {
		if err := hold.ValidateAs(CaptureSealConfirmed); err == nil {
			t.Fatal("a hold_acknowledged statement was accepted as proof that every admitted " +
				"writer drained")
		} else {
			t.Logf("refused as required: %v", err)
		}
	})

	t.Run("each half accepts exactly its own statement", func(t *testing.T) {
		if err := testSealStarted(t, testWriterTicketA).Validate(); err != nil {
			t.Errorf("a seal_started statement with a captured drain set was refused: %v", err)
		}
		confirmed := testAcknowledgement(t, CaptureSealConfirmed, "")
		if err := confirmed.ValidateAs(CaptureSealConfirmed); err != nil {
			t.Errorf("a seal_confirmed statement was refused: %v", err)
		}
		if err := confirmed.ValidateAs(CaptureSealStarted); err == nil {
			t.Error("a seal_confirmed statement was accepted as a seal_started one; the two " +
				"halves are owned by two actors and only one of them has run")
		}
	})
}

// TestConfirmingASealRequiresTheCapturedDrainSet is Req 14's other half: a
// confirmation is evidence about the set captured when admission was fenced,
// never about whatever is outstanding now.
func TestConfirmingASealRequiresTheCapturedDrainSet(t *testing.T) {
	started := testSealStarted(t, testWriterTicketA, testWriterTicketB)

	confirmation := func(drained ...DrainedWriter) SealConfirmation {
		return SealConfirmation{
			Started:      started,
			Drained:      drained,
			CaptureFence: 1,
			ObservedAt:   NewTimestamp(mustParse(t, "2026-09-08T21:47:09Z")),
		}
	}

	t.Run("every captured ticket is accounted for", func(t *testing.T) {
		valid := confirmation(testDrained(t, testWriterTicketA), testDrained(t, testWriterTicketB))
		if err := valid.Validate(); err != nil {
			t.Fatalf("a confirmation closing every captured ticket was refused: %v", err)
		}
	})

	t.Run("a missing captured ticket is seal_unconfirmed", func(t *testing.T) {
		err := confirmation(testDrained(t, testWriterTicketA)).Validate()
		if err == nil {
			t.Fatal("a seal was confirmed while a ticket from its own captured drain set was " +
				"unaccounted for")
		}
		if !errors.Is(err, ErrSealUnconfirmed) {
			t.Errorf("an unproved drain must be ErrSealUnconfirmed, got %v", err)
		}
	})

	t.Run("an empty confirmation is not proof", func(t *testing.T) {
		if err := confirmation().Validate(); err == nil {
			t.Fatal("a confirmation naming no drained writer at all was accepted. A " +
				"zero-looking ticket count without the captured drain set is exactly what " +
				"Req 14 refuses as proof.")
		}
	})

	t.Run("a ticket outside the captured set is refused", func(t *testing.T) {
		extra := confirmation(
			testDrained(t, testWriterTicketA),
			testDrained(t, testWriterTicketB),
			testDrained(t, testUnknownTicketID),
		)
		if err := extra.Validate(); err == nil {
			t.Fatal("a confirmation carrying a ticket the seal never captured was accepted")
		}
	})

	t.Run("a pod writer needs its containers observed terminated", func(t *testing.T) {
		notTerminated := testDrained(t, testWriterTicketB)
		notTerminated.ContainersTerminated = false
		err := confirmation(testDrained(t, testWriterTicketA), notTerminated).Validate()
		if err == nil {
			t.Fatal("a pod writer was accepted as drained without a terminated status for its " +
				"containers; NotFound, Gone or a force deletion is never that proof")
		}
		if !errors.Is(err, ErrSealUnconfirmed) {
			t.Errorf("an unproved container boundary must be ErrSealUnconfirmed, got %v", err)
		}
	})

	t.Run("a ticket-closed statement for another ticket is refused", func(t *testing.T) {
		mismatched := testDrained(t, testWriterTicketB)
		mismatched.WriterTicketID = testWriterTicketA
		if err := confirmation(mismatched).Validate(); err == nil {
			t.Fatal("a close statement for one ticket was accepted as proof for another")
		}
	})

	t.Run("a hold statement is not a close statement", func(t *testing.T) {
		wrongKind := testDrained(t, testWriterTicketA)
		wrongKind.Closed = testAcknowledgement(t, CaptureHoldAcknowledged, "")
		if err := confirmation(wrongKind, testDrained(t, testWriterTicketB)).Validate(); err == nil {
			t.Fatal("a hold_acknowledged statement was accepted as proof that a writer closed")
		}
	})
}

// twoHalvedSourceControl proves the API itself has two halves. It would not
// compile against a single blocking Seal.
type twoHalvedSourceControl struct{}

func (twoHalvedSourceControl) AcknowledgeHold(context.Context, CaptureAdmission, SourceIncarnation,
	executioncontrol.PodUID) (CaptureAcknowledgement, error) {
	panic("not implemented")
}

func (twoHalvedSourceControl) AdmitWriter(context.Context, WriterAdmission) (CaptureAcknowledgement, error) {
	panic("not implemented")
}

func (twoHalvedSourceControl) RetireWriter(context.Context, WriterAdmission) (CaptureAcknowledgement, error) {
	panic("not implemented")
}

func (twoHalvedSourceControl) BeginSeal(context.Context, SealRequest) (SealStarted, error) {
	panic("not implemented")
}

func (twoHalvedSourceControl) ConfirmSeal(context.Context, SealConfirmation) (CaptureAcknowledgement, error) {
	panic("not implemented")
}

func (twoHalvedSourceControl) AcknowledgeRelease(context.Context, ReleaseIntent) (ReleaseAcknowledgement, error) {
	panic("not implemented")
}

var _ SourceControl = twoHalvedSourceControl{}
