package hangaroutput

// Every crash half, as a state.
//
// A capture crosses two systems, so the interesting failures are all of one
// shape: one side committed and the other did not, and the process that knew
// which is gone. There is no fact anywhere that says "I was sealing". So the
// question a recovery review actually has to answer is "given exactly these
// durable facts, what is legal next", and that is a pure function -- which is
// why this file has no database, no daemon and no clock. The half that can
// lose an answer is driven in coordinator_test.go against a real daemon.
//
// The table is written as CRASH HALVES rather than as states, because that is
// the review the checkpoint asks for: each row names the moment the process
// died and asserts both the transition taken and, where it matters, a
// transition NOT taken. A row with only a positive assertion would pass on a
// coordinator that always answered the same thing.
//
// Reqs 1-11, 21, 25-27; ACs 1, 2, 6, 8, 9.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

const (
	testHandoff   = output.HandoffID("11111111-1111-4111-8111-111111111111")
	testLease     = output.SourceLeaseID("22222222-2222-4222-8222-222222222222")
	testExecution = executioncontrol.ExecutionID("33333333-3333-4333-8333-333333333333")
	testReserve   = output.ReservationID("44444444-4444-4444-8444-444444444444")
	testIntent    = output.ReleaseIntentID("55555555-5555-4555-8555-555555555555")
)

func testTimestamp() output.Timestamp {
	return output.NewTimestamp(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
}

// predeclared is the record as it stands the moment a pod may start: identities
// and an activation epoch, and nothing that could authorize anything.
func predeclared() output.HandoffRecord {
	return output.HandoffRecord{
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		Execution:       executioncontrol.Identity{ExecutionID: testExecution, Fence: 1},
		ActivationEpoch: 7,
		Output:          "result",
		CaptureDeadline: testTimestamp(),
	}
}

// reserved adds the daemon's answer to `reserve-incarnation`, which in
// production exists before the Pod does.
func reserved(record output.HandoffRecord) output.HandoffRecord {
	record.Source = output.SourcePlacement{
		Locator: "node-a",
		Incarnation: output.SourceIncarnation{
			ExecutionID:      testExecution,
			NodeUID:          "node-uid-a",
			HandleGeneration: 4,
			Output:           "result",
		},
		Directory: "33333333-3333-4333-8333-333333333333.4/result",
	}

	return record
}

func held(record output.HandoffRecord) output.HandoffRecord {
	record = reserved(record)
	record.HoldAcknowledged = true

	return record
}

func witnessed(record output.HandoffRecord, successful bool) output.HandoffRecord {
	status := 0
	if !successful {
		status = 2
	}
	record.FinishWitness = &executioncontrol.Acknowledgement{
		Identity: record.Execution,
		Kind:     executioncontrol.AcknowledgementFinish,
		Outcome:  &executioncontrol.ExitOutcome{ExitCode: status},
	}

	return record
}

func dispositioned(record output.HandoffRecord, branch output.Disposition) output.HandoffRecord {
	record.Disposition = &branch

	return record
}

// captured is the state Stage 2 leaves behind: a checkpoint and an UNRESOLVED
// reservation, with no scope, digest, generation or receipt.
func captured(record output.HandoffRecord) output.HandoffRecord {
	record = dispositioned(held(record), output.DispositionCapture)
	record.ReservationID = testReserve
	record.ProducerCheckpointID = "opaque-producer-checkpoint"
	record.CaptureFence = 3
	record.State = output.CaptureStateUnresolved

	return record
}

func sealed(record output.HandoffRecord) output.HandoffRecord {
	record.SealBegun, record.SealConfirmed = true, true

	return record
}

func resolvedLogically(record output.HandoffRecord) output.HandoffRecord {
	record = sealed(record)
	record.State = output.CaptureStateResolved
	record.LogicalResolved = true
	record.Scope = hangar.Scope("o1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0")
	record.Digest = hangar.Digest("sha256:" + strings.Repeat("ab", 32))

	return record
}

// TestEveryCrashHalfSelectsExactlyOneLegalTransition is the recovery review, as
// a table.
func TestEveryCrashHalfSelectsExactlyOneLegalTransition(t *testing.T) {
	for _, row := range []struct {
		crash   string
		record  output.HandoffRecord
		want    Transition
		notWant []Transition
	}{
		{
			// The predeclaration committed and nothing else did. This is the
			// state a crash before the pod leaves, and the whole point of it
			// is that it authorizes nothing: not capture, not no_capture, not
			// a release.
			crash:   "after the predeclaration commit, before anything ran",
			record:  predeclared(),
			want:    TransitionAwaitOutcome,
			notWant: []Transition{TransitionCommitCaptureReservation, TransitionRecordNoCaptureIntent},
		},
		{
			crash:   "after the incarnation was reserved, before the hold was acknowledged",
			record:  reserved(predeclared()),
			want:    TransitionAwaitOutcome,
			notWant: []Transition{TransitionRecordNoCaptureIntent, TransitionBeginSeal},
		},
		{
			crash:   "after the hold was acknowledged, before the producer finished",
			record:  held(predeclared()),
			want:    TransitionAwaitOutcome,
			notWant: []Transition{TransitionRecordNoCaptureIntent, TransitionCommitCaptureReservation},
		},
		{
			// The child exited and no outcome record exists. Requirement 4:
			// process disappearance is not a witness, and inability to prove
			// success cannot authorize capture -- but it cannot authorize
			// no_capture either until reconciliation types it, because a
			// no_capture recorded here would close a handoff whose producer
			// may have succeeded.
			crash:   "after the child exited, before any outcome record",
			record:  held(predeclared()),
			want:    TransitionAwaitOutcome,
			notWant: []Transition{TransitionRecordNoCaptureIntent, TransitionCommitCaptureReservation},
		},
		{
			crash:   "after the daemon acknowledged a successful finish, before Stage 2 committed",
			record:  witnessed(held(predeclared()), true),
			want:    TransitionCommitCaptureReservation,
			notWant: []Transition{TransitionRecordNoCaptureIntent, TransitionBeginSeal},
		},
		{
			crash:   "after the daemon acknowledged a NON-success, before the disposition committed",
			record:  witnessed(held(predeclared()), false),
			want:    TransitionRecordNoCaptureIntent,
			notWant: []Transition{TransitionCommitCaptureReservation},
		},
		{
			// Reconciliation could not prove the outcome. It is terminal for
			// the capture and it is not a success; it is also not a producer
			// failure, and the reason vocabulary keeps them apart.
			crash: "after finish reconciliation typed the outcome unresolved",
			record: func() output.HandoffRecord {
				record := held(predeclared())
				record.FinishUnresolvable = output.NoCaptureUnresolved

				return record
			}(),
			want:    TransitionRecordNoCaptureIntent,
			notWant: []Transition{TransitionCommitCaptureReservation, TransitionAwaitOutcome},
		},
		{
			crash:   "after Stage 2 committed, before any seal began",
			record:  captured(predeclared()),
			want:    TransitionBeginSeal,
			notWant: []Transition{TransitionResolveLogicalReservation, TransitionPublish},
		},
		{
			crash: "after the seal began, before the drain was confirmed",
			record: func() output.HandoffRecord {
				record := captured(predeclared())
				record.SealBegun = true
				record.SealDrainSet = []output.WriterTicketID{"ticket-1"}

				return record
			}(),
			want:    TransitionConfirmSeal,
			notWant: []Transition{TransitionResolveLogicalReservation, TransitionPublish},
		},
		{
			crash:   "after the drain was confirmed, before the logical resolution committed",
			record:  sealed(captured(predeclared())),
			want:    TransitionResolveLogicalReservation,
			notWant: []Transition{TransitionPublish, TransitionRegisterReceipt},
		},
		{
			crash:   "after the logical resolution committed, before the first object create",
			record:  resolvedLogically(captured(predeclared())),
			want:    TransitionPublish,
			notWant: []Transition{TransitionRegisterReceipt, TransitionResolveLogicalReservation},
		},
		{
			// The create was attempted and the response was lost. The object
			// may exist. Repeating the same identity converges -- a receipt is
			// never signed for a generation nobody observed.
			crash: "after an object create was attempted, before any receipt registered",
			record: func() output.HandoffRecord {
				record := resolvedLogically(captured(predeclared()))
				record.PastIrreversiblePublishPoint = true

				return record
			}(),
			want:    TransitionRegisterReceipt,
			notWant: []Transition{TransitionPublish, TransitionReleaseSource, TransitionNone},
		},
		{
			crash: "after the receipt was registered",
			record: func() output.HandoffRecord {
				record := resolvedLogically(captured(predeclared()))
				record.PastIrreversiblePublishPoint = true
				record.State = output.CaptureStateRegistered
				record.Ref = hangar.TreeRef{Scope: record.Scope, Digest: record.Digest, Generation: 9}

				return record
			}(),
			want:    TransitionNone,
			notWant: []Transition{TransitionRegisterReceipt, TransitionPublish, TransitionReleaseSource},
		},
		{
			crash: "after a capture was terminally cancelled before the publish point, before " +
				"its release was acknowledged",
			record: func() output.HandoffRecord {
				record := sealed(captured(predeclared()))
				record.State = output.CaptureStateCancelled
				record.ReleaseIntentID = testIntent

				return record
			}(),
			want:    TransitionReleaseSource,
			notWant: []Transition{TransitionPublish, TransitionSettleOrphan, TransitionNone},
		},
		{
			crash: "after that release was acknowledged",
			record: func() output.HandoffRecord {
				record := sealed(captured(predeclared()))
				record.State = output.CaptureStateCancelled
				record.ReleaseIntentID = testIntent
				record.ReleaseAcknowledged = true

				return record
			}(),
			want:    TransitionNone,
			notWant: []Transition{TransitionReleaseSource},
		},
		{
			// Past the publish point, cancellation cannot unmake an object.
			crash: "after cancellation reached a capture already past the irreversible publish point",
			record: func() output.HandoffRecord {
				record := resolvedLogically(captured(predeclared()))
				record.PastIrreversiblePublishPoint = true
				record.State = output.CaptureStateCancelled

				return record
			}(),
			want:    TransitionSettleOrphan,
			notWant: []Transition{TransitionReleaseSource, TransitionNone},
		},
		{
			crash:   "after no_capture committed, before the daemon acknowledged the release",
			record:  dispositioned(held(predeclared()), output.DispositionNoCapture),
			want:    TransitionAcknowledgeNoCaptureRelease,
			notWant: []Transition{TransitionNone, TransitionCommitCaptureReservation},
		},
		{
			crash: "after the daemon acknowledged the no_capture release",
			record: func() output.HandoffRecord {
				record := dispositioned(held(predeclared()), output.DispositionNoCapture)
				record.ReleaseAcknowledged = true

				return record
			}(),
			want:    TransitionNone,
			notWant: []Transition{TransitionAcknowledgeNoCaptureRelease},
		},
		{
			// Cancellation before Stage 2, with an incarnation on a node. The
			// Phase 4 ruling makes this the ordinary case rather than the rare
			// one: the reservation exists before the Pod does.
			crash:  "after a pre-Stage-2 cancellation committed over a reserved source",
			record: dispositioned(reserved(predeclared()), output.DispositionPreReservationCancel),
			want:   TransitionAcknowledgeCancellationRelease,
			notWant: []Transition{
				TransitionNone, TransitionRecordNoCaptureIntent, TransitionCommitCaptureReservation,
			},
		},
		{
			crash: "after that cancellation's release was acknowledged",
			record: func() output.HandoffRecord {
				record := dispositioned(reserved(predeclared()),
					output.DispositionPreReservationCancel)
				record.ReleaseAcknowledged = true

				return record
			}(),
			want:    TransitionNone,
			notWant: []Transition{TransitionAcknowledgeCancellationRelease},
		},
		{
			// The one form that really does close with no daemon call: the
			// cancellation beat the reservation, so there is nothing anywhere.
			crash:   "after a cancellation that beat the reservation",
			record:  dispositioned(predeclared(), output.DispositionPreReservationCancel),
			want:    TransitionNone,
			notWant: []Transition{TransitionAcknowledgeCancellationRelease},
		},
		{
			crash:   "after cancellation was requested and before any arbiter branch was won",
			record:  cancellationRequested(held(predeclared())),
			want:    TransitionRecordCancellationIntent,
			notWant: []Transition{TransitionRecordNoCaptureIntent, TransitionCommitCaptureReservation},
		},
		{
			// The branch-confusion row, and the reason it is here: a producer
			// that FAILED and a cancellation that arrived are both true, and
			// requirement 11 says cancellation selects pre_reservation_cancel,
			// never no_capture. A coordinator that asked about the outcome
			// first would answer no_capture and nothing downstream could tell
			// that apart from a broken arbiter.
			crash:   "after cancellation was requested for a handoff whose producer also failed",
			record:  cancellationRequested(witnessed(held(predeclared()), false)),
			want:    TransitionRecordCancellationIntent,
			notWant: []Transition{TransitionRecordNoCaptureIntent},
		},
		{
			crash:   "after cancellation reached a live capture past Stage 2",
			record:  cancellationRequested(sealed(captured(predeclared()))),
			want:    TransitionCancelCapture,
			notWant: []Transition{TransitionResolveLogicalReservation, TransitionRecordNoCaptureIntent},
		},
	} {
		t.Run(row.crash, func(t *testing.T) {
			decision, err := Decide(row.record)
			if err != nil {
				t.Fatalf("deciding: %v", err)
			}
			if decision.Transition != row.want {
				t.Errorf("crash %s selects %q; the one legal transition is %q (reason given: %s)",
					row.crash, decision.Transition, row.want, decision.Reason)
			}
			for _, forbidden := range row.notWant {
				if decision.Transition == forbidden {
					t.Errorf("crash %s selected %q", row.crash, forbidden)
				}
			}
			if decision.Reason == "" {
				t.Error("the decision carries no reason; a recovery review reads it")
			}
		})
	}
}

func cancellationRequested(record output.HandoffRecord) output.HandoffRecord {
	record.CancellationRequested = true

	return record
}

// A created object with no committed logical resolution is CORRUPTION, not a
// state to recover from.
//
// Requirement 21 orders the resolution before the first create precisely so
// that every possibly-created object has a pre-existing reservation recovery
// and inventory can correlate. A coordinator that reached this state and
// resolved a digest now would be resolving it after the create and calling the
// ordering satisfied -- so it refuses instead, and says which ordering broke.
func TestACreateWithNoCommittedLogicalResolutionIsRefusedRatherThanRepaired(t *testing.T) {
	// The control: the same record without the create is an ordinary
	// resolution step, so this cannot pass on a Decide that refuses everything.
	control := sealed(captured(predeclared()))
	decision, err := Decide(control)
	if err != nil {
		t.Fatalf("a sealed, unresolved capture was refused: %v", err)
	}
	if decision.Transition != TransitionResolveLogicalReservation {
		t.Fatalf("the control selected %q", decision.Transition)
	}

	broken := control
	broken.PastIrreversiblePublishPoint = true
	if _, err := Decide(broken); !errors.Is(err, output.ErrCorrupt) {
		t.Errorf("an object create with no committed logical resolution was answered with %v; "+
			"requirement 21 makes that state unreachable and reaching it means the correlation "+
			"is broken", err)
	}
}

// Winning the arbiter as capture is not the reservation.
func TestACaptureBranchWithNoReservationRowIsRefused(t *testing.T) {
	record := dispositioned(held(predeclared()), output.DispositionCapture)
	if _, err := Decide(record); !errors.Is(err, output.ErrCorrupt) {
		t.Errorf("a capture branch with no reservation row was answered with %v; Stage 2 commits "+
			"the checkpoint and the reservation together or neither", err)
	}
}

// No predeclaration and no acknowledgement is capture authority.
//
// This is the plan's own sentence -- "assert no predeclaration row or
// acknowledgement can be supplied to a seal/publish/receipt method" -- and it
// is one question because a predeclaration has no reservation id to offer. The
// control is asserted first: a committed Stage 2 reservation IS admitted, so
// this cannot pass on a guard that refuses everything.
func TestNoPredeclarationOrAcknowledgementIsAcceptedByASealOrPublicationAPI(t *testing.T) {
	for _, operation := range []string{"begin_seal", "publish", "register_receipt"} {
		if err := requireCaptureAuthority(captured(predeclared()), operation); err != nil {
			t.Fatalf("%s refused a committed Stage 2 reservation: %v", operation, err)
		}

		err := requireCaptureAuthority(held(predeclared()), operation)
		if !errors.Is(err, output.ErrUnauthorized) {
			t.Errorf("%s admitted a predeclaration with an acknowledged hold: %v", operation, err)
		}
		// And it says WHICH thing was offered. "No reservation" is true of a
		// predeclaration and of half a dozen other states; a caller holding a
		// predeclaration and reading "no committed Stage 2 reservation" would
		// reasonably go and look for one.
		if !strings.Contains(err.Error(), "predeclaration") {
			t.Errorf("%s refused a predeclaration without naming it: %v", operation, err)
		}

		noCapture := dispositioned(held(predeclared()), output.DispositionNoCapture)
		if err := requireCaptureAuthority(noCapture, operation); !errors.Is(
			err, output.ErrUnauthorized) {
			t.Errorf("%s admitted a no_capture handoff: %v", operation, err)
		}

		unfenced := captured(predeclared())
		unfenced.CaptureFence = 0
		if err := requireCaptureAuthority(unfenced, operation); !errors.Is(
			err, output.ErrUnauthorized) {
			t.Errorf("%s admitted a reservation under no capture fence: %v", operation, err)
		}
	}
}

// A terminal outcome may not be exposed while the handoff is incomplete, and it
// is checked at EVERY boundary rather than at the one a reviewer thinks of.
//
// Requirement 5 says finish disposition completes before the task exposes a
// terminal outcome, and requirement 6 says the task stays externally nonterminal
// while reconciliation, no-capture release or capture resolves. The permitted
// cases are asserted first: without them "exposure is refused" would pass on a
// predicate that refuses everything, and the step would never finish at all.
func TestTerminalExposureWaitsForEveryBoundary(t *testing.T) {
	registered := resolvedLogically(captured(predeclared()))
	registered.PastIrreversiblePublishPoint = true
	registered.State = output.CaptureStateRegistered
	registered.Ref = hangar.TreeRef{
		Scope: registered.Scope, Digest: registered.Digest, Generation: 9,
	}

	settledNoCapture := dispositioned(held(predeclared()), output.DispositionNoCapture)
	settledNoCapture.ReleaseAcknowledged = true

	closedCancel := dispositioned(reserved(predeclared()), output.DispositionPreReservationCancel)
	closedCancel.ReleaseAcknowledged = true

	for name, record := range map[string]output.HandoffRecord{
		"a registered receipt":  registered,
		"a settled no_capture":  settledNoCapture,
		"a closed cancellation": closedCancel,
	} {
		if permitted, reason := TerminalExposurePermitted(record); !permitted {
			t.Errorf("%s does not permit a terminal outcome: %s", name, reason)
		}
	}

	orphan := resolvedLogically(captured(predeclared()))
	orphan.PastIrreversiblePublishPoint = true
	orphan.State = output.CaptureStateCancelled

	for name, record := range map[string]output.HandoffRecord{
		"a predeclared handoff":                   predeclared(),
		"a held source with no witness":           held(predeclared()),
		"a witnessed finish before Stage 2":       witnessed(held(predeclared()), true),
		"a committed reservation before the seal": captured(predeclared()),
		"a sealed capture before resolution":      sealed(captured(predeclared())),
		"a resolved capture before the create":    resolvedLogically(captured(predeclared())),
		"a no_capture before its release":         dispositioned(held(predeclared()), output.DispositionNoCapture),
		"a cancellation before its release":       dispositioned(reserved(predeclared()), output.DispositionPreReservationCancel),
		"an unsettled orphan":                     orphan,
	} {
		permitted, reason := TerminalExposurePermitted(record)
		if permitted {
			t.Errorf("%s permitted a terminal outcome; the handoff is incomplete", name)
		}
		if reason == "" {
			t.Errorf("%s was refused with no reason; a caller that waits has to say why", name)
		}
	}
}

// A hold over no reserved source cannot have happened, and saying so is what
// stops a record assembled from a partial read being decided on.
func TestAHoldOverNoReservedSourceIsRefused(t *testing.T) {
	record := predeclared()
	record.HoldAcknowledged = true

	if _, err := Decide(record); !errors.Is(err, output.ErrInvalidIdentity) {
		t.Errorf("a hold over no reserved incarnation was answered with %v", err)
	}
}

// A receipt is checked against the capture it is supposed to be FOR.
//
// The signature says the daemon said it; this says the daemon said it about
// this capture. Every field below is one a receipt replayed from another
// capture, source, output, epoch or fence would differ in — which is why the
// list is long rather than a spot check, and why each arm is tampered with
// individually here: in a live run the receipt is freshly obtained and every
// field agrees, so nothing on the happy path can tell a missing arm from a
// present one.
func TestAReceiptIsMatchedToTheCaptureItIsFor(t *testing.T) {
	record := resolvedLogically(captured(predeclared()))
	record.PastIrreversiblePublishPoint = true

	whole := output.Receipt{
		Claims: output.ReceiptClaims{
			Execution:            record.Execution,
			ProducerCheckpointID: record.ProducerCheckpointID,
			Incarnation:          record.Source.Incarnation,
			Output:               record.Output,
			Ref: hangar.TreeRef{
				Scope: record.Scope, Digest: record.Digest, Generation: 12,
			},
			ActivationEpoch: record.ActivationEpoch,
			WriterFence:     output.WriterFence(record.CaptureFence),
		},
	}

	// The control: the receipt this capture would really get.
	if err := checkReceiptClaims(whole, record, record.CaptureFence); err != nil {
		t.Fatalf("a receipt for this exact capture was refused: %v", err)
	}

	for name, tamper := range map[string]func(*output.Receipt){
		"another execution": func(r *output.Receipt) {
			r.Claims.Execution.ExecutionID = "99999999-9999-4999-8999-999999999999"
		},
		"another producer checkpoint": func(r *output.Receipt) {
			r.Claims.ProducerCheckpointID = "somebody-else's-checkpoint"
		},
		"another source incarnation": func(r *output.Receipt) {
			r.Claims.Incarnation.HandleGeneration = 99
		},
		"another output": func(r *output.Receipt) { r.Claims.Output = "report" },
		"another digest": func(r *output.Receipt) {
			r.Claims.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("cd", 32))
		},
		"another scope":            func(r *output.Receipt) { r.Claims.Ref.Scope = "someone-elses-scope" },
		"another activation epoch": func(r *output.Receipt) { r.Claims.ActivationEpoch = 99 },
		"another writer fence":     func(r *output.Receipt) { r.Claims.WriterFence = 99 },
	} {
		tampered := whole
		tamper(&tampered)
		if err := checkReceiptClaims(tampered, record, record.CaptureFence); err == nil {
			t.Errorf("a receipt bound to %s was admitted for this capture", name)
		}
	}
}
