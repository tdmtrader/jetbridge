package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// Disposition is the three-way exclusive branch a capture handoff takes. Every
// admitted capture reaches exactly one of them, permanently, and a one-row
// arbiter in PostgreSQL is what makes "exactly one" true rather than hoped for.
//
// The arbiter alone is not capture authority. Winning `capture` records the
// producer-completion checkpoint and creates an unresolved reservation; it does
// not seal, publish or bind anything.
type Disposition string

const (
	// DispositionCapture is Stage 2: an authoritative *successful* finish
	// witness, and only that, may enter it. It commits the producer-completion
	// checkpoint and the capture reservation together while verifying the exact
	// predeclared identity, source hold, finish witness, activation epoch and
	// database-clock deadline.
	DispositionCapture Disposition = "capture"

	// DispositionNoCapture is where every authoritative non-success and every
	// typed unresolved or lost reconciliation goes. It creates no seal,
	// receipt, candidate or result, and it completes in two crash-recoverable
	// halves: the caller's recorded fenced release intent, then the daemon's
	// acknowledgement of that exact release. Until both are authoritative the
	// source stays held, the external non-success stays pending, and
	// destructive cleanup is forbidden.
	DispositionNoCapture Disposition = "no_capture"

	// DispositionPreReservationCancel is cancellation winning before Stage 2.
	// With no acknowledged hold it closes without a daemon call; with one it
	// records its own exact fenced release intent. After the irreversible
	// publish point this branch is unavailable: cancellation then settles a
	// receipt or a terminal orphan, and can never create a domain binding.
	DispositionPreReservationCancel Disposition = "pre_reservation_cancel"
)

func Dispositions() []Disposition {
	return []Disposition{
		DispositionCapture,
		DispositionNoCapture,
		DispositionPreReservationCancel,
	}
}

func ParseDisposition(value string) (Disposition, error) {
	for _, member := range Dispositions() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: disposition %q; the vocabulary is %v",
		ErrUnknownMember, value, Dispositions())
}

func (disposition *Disposition) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseDisposition(text)
	if err != nil {
		return err
	}
	*disposition = parsed

	return nil
}

func (disposition Disposition) Validate() error {
	_, err := ParseDisposition(string(disposition))

	return err
}

// SuccessfulFinishDisposition is the Stage 2 commit.
//
// It is the only value in this package that may accompany a producer
// checkpoint, and it requires the finish acknowledgement itself rather than a
// caller's assertion that one exists. A Pod state, a disappeared process, a
// terminal build row or an in-memory result is not a witness (Req 4), and the
// type makes that structural: there is nowhere to put one.
type SuccessfulFinishDisposition struct {
	ProtocolVersion       string                           `json:"protocol_version"`
	Disposition           Disposition                      `json:"disposition"`
	Execution             executioncontrol.Identity        `json:"execution"`
	ActivationEpoch       executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID             HandoffID                        `json:"handoff_id"`
	SourceLeaseID         SourceLeaseID                    `json:"source_lease_id"`
	ProducerCheckpointID  OpaqueID                         `json:"producer_checkpoint_id"`
	Output                OutputName                       `json:"output"`
	CaptureFence          CaptureFence                     `json:"capture_fence"`
	CaptureDeadline       Timestamp                        `json:"capture_deadline_at"`
	FinishAcknowledgement executioncontrol.Acknowledgement `json:"finish_acknowledgement"`
}

func (disposition SuccessfulFinishDisposition) Validate() error {
	if err := validateProtocol(disposition.ProtocolVersion); err != nil {
		return err
	}
	if disposition.Disposition != DispositionCapture {
		return fmt.Errorf("%w: a successful-finish disposition is %s, not %s",
			ErrIncomplete, DispositionCapture, disposition.Disposition)
	}
	if err := disposition.Execution.Validate(); err != nil {
		return err
	}
	if disposition.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := disposition.HandoffID.Validate(); err != nil {
		return err
	}
	if err := disposition.SourceLeaseID.Validate(); err != nil {
		return err
	}
	if err := disposition.ProducerCheckpointID.Validate(); err != nil {
		return err
	}
	if err := disposition.Output.Validate(); err != nil {
		return err
	}
	if disposition.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero; a stale owner may not seal, publish, sign, "+
			"register or finalize, and a zero fence cannot be stale", ErrIncomplete)
	}
	if err := disposition.CaptureDeadline.Validate(); err != nil {
		return err
	}
	if err := disposition.FinishAcknowledgement.Validate(); err != nil {
		return err
	}
	if disposition.FinishAcknowledgement.Kind != executioncontrol.AcknowledgementFinish {
		return fmt.Errorf("%w: Stage 2 offered a %s acknowledgement; only an authoritative finish "+
			"may enter it", ErrIncomplete, disposition.FinishAcknowledgement.Kind)
	}
	if disposition.FinishAcknowledgement.Identity != disposition.Execution {
		return fmt.Errorf("%w: the finish witness is for a different exact execution",
			ErrInvalidIdentity)
	}
	if outcome := disposition.FinishAcknowledgement.Outcome; outcome == nil || !outcome.Successful() {
		return fmt.Errorf("%w: Stage 2 offered a non-successful producer outcome. This slice "+
			"captures only after the main command exits successfully; a failed or cancelled "+
			"producer follows existing task semantics", ErrIncomplete)
	}

	return nil
}

// NoCaptureReason is the closed set of ways a handoff reaches no_capture.
//
// The distinction matters because only the first is a decision. The other two
// are admissions that the truth could not be proved, and neither may ever be
// rounded into success or into permission to re-execute the command.
type NoCaptureReason string

const (
	// NoCaptureAuthoritativeNonSuccess is a durable finish or stop witness
	// whose outcome was not success. The task's ordinary non-success outcome is
	// preserved exactly.
	NoCaptureAuthoritativeNonSuccess NoCaptureReason = "authoritative_non_success"

	// NoCaptureUnresolved is a reconciliation that could not prove the exact
	// outcome. It is terminal for the capture and it is not a success.
	NoCaptureUnresolved NoCaptureReason = "unresolved"

	// NoCaptureLost is the ledger that would hold the answer being gone.
	NoCaptureLost NoCaptureReason = "lost"
)

func NoCaptureReasons() []NoCaptureReason {
	return []NoCaptureReason{
		NoCaptureAuthoritativeNonSuccess,
		NoCaptureUnresolved,
		NoCaptureLost,
	}
}

func ParseNoCaptureReason(value string) (NoCaptureReason, error) {
	for _, member := range NoCaptureReasons() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: no-capture reason %q; the vocabulary is %v",
		ErrUnknownMember, value, NoCaptureReasons())
}

func (reason *NoCaptureReason) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseNoCaptureReason(text)
	if err != nil {
		return err
	}
	*reason = parsed

	return nil
}

func (reason NoCaptureReason) Validate() error {
	_, err := ParseNoCaptureReason(string(reason))

	return err
}

// NoCaptureDisposition is the first half of the no-capture handoff.
//
// FinishAcknowledgement is present for an authoritative non-success and absent
// for a reconciliation, because there is no acknowledgement to show when the
// point is that none could be obtained. Validate enforces that correspondence
// in both directions: an `unresolved` disposition holding a finish witness is a
// reconciliation contradicting itself.
type NoCaptureDisposition struct {
	ProtocolVersion       string                            `json:"protocol_version"`
	Disposition           Disposition                       `json:"disposition"`
	Execution             executioncontrol.Identity         `json:"execution"`
	ActivationEpoch       executioncontrol.ActivationEpoch  `json:"activation_epoch"`
	HandoffID             HandoffID                         `json:"handoff_id"`
	SourceLeaseID         SourceLeaseID                     `json:"source_lease_id"`
	Reason                NoCaptureReason                   `json:"reason"`
	ReleaseIntentID       ReleaseIntentID                   `json:"release_intent_id"`
	FinishAcknowledgement *executioncontrol.Acknowledgement `json:"finish_acknowledgement,omitempty"`
}

func (disposition NoCaptureDisposition) Validate() error {
	if err := validateProtocol(disposition.ProtocolVersion); err != nil {
		return err
	}
	if disposition.Disposition != DispositionNoCapture {
		return fmt.Errorf("%w: a no-capture disposition is %s, not %s",
			ErrIncomplete, DispositionNoCapture, disposition.Disposition)
	}
	if err := disposition.Execution.Validate(); err != nil {
		return err
	}
	if disposition.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := disposition.HandoffID.Validate(); err != nil {
		return err
	}
	if err := disposition.SourceLeaseID.Validate(); err != nil {
		return err
	}
	if err := disposition.Reason.Validate(); err != nil {
		return err
	}
	if err := disposition.ReleaseIntentID.Validate(); err != nil {
		return err
	}

	if disposition.Reason == NoCaptureAuthoritativeNonSuccess {
		if disposition.FinishAcknowledgement == nil {
			return fmt.Errorf("%w: an authoritative non-success carries no witness; what makes it "+
				"authoritative is the acknowledgement, not the claim", ErrIncomplete)
		}
		if err := disposition.FinishAcknowledgement.Validate(); err != nil {
			return err
		}
		if disposition.FinishAcknowledgement.Identity != disposition.Execution {
			return fmt.Errorf("%w: the witness is for a different exact execution", ErrInvalidIdentity)
		}
		if outcome := disposition.FinishAcknowledgement.Outcome; outcome != nil && outcome.Successful() {
			return fmt.Errorf("%w: a successful producer outcome was routed to no_capture; the "+
				"branches are permanently exclusive", ErrIncomplete)
		}

		return nil
	}

	if disposition.FinishAcknowledgement != nil {
		return fmt.Errorf("%w: reason %s carries a finish witness; a reconciliation exists "+
			"precisely because none could be obtained", ErrIncomplete, disposition.Reason)
	}

	return nil
}

// PreReservationCancelDisposition is cancellation winning the arbiter before
// Stage 2.
//
// HoldAcknowledged is the fork: without an acknowledged hold there is nothing
// on any node to release and the branch closes with no daemon call at all, so a
// release intent would be a promise to no one. With one, the intent is required.
type PreReservationCancelDisposition struct {
	ProtocolVersion  string                           `json:"protocol_version"`
	Disposition      Disposition                      `json:"disposition"`
	Execution        executioncontrol.Identity        `json:"execution"`
	ActivationEpoch  executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID        HandoffID                        `json:"handoff_id"`
	SourceLeaseID    SourceLeaseID                    `json:"source_lease_id"`
	HoldAcknowledged bool                             `json:"hold_acknowledged"`
	ReleaseIntentID  ReleaseIntentID                  `json:"release_intent_id"`
}

func (disposition PreReservationCancelDisposition) Validate() error {
	if err := validateProtocol(disposition.ProtocolVersion); err != nil {
		return err
	}
	if disposition.Disposition != DispositionPreReservationCancel {
		return fmt.Errorf("%w: a pre-reservation cancel disposition is %s, not %s",
			ErrIncomplete, DispositionPreReservationCancel, disposition.Disposition)
	}
	if err := disposition.Execution.Validate(); err != nil {
		return err
	}
	if disposition.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := disposition.HandoffID.Validate(); err != nil {
		return err
	}
	if err := disposition.SourceLeaseID.Validate(); err != nil {
		return err
	}

	if disposition.HoldAcknowledged {
		return disposition.ReleaseIntentID.Validate()
	}
	if disposition.ReleaseIntentID != "" {
		return fmt.Errorf("%w: cancellation before any acknowledged hold recorded release intent "+
			"%q; there is nothing on any node to release", ErrIncomplete, disposition.ReleaseIntentID)
	}

	return nil
}

// ReleaseAcknowledgement is the second, daemon-side half of an exact fenced
// source release.
//
// All three branches can owe one. no_capture and pre_reservation_cancel owe it
// by construction. The capture branch owes it in exactly one situation, and the
// Phase 2 branch review's F7 is where that was settled: a capture that
// TERMINALLY CANCELS or fails before the irreversible publish point has decided
// something and released nothing, and the source is still held on a node until
// this statement says otherwise. `Settled` means the same thing on all three
// branches -- nothing is still owed -- so a cancelled capture with no
// acknowledged release is decided and unsettled, which is the state drain has
// to wait on. What the capture branch may NOT do is release after that point:
// past the publish point it settles a registered receipt or a terminal orphan,
// and there is nothing left to release.
//
// The two halves are deliberately two steps and idempotent. Nothing in this
// package describes them as a cross-system atomic commit, because they are not
// one: recovery repeats the same identity until committed-versus-noncommitted
// is known, and lease expiry by itself is never proof or release authority.
type ReleaseAcknowledgement struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Disposition     Disposition                      `json:"disposition"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	SourceLeaseID   SourceLeaseID                    `json:"source_lease_id"`
	ReleaseIntentID ReleaseIntentID                  `json:"release_intent_id"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
	LedgerSequence  executioncontrol.LedgerSequence  `json:"ledger_sequence"`
	ObservedAt      Timestamp                        `json:"observed_at"`
	Signature       string                           `json:"signature"`
}

func (ack ReleaseAcknowledgement) Validate() error {
	if err := validateProtocol(ack.ProtocolVersion); err != nil {
		return err
	}
	if err := ack.Disposition.Validate(); err != nil {
		return err
	}
	if err := ack.Execution.Validate(); err != nil {
		return err
	}
	if ack.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := ack.HandoffID.Validate(); err != nil {
		return err
	}
	if err := ack.SourceLeaseID.Validate(); err != nil {
		return err
	}
	if err := ack.ReleaseIntentID.Validate(); err != nil {
		return err
	}
	if err := ack.Incarnation.Validate(); err != nil {
		return err
	}
	if ack.Incarnation.ExecutionID != ack.Execution.ExecutionID {
		return fmt.Errorf("%w: the released incarnation belongs to a different execution",
			ErrInvalidIdentity)
	}
	if ack.LedgerSequence == 0 {
		return fmt.Errorf("%w: ledger sequence is zero", ErrIncomplete)
	}
	if err := ack.ObservedAt.Validate(); err != nil {
		return err
	}
	if ack.Signature == "" {
		return fmt.Errorf("%w: release acknowledgement is unsigned", ErrIncomplete)
	}

	return nil
}
