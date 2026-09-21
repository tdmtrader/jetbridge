package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// SourceIncarnation is the server-issued identity of the bytes a capture will
// seal.
//
// It is four facts and no path. A handle string alone is never an identity
// (Req 7): handles are reused, and a reused handle with a stale generation is
// exactly the confusion this type exists to make impossible. The daemon derives
// and confines the actual location beneath its own managed steps/ root at the
// filesystem operation, so no caller ever names one.
//
// It carries no fence. A source incarnation is not a control operation; the
// fence that matters when acting on it is the WriterFence in the acknowledgement
// that admitted the action.
type SourceIncarnation struct {
	ExecutionID      executioncontrol.ExecutionID `json:"execution_id"`
	NodeUID          executioncontrol.NodeUID     `json:"node_uid"`
	HandleGeneration HandleGeneration             `json:"handle_generation"`
	Output           OutputName                   `json:"output"`
}

func (incarnation SourceIncarnation) Validate() error {
	if err := incarnation.ExecutionID.Validate(); err != nil {
		return err
	}
	if incarnation.NodeUID == "" {
		return fmt.Errorf("%w: source incarnation names no node", ErrInvalidIdentity)
	}
	if incarnation.HandleGeneration == 0 {
		return fmt.Errorf("%w: source incarnation has no handle generation; a handle string "+
			"alone is never an identity", ErrInvalidIdentity)
	}

	return incarnation.Output.Validate()
}

// CaptureAdmission is what is predeclared before the producing Pod may start.
//
// Everything here is known before anything has run. It records only the
// caller-generated handoff and provisional source-hold identities, the exact
// execution it extends, the declared output and the activation epoch. It
// carries no producer checkpoint, no success fact, no capture lease, no
// scope or digest, no receipt, no seal and no claim -- and the database API
// that accepts it will not accept a sealing or publication request, so no
// pre-start fact can be mistaken for capture authority.
//
// A late request cannot capture an already-running or completed task; admission
// is part of the admitted task configuration or it does not exist (Req 1).
type CaptureAdmission struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	SourceHoldID    SourceHoldID                     `json:"source_hold_id"`
	Output          OutputName                       `json:"output"`
	CaptureDeadline Timestamp                        `json:"capture_deadline_at"`
}

func (admission CaptureAdmission) Validate() error {
	if err := validateProtocol(admission.ProtocolVersion); err != nil {
		return err
	}
	if err := admission.Execution.Validate(); err != nil {
		return err
	}
	if admission.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero; a capture records the epoch it was "+
			"admitted under or it is not admitted", ErrIncomplete)
	}
	if err := admission.HandoffID.Validate(); err != nil {
		return err
	}
	if err := admission.SourceHoldID.Validate(); err != nil {
		return err
	}
	if err := admission.Output.Validate(); err != nil {
		return err
	}

	return admission.CaptureDeadline.Validate()
}

// CaptureAcknowledgementKind is the closed set of statements the source ledger
// makes about a capture.
//
// It is a separate vocabulary from executioncontrol.AcknowledgementKind rather
// than an extension of it, which is the structural form of the rule that
// base-only acknowledgements omit the extension's fields: a base ledger has no
// way to spell "hold acknowledged", and this one has no way to spell "start".
type CaptureAcknowledgementKind string

const (
	// CaptureHoldAcknowledged is the pre-start durable acknowledgement of the
	// non-authorizing provisional source hold. The producer's main process may
	// not start before it, and start and recovery fail closed until the hold
	// matches current execution admission.
	CaptureHoldAcknowledged CaptureAcknowledgementKind = "hold_acknowledged"

	// CaptureHoldReleased is the daemon's acknowledgement of one exact fenced
	// release. It is the second of the two crash-recoverable halves of a
	// no-capture or pre-reservation-cancel handoff; until it exists the source
	// remains held and destructive cleanup is forbidden.
	CaptureHoldReleased CaptureAcknowledgementKind = "hold_released"

	// CaptureWriterTicketIssued admits one writer over the incarnation. Issuing
	// a ticket and moving the incarnation from open to sealing serialize on one
	// durable boundary: either the ticket is in the seal's drain set, or
	// issuance receives a typed refusal.
	CaptureWriterTicketIssued CaptureAcknowledgementKind = "writer_ticket_issued"

	// CaptureWriterTicketClosed retires one admitted writer. Sealing waits for
	// every ticket in the captured drain set to reach this, and a zero-looking
	// current-ticket count without that captured set is not proof.
	CaptureWriterTicketClosed CaptureAcknowledgementKind = "writer_ticket_closed"

	// CaptureSealStarted fences future ticket admission. After it, no process
	// and no new Pod UID may receive a write-capable mount for the incarnation,
	// including after a daemon or ATC restart.
	CaptureSealStarted CaptureAcknowledgementKind = "seal_started"

	// CaptureSealConfirmed is both halves proved: admission fenced, and every
	// previously admitted writer drained. No canonicalization read begins
	// before it.
	CaptureSealConfirmed CaptureAcknowledgementKind = "seal_confirmed"
)

func CaptureAcknowledgementKinds() []CaptureAcknowledgementKind {
	return []CaptureAcknowledgementKind{
		CaptureHoldAcknowledged,
		CaptureHoldReleased,
		CaptureWriterTicketIssued,
		CaptureWriterTicketClosed,
		CaptureSealStarted,
		CaptureSealConfirmed,
	}
}

func ParseCaptureAcknowledgementKind(value string) (CaptureAcknowledgementKind, error) {
	for _, member := range CaptureAcknowledgementKinds() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: capture acknowledgement kind %q; the vocabulary is %v",
		ErrUnknownMember, value, CaptureAcknowledgementKinds())
}

func (kind *CaptureAcknowledgementKind) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseCaptureAcknowledgementKind(text)
	if err != nil {
		return err
	}
	*kind = parsed

	return nil
}

func (kind CaptureAcknowledgementKind) Validate() error {
	_, err := ParseCaptureAcknowledgementKind(string(kind))

	return err
}

// stateProved is the human-readable state each statement attests. It exists so
// that ValidateAs can say what was expected rather than only what was offered.
func (kind CaptureAcknowledgementKind) stateProved() string {
	switch kind {
	case CaptureHoldAcknowledged:
		return "the pre-start, non-authorizing source hold"
	case CaptureHoldReleased:
		return "one exact fenced release of that hold"
	case CaptureWriterTicketIssued:
		return "one admitted writer over the incarnation"
	case CaptureWriterTicketClosed:
		return "one admitted writer retired"
	case CaptureSealStarted:
		return "future writer admission fenced"
	case CaptureSealConfirmed:
		return "admission fenced and every captured writer drained"
	}

	return string(kind)
}

// concernsAWriterTicket reports whether this kind must name one.
func (kind CaptureAcknowledgementKind) concernsAWriterTicket() bool {
	return kind == CaptureWriterTicketIssued || kind == CaptureWriterTicketClosed
}

// CaptureAcknowledgement is an immutable, signed statement from the node's
// source ledger.
//
// It references the base execution identity and activation epoch; it does not
// restate them as fields of its own, and it never carries a second exact
// identity. That is the whole reason an optional extension cannot fork the base
// truth, and architecture_test.go fails the test suite if it starts to.
type CaptureAcknowledgement struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Kind            CaptureAcknowledgementKind       `json:"kind"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	LedgerSequence  executioncontrol.LedgerSequence  `json:"ledger_sequence"`
	NodeUID         executioncontrol.NodeUID         `json:"node_uid"`
	PodUID          executioncontrol.PodUID          `json:"pod_uid"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	SourceHoldID    SourceHoldID                     `json:"source_hold_id"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
	WriterTicketID  WriterTicketID                   `json:"writer_ticket_id"`
	WriterFence     WriterFence                      `json:"writer_fence"`
	ObservedAt      Timestamp                        `json:"observed_at"`
	Signature       string                           `json:"signature"`
}

func (ack CaptureAcknowledgement) Validate() error {
	if err := validateProtocol(ack.ProtocolVersion); err != nil {
		return err
	}
	if err := ack.Kind.Validate(); err != nil {
		return err
	}
	if err := ack.Execution.Validate(); err != nil {
		return err
	}
	if ack.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if ack.LedgerSequence == 0 {
		return fmt.Errorf("%w: ledger sequence is zero", ErrIncomplete)
	}
	if ack.NodeUID == "" {
		return fmt.Errorf("%w: acknowledgement names no node", ErrIncomplete)
	}
	if err := ack.HandoffID.Validate(); err != nil {
		return err
	}
	if err := ack.SourceHoldID.Validate(); err != nil {
		return err
	}
	if err := ack.Incarnation.Validate(); err != nil {
		return err
	}
	if ack.Incarnation.ExecutionID != ack.Execution.ExecutionID {
		return fmt.Errorf("%w: the incarnation belongs to a different execution than the "+
			"acknowledgement", ErrInvalidIdentity)
	}
	if ack.Incarnation.NodeUID != ack.NodeUID {
		return fmt.Errorf("%w: the incarnation belongs to a different node than the "+
			"acknowledgement", ErrInvalidIdentity)
	}
	if err := ack.ObservedAt.Validate(); err != nil {
		return err
	}
	if ack.Signature == "" {
		return fmt.Errorf("%w: acknowledgement is unsigned", ErrIncomplete)
	}

	// A ticket statement without a ticket, or a hold statement carrying one, is
	// the shape a re-used record takes.
	if ack.Kind.concernsAWriterTicket() {
		if err := ack.WriterTicketID.Validate(); err != nil {
			return fmt.Errorf("a %s acknowledgement names no writer ticket: %w", ack.Kind, err)
		}
		if ack.WriterFence == 0 {
			return fmt.Errorf("%w: a %s acknowledgement carries no writer fence; ticket issuance "+
				"and sealing serialize on it", ErrIncomplete, ack.Kind)
		}

		return nil
	}
	if ack.WriterTicketID != "" {
		return fmt.Errorf("%w: a %s acknowledgement names writer ticket %q",
			ErrIncomplete, ack.Kind, ack.WriterTicketID)
	}
	if ack.Kind == CaptureHoldAcknowledged && ack.WriterFence != 0 {
		return fmt.Errorf("%w: the pre-start hold carries writer fence %d; the hold authorizes "+
			"no writing at all", ErrIncomplete, ack.WriterFence)
	}

	return nil
}

// ValidateAs is Validate plus the rule a boolean beside an acknowledgement
// cannot express: this statement must be of the kind that attests the state it
// is being offered for.
//
// Sealing is why it exists. `seal_started` says admission is fenced and
// `seal_confirmed` says every captured writer drained; those are two states,
// owned by two actors, and a value that carried an acknowledgement of any kind
// next to a `Confirmed: true` field would let a pre-start hold back a confirmed
// seal. Binding the kind to the state is the structural form of Req 15's "both
// halves hold".
func (ack CaptureAcknowledgement) ValidateAs(kind CaptureAcknowledgementKind) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if err := ack.Validate(); err != nil {
		return err
	}
	if ack.Kind != kind {
		return fmt.Errorf("%w: a %s statement was offered as proof of %s; only a %s statement "+
			"attests that", ErrIncomplete, ack.Kind, kind.stateProved(), kind)
	}

	return nil
}
