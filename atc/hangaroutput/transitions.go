// Package hangaroutput coordinates a durable output capture across the two
// systems that own it.
//
// PostgreSQL is authoritative for admission, branch choice, ownership leases,
// reservations and receipts. A node-local daemon is authoritative for the
// source incarnation, writer admission and sealing. Neither can commit for the
// other, so every step across the boundary is two steps and idempotent, and
// nothing here describes a pair as an atomic transaction -- because across two
// systems it is not one.
//
// NOTHING IN THIS PACKAGE KNOWS WHAT A CAPTURE IS FOR. There is no build, job,
// check, Run, workflow, ticket, agent or playbook here, and the guard in
// hangar/output/product_vocabulary_test.go covers this directory so that
// staying that way is checked rather than intended. The one place a deployment
// concept appears is as an OPAQUE locator the source dialer is handed back
// unchanged.
package hangaroutput

import (
	"fmt"

	"github.com/concourse/concourse/hangar/output"
)

// Transition is the one bounded step a coordinator may take next.
//
// The set is closed and the decision is a pure function of durable facts. That
// is the whole design: a capture crosses two systems, so recovery cannot ask
// "what was I doing" -- there is no such fact anywhere -- and a coordinator
// that kept one would be trusting memory over commits.
type Transition string

const (
	// TransitionNone is a handoff with nothing owed. It is not "finished
	// successfully": a settled no_capture, a closed cancellation and a
	// registered receipt all reach it, and which of them happened is the
	// disposition's question rather than this one's.
	TransitionNone Transition = "none"

	// TransitionAwaitOutcome is an admitted handoff whose producer has not
	// produced an authoritative outcome yet. It is deliberately not an error
	// and deliberately not terminal: the task stays externally nonterminal
	// while finish reconciliation resolves, and a coordinator that treated
	// "no answer yet" as "no capture" would be deciding the branch itself.
	TransitionAwaitOutcome Transition = "await_outcome"

	TransitionCommitCaptureReservation       Transition = "commit_capture_reservation"
	TransitionRecordNoCaptureIntent          Transition = "record_no_capture_intent"
	TransitionAcknowledgeNoCaptureRelease    Transition = "acknowledge_no_capture_release"
	TransitionRecordCancellationIntent       Transition = "record_cancellation_intent"
	TransitionAcknowledgeCancellationRelease Transition = "acknowledge_cancellation_release"

	// TransitionCancelCapture is cancellation arriving AFTER Stage 2 and
	// before the irreversible publish point. It cannot unwin the arbiter --
	// the branches are permanently exclusive -- so it records a terminal
	// cancellation on the capture row and the fenced release it then owes.
	TransitionCancelCapture Transition = "cancel_capture"

	TransitionBeginSeal   Transition = "begin_seal"
	TransitionConfirmSeal Transition = "confirm_seal"

	// TransitionResolveLogicalReservation is canonicalization plus the durable
	// resolution, and it is one transition because the resolution is what
	// makes the canonicalization mean anything. It runs before the first
	// object create, which is the ordering requirement 21 exists to state.
	TransitionResolveLogicalReservation Transition = "resolve_logical_reservation"

	// TransitionPublish creates the object and records that a create was
	// attempted. It commits nothing about the object itself: the ref is not
	// durable until a verified receipt registers it, and a create whose
	// response was lost is converged by repeating the same identity.
	TransitionPublish Transition = "publish"

	// TransitionRegisterReceipt converges the object, obtains a fresh
	// per-capture receipt against a one-use challenge, verifies it, and
	// registers it. A receipt signed over old facts proves only that the facts
	// were once true, so the challenge is part of the transition.
	TransitionRegisterReceipt Transition = "register_receipt"

	// TransitionReleaseSource is the capture branch's own fenced release: a
	// capture that terminally cancelled or failed BEFORE the irreversible
	// publish point has decided something and released nothing.
	TransitionReleaseSource Transition = "release_source"

	// TransitionSettleOrphan is the only thing left past the irreversible
	// publish point. Cancellation cannot unmake an object, so the capture
	// settles a registered receipt or a terminal orphan -- and never a domain
	// binding.
	TransitionSettleOrphan Transition = "settle_orphan"
)

// Decision is a transition and the durable fact that selected it.
//
// The reason is part of the value rather than a log line because it is what a
// recovery review reads: "why is this handoff doing this" has to be answerable
// from the state, and a reason derived at decision time from the same facts is
// the only kind that cannot drift.
type Decision struct {
	Transition Transition
	Reason     string
}

func decide(transition Transition, format string, args ...any) Decision {
	return Decision{Transition: transition, Reason: fmt.Sprintf(format, args...)}
}

// Decide is the transition legality table, as a pure function.
//
// It is pure so that every crash half is a test with no database and no
// daemon: "the checkpoint committed and the acknowledgement did not" is a
// record, and what a coordinator does about it is one call. The impure half --
// which is the half that can lose an answer -- is Coordinator.Advance below,
// and it performs exactly the transition this returns.
func Decide(record output.HandoffRecord) (Decision, error) {
	if err := record.Validate(); err != nil {
		return Decision{}, err
	}

	if record.Disposition == nil {
		return decideUndisposed(record)
	}

	switch *record.Disposition {
	case output.DispositionNoCapture:
		return decideNoCapture(record)
	case output.DispositionPreReservationCancel:
		return decideCancelled(record)
	case output.DispositionCapture:
		return decideCapture(record)
	}

	return Decision{}, fmt.Errorf("%w: handoff %s is dispositioned %q",
		output.ErrUnknownMember, record.HandoffID, *record.Disposition)
}

// decideUndisposed is the arbiter race, from the side that has not won yet.
//
// Cancellation is asked FIRST, and that ordering is requirement 11's: before
// Stage 2, cancellation selects only pre_reservation_cancel, never no_capture.
// Asking about the producer's outcome first would let a cancelled handoff whose
// producer also failed reach no_capture, which is a branch confusion no later
// state can tell apart from a broken arbiter.
func decideUndisposed(record output.HandoffRecord) (Decision, error) {
	if record.CancellationRequested {
		return decide(TransitionRecordCancellationIntent,
			"cancellation reached handoff %s before any Stage 2 reservation committed",
			record.HandoffID), nil
	}

	if record.FinishWitness != nil {
		outcome := record.FinishWitness.Outcome
		if outcome != nil && outcome.Successful() {
			return decide(TransitionCommitCaptureReservation,
				"an authoritative successful finish witness exists for handoff %s and no "+
					"disposition has been recorded", record.HandoffID), nil
		}

		return decide(TransitionRecordNoCaptureIntent,
			"an authoritative non-success witness exists for handoff %s", record.HandoffID), nil
	}

	if record.FinishUnresolvable != "" {
		return decide(TransitionRecordNoCaptureIntent,
			"finish reconciliation for handoff %s is typed %s and cannot authorize capture",
			record.HandoffID, record.FinishUnresolvable), nil
	}

	return decide(TransitionAwaitOutcome,
		"handoff %s has no authoritative finish outcome yet; the task stays externally "+
			"nonterminal and the source stays held", record.HandoffID), nil
}

// decideNoCapture is the two recoverable halves.
//
// Between them the outcome stays pending, the source stays held and destructive
// cleanup is forbidden. That is the whole reason the branch is two halves and
// not one: neither system can commit for the other.
func decideNoCapture(record output.HandoffRecord) (Decision, error) {
	if record.ReleaseAcknowledged {
		return decide(TransitionNone,
			"handoff %s reached no_capture and its exact fenced source release is acknowledged",
			record.HandoffID), nil
	}
	if !record.Source.Reserved() {
		return Decision{}, fmt.Errorf("%w: handoff %s reached no_capture with no source "+
			"incarnation on any node; a producer cannot have run without one", output.ErrCorrupt,
			record.HandoffID)
	}

	return decide(TransitionAcknowledgeNoCaptureRelease,
		"handoff %s recorded no_capture release intent %s and no daemon acknowledgement of it "+
			"is durable yet", record.HandoffID, record.ReleaseIntentID), nil
}

// decideCancelled is the third branch, and the fork it takes is whether there
// are bytes on a node.
//
// That fork used to be "was a hold acknowledged", and the Phase 4 ruling makes
// the two different questions: the incarnation is reserved and its directory
// created BEFORE the producing Pod exists, so a cancellation that beats the
// control init still has a directory to release. A branch that asked about the
// hold would leave one behind for every capture cancelled before it started.
func decideCancelled(record output.HandoffRecord) (Decision, error) {
	if !record.Source.Reserved() {
		return decide(TransitionNone,
			"handoff %s was cancelled before any source incarnation was reserved; there is "+
				"nothing on any node to release", record.HandoffID), nil
	}
	if record.ReleaseAcknowledged {
		return decide(TransitionNone,
			"handoff %s was cancelled and its exact fenced source release is acknowledged",
			record.HandoffID), nil
	}

	return decide(TransitionAcknowledgeCancellationRelease,
		"handoff %s recorded cancellation release intent %s over incarnation %s and no daemon "+
			"acknowledgement of it is durable yet", record.HandoffID, record.ReleaseIntentID,
		record.Source.Incarnation.Output), nil
}

// decideCapture walks the capture branch, and every step in it is a fact that
// is durable somewhere rather than a phase somebody remembered.
func decideCapture(record output.HandoffRecord) (Decision, error) {
	if !record.HasCaptureReservation() {
		return Decision{}, fmt.Errorf("%w: handoff %s won the arbiter as capture with no "+
			"reservation row; Stage 2 commits both or neither", output.ErrCorrupt,
			record.HandoffID)
	}

	switch record.State {
	case output.CaptureStateRegistered:
		return decide(TransitionNone,
			"handoff %s registered a verified receipt for %s", record.HandoffID, record.Ref.Digest), nil

	case output.CaptureStateCancelled, output.CaptureStateFailed:
		// Past the publish point an object may exist, and cancellation cannot
		// unmake it. There is nothing to release and nothing to re-decide:
		// what is left is a registered receipt or a terminal orphan.
		if record.PastIrreversiblePublishPoint {
			return decide(TransitionSettleOrphan,
				"handoff %s is %s past the irreversible publish point; a created object is "+
					"settled as a receipt or a terminal orphan, never released",
				record.HandoffID, record.State), nil
		}
		if record.ReleaseAcknowledged || !record.Source.Reserved() {
			return decide(TransitionNone,
				"handoff %s is terminally %s and owes no further release",
				record.HandoffID, record.State), nil
		}

		return decide(TransitionReleaseSource,
			"handoff %s is terminally %s before the irreversible publish point and its source "+
				"is still held", record.HandoffID, record.State), nil

	case output.CaptureStateUnresolved, output.CaptureStateResolved:
		return decideLiveCapture(record)
	}

	return Decision{}, fmt.Errorf("%w: handoff %s is in capture state %q",
		output.ErrUnknownMember, record.HandoffID, record.State)
}

func decideLiveCapture(record output.HandoffRecord) (Decision, error) {
	// An object create was attempted for a reservation that names no logical
	// identity. Requirement 21 exists so that this state is unreachable: every
	// possibly-created object has a pre-existing reservation recovery and
	// inventory can correlate without trusting a task-supplied key. If it is
	// reached anyway, the honest answer is that the correlation is broken, and
	// a coordinator that resolved a digest NOW would be resolving it after the
	// create and calling the ordering satisfied.
	if record.State == output.CaptureStateUnresolved && record.PastIrreversiblePublishPoint {
		return Decision{}, fmt.Errorf("%w: handoff %s attempted an object create with no "+
			"committed logical resolution; requirement 21 orders the resolution before the "+
			"first create precisely so that no created object is uncorrelatable",
			output.ErrCorrupt, record.HandoffID)
	}

	// Cancellation of a live capture is asked before any of the work, because
	// classification is read-only and precedes source-destructive cancellation:
	// a coordinator that sealed first would be racing a decision already made.
	if record.CancellationRequested && !record.PastIrreversiblePublishPoint {
		return decide(TransitionCancelCapture,
			"cancellation reached handoff %s after Stage 2 and before the irreversible publish "+
				"point; the capture is immutable and terminally cancels", record.HandoffID), nil
	}

	if record.State == output.CaptureStateResolved {
		if record.PastIrreversiblePublishPoint {
			return decide(TransitionRegisterReceipt,
				"handoff %s attempted an object create for %s and no verified receipt is "+
					"registered", record.HandoffID, record.Digest), nil
		}

		return decide(TransitionPublish,
			"handoff %s resolved %s and no object create has been attempted",
			record.HandoffID, record.Digest), nil
	}

	// Unresolved. The seal is the node's fact, so it is read from the node.
	if !record.SealBegun {
		return decide(TransitionBeginSeal,
			"handoff %s committed a producer checkpoint and no seal has begun over its source",
			record.HandoffID), nil
	}
	if !record.SealConfirmed {
		return decide(TransitionConfirmSeal,
			"handoff %s began sealing with %d ticket(s) in the drain set and the drain is not "+
				"confirmed", record.HandoffID, len(record.SealDrainSet)), nil
	}

	return decide(TransitionResolveLogicalReservation,
		"handoff %s is sealed and its reservation names no logical identity yet",
		record.HandoffID), nil
}

// SealPrecondition is the refusal a publication API owes a caller that offers
// anything other than a committed Stage 2 reservation.
//
// It is a function rather than a comment because the plan asks for it as an
// assertion: "no predeclaration row or acknowledgement can be supplied to a
// seal/publish/receipt method". A predeclaration carries no reservation id, so
// the check is one question, and every publication entry point asks it.
func requireCaptureAuthority(record output.HandoffRecord, operation string) error {
	return PublicationAuthority(record, operation)
}

// PublicationAuthority is the exported form, for a caller that wants to ask
// the question without performing the operation.
//
// It exists because "a predeclaration is refused" is a fact a test can only
// observe by asking: a coordinator never REACHES a seal from a predeclaration,
// so an observer that only saw "it did not seal" could not tell the guard from
// a coordinator that had simply not got round to it.
func PublicationAuthority(record output.HandoffRecord, operation string) error {
	if record.Disposition == nil || *record.Disposition != output.DispositionCapture {
		return fmt.Errorf("%w: %s was attempted for handoff %s from a predeclaration. A "+
			"predeclaration records identities before a pod may start; it is not capture "+
			"authority and no sealing or publication API accepts one",
			output.ErrUnauthorized, operation, record.HandoffID)
	}
	if !record.HasCaptureReservation() {
		return fmt.Errorf("%w: %s was attempted for handoff %s with no committed Stage 2 "+
			"reservation", output.ErrUnauthorized, operation, record.HandoffID)
	}
	if record.CaptureFence == 0 {
		return fmt.Errorf("%w: %s was attempted for handoff %s under no capture fence; a stale "+
			"owner may not seal, publish, sign, register, finalize or release",
			output.ErrUnauthorized, operation, record.HandoffID)
	}

	return nil
}

// TerminalExposurePermitted answers the one question a caller outside Hangar
// may ask about timing: may the thing that owns this execution now expose a
// terminal outcome?
//
// It is here rather than at the caller because the rule is Hangar's and the
// consequence is not: requirement 5 says finish disposition completes before a
// terminal outcome is exposed, requirement 6 says the task stays externally
// nonterminal while reconciliation, release or capture resolves, and neither is
// a fact a consumer could derive without reading Hangar's state. The answer is
// a bool and a REASON, so that a caller which is not allowed to proceed can say
// why rather than looking stuck.
//
// It says nothing about what the terminal outcome should BE. Capture success
// permits ordinary success, a terminal capture failure fails the step and a
// completed no_capture preserves the authoritative ordinary non-success -- all
// three are the consumer's to apply, and a product-neutral plane that returned
// them would be choosing a step's result.
func TerminalExposurePermitted(record output.HandoffRecord) (bool, string) {
	decision, err := Decide(record)
	if err != nil {
		return false, fmt.Sprintf("handoff %s is not in a state this plane can read: %v",
			record.HandoffID, err)
	}

	switch decision.Transition {
	case TransitionNone:
		return true, fmt.Sprintf("handoff %s owes nothing further", record.HandoffID)
	case TransitionSettleOrphan:
		// Past the irreversible publish point the object exists and the
		// capture settles under its own fence. Exposure still waits: an
		// unsettled orphan is a reservation that is inventory-ineligible until
		// its outcome is durably terminal.
		return false, fmt.Sprintf("handoff %s is past the irreversible publish point and its "+
			"outcome is not durably terminal yet", record.HandoffID)
	default:
		return false, fmt.Sprintf("handoff %s is incomplete: %s", record.HandoffID, decision.Reason)
	}
}
