package executioncontrol

import (
	"context"
	"fmt"
)

// ExitOutcome is the real outcome of the real process. It is recorded from the
// supervisor's own wait, never inferred from a Pod phase or a container status.
type ExitOutcome struct {
	ExitCode  int    `json:"exit_code"`
	Signalled bool   `json:"signalled"`
	Signal    string `json:"signal"`
}

func (outcome ExitOutcome) Validate() error {
	if outcome.Signalled && outcome.Signal == "" {
		return fmt.Errorf("%w: a signalled outcome names no signal", ErrIncomplete)
	}
	if !outcome.Signalled && outcome.Signal != "" {
		return fmt.Errorf("%w: an unsignalled outcome names signal %q", ErrIncomplete, outcome.Signal)
	}

	return nil
}

// Successful is the one predicate that may gate a durable capture. Requirement
// 2 admits capture only after the producer's main command exits successfully,
// and "successfully" means exactly this: exit code zero, not signalled.
func (outcome ExitOutcome) Successful() bool {
	return outcome.ExitCode == 0 && !outcome.Signalled
}

// Acknowledgement is an immutable, signed statement from the node's control
// ledger. It is the only thing in this protocol that constitutes proof.
//
// A base acknowledgement carries no capture identity, source lease, handle
// generation or output name. Those belong to the optional extension's own
// acknowledgement type in hangar/output, which references this Identity rather
// than restating it.
type Acknowledgement struct {
	ProtocolVersion string              `json:"protocol_version"`
	Kind            AcknowledgementKind `json:"kind"`
	Identity
	ActivationEpoch ActivationEpoch `json:"activation_epoch"`
	LedgerSequence  LedgerSequence  `json:"ledger_sequence"`
	NodeUID         NodeUID         `json:"node_uid"`
	PodUID          PodUID          `json:"pod_uid"`
	ProcessIdentity ProcessIdentity `json:"process_identity"`
	ObservedAt      Timestamp       `json:"observed_at"`
	Outcome         *ExitOutcome    `json:"outcome,omitempty"`
	Signature       string          `json:"signature"`
}

func (ack Acknowledgement) Validate() error {
	if err := validateProtocol(ack.ProtocolVersion); err != nil {
		return err
	}
	if err := ack.Kind.Validate(); err != nil {
		return err
	}
	if err := ack.Identity.Validate(); err != nil {
		return err
	}
	if ack.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if ack.LedgerSequence == 0 {
		return fmt.Errorf("%w: ledger sequence is zero", ErrIncomplete)
	}
	if ack.NodeUID == "" {
		return fmt.Errorf("%w: node uid is empty", ErrIncomplete)
	}
	if ack.ProcessIdentity == "" {
		return fmt.Errorf("%w: process identity is empty", ErrIncomplete)
	}
	if err := ack.ObservedAt.Validate(); err != nil {
		return err
	}
	if ack.Signature == "" {
		return fmt.Errorf("%w: acknowledgement is unsigned", ErrIncomplete)
	}

	// A start acknowledgement with an outcome, or a finish without one, is the
	// shape a re-used record takes.
	switch ack.Kind {
	case AcknowledgementStart:
		if ack.Outcome != nil {
			return fmt.Errorf("%w: a start acknowledgement carries an exit outcome", ErrIncomplete)
		}
	case AcknowledgementFinish, AcknowledgementStop:
		if ack.Outcome == nil {
			return fmt.Errorf("%w: a %s acknowledgement carries no exit outcome", ErrIncomplete, ack.Kind)
		}
		if err := ack.Outcome.Validate(); err != nil {
			return err
		}
	}

	return nil
}

// ClassifyRequest asks what is durably known about an exact identity.
type ClassifyRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
}

func (request ClassifyRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}

	return request.Identity.Validate()
}

// ClassifyResult is the answer. An authoritative classification must carry the
// acknowledgement that makes it authoritative; a non-authoritative one must
// not, because an acknowledgement attached to `unresolved` is how an inference
// starts looking like proof.
type ClassifyResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	Classification  Classification   `json:"classification"`
	Acknowledgement *Acknowledgement `json:"acknowledgement,omitempty"`
}

func (result ClassifyResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Identity.Validate(); err != nil {
		return err
	}

	return validateEvidence(result.Classification, result.Acknowledgement, result.Identity)
}

// validateEvidence is the rule shared by every result that carries a
// classification and an optional acknowledgement.
func validateEvidence(classification Classification, ack *Acknowledgement, identity Identity) error {
	if err := classification.Validate(); err != nil {
		return err
	}
	if classification.Authoritative() {
		if ack == nil {
			return fmt.Errorf("%w: classification %s carries no acknowledgement; only a durable "+
				"acknowledgement makes an outcome authoritative", ErrIncomplete, classification)
		}
		if err := ack.Validate(); err != nil {
			return err
		}
		if ack.Kind.Classification() != classification {
			return fmt.Errorf("%w: a %s acknowledgement cannot establish %s",
				ErrIncomplete, ack.Kind, classification)
		}
		if ack.Identity != identity {
			return fmt.Errorf("%w: the acknowledgement is for a different exact execution",
				ErrInvalidIdentity)
		}

		return nil
	}
	if ack != nil {
		return fmt.Errorf("%w: classification %s carries an acknowledgement; evidence attached to "+
			"a non-authoritative answer is how an inference starts looking like proof",
			ErrIncomplete, classification)
	}

	return nil
}

// ObserveFinishOrStopRequest waits, bounded, for a durable outcome.
type ObserveFinishOrStopRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	// WaitMilliseconds bounds the wait. Zero polls once. The bound is on the
	// wire because the caller, not the node, owns its own deadline.
	WaitMilliseconds int64 `json:"wait_milliseconds"`
}

func (request ObserveFinishOrStopRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.WaitMilliseconds < 0 {
		return fmt.Errorf("%w: negative wait", ErrIncomplete)
	}

	return nil
}

// ObserveFinishOrStopResult returns a durable acknowledgement or says it does
// not have one. It never returns a Pod phase, a build row or an in-memory
// result dressed as an outcome.
type ObserveFinishOrStopResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	Classification  Classification   `json:"classification"`
	Acknowledgement *Acknowledgement `json:"acknowledgement,omitempty"`
}

func (result ObserveFinishOrStopResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Identity.Validate(); err != nil {
		return err
	}

	return validateEvidence(result.Classification, result.Acknowledgement, result.Identity)
}

// RequestSourcePreservingStopRequest interrupts the exact command.
//
// The name is the contract: it stops the process and it destroys nothing. No
// Pod is deleted, no artifact path is removed, no optional hold is released.
// The caller's reason for stopping is not on the wire, because the base
// protocol has no reasons -- a cancellation, a deadline and a drain are the
// same operation here.
type RequestSourcePreservingStopRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	Capability ControlCapability `json:"capability"`
}

func (request RequestSourcePreservingStopRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.Capability == "" {
		return fmt.Errorf("%w: no control capability offered", ErrIncomplete)
	}

	return nil
}

// RequestSourcePreservingStopResult reports whether the stop was admitted.
// Accepted is not an outcome: the acknowledgement arrives through
// ObserveFinishOrStop like any other.
type RequestSourcePreservingStopResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	Accepted       bool           `json:"accepted"`
	Classification Classification `json:"classification"`
}

func (result RequestSourcePreservingStopResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Identity.Validate(); err != nil {
		return err
	}
	if err := result.Classification.Validate(); err != nil {
		return err
	}
	if result.Accepted && result.Classification.Terminal() {
		return fmt.Errorf("%w: a stop was admitted for an execution already %s",
			ErrIncomplete, result.Classification)
	}

	return nil
}

// DestructiveCleanupEligibleRequest asks whether an execution's remains may be
// deleted.
type DestructiveCleanupEligibleRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
}

func (request DestructiveCleanupEligibleRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}

	return request.Identity.Validate()
}

// DestructiveCleanupEligibleResult is the answer, and it fails closed.
//
// Eligibility requires an authoritative finish or stop *and* every optional
// extension gate closed. OpenExtensionGates names the ones that are not, by
// opaque gate name -- so the base protocol can report that a capture hold is
// still open without learning what a capture is.
type DestructiveCleanupEligibleResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Identity
	Eligible           bool           `json:"eligible"`
	Classification     Classification `json:"classification"`
	OpenExtensionGates []string       `json:"open_extension_gates"`
	WithheldReason     string         `json:"withheld_reason"`
}

func (result DestructiveCleanupEligibleResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Identity.Validate(); err != nil {
		return err
	}
	if err := result.Classification.Validate(); err != nil {
		return err
	}
	if result.Eligible {
		if !result.Classification.Authoritative() {
			return fmt.Errorf("%w: cleanup declared eligible on classification %s; only a durable "+
				"finish or stop may authorize destroying anything", ErrIncomplete, result.Classification)
		}
		if len(result.OpenExtensionGates) != 0 {
			return fmt.Errorf("%w: cleanup declared eligible with gates still open: %v",
				ErrIncomplete, result.OpenExtensionGates)
		}
		if result.WithheldReason != "" {
			return fmt.Errorf("%w: cleanup declared eligible and withheld at once (%q)",
				ErrIncomplete, result.WithheldReason)
		}

		return nil
	}
	if result.WithheldReason == "" {
		return fmt.Errorf("%w: cleanup withheld with no reason; a refusal nobody can read is a "+
			"stuck execution nobody can diagnose", ErrIncomplete)
	}

	return nil
}

// Handshake is what an authenticated daemon returns when a control plane asks
// what it speaks. Node labels are hints; this is the authority.
//
// It carries the base facet only. The capture extension's handshake, in
// hangar/output, embeds this one and adds its own facts, so a base-only cohort
// is attestable without an output bucket existing at all.
type Handshake struct {
	ProtocolVersion string          `json:"protocol_version"`
	LedgerVersion   string          `json:"ledger_version"`
	ControlKeyID    string          `json:"control_key_id"`
	ActivationEpoch ActivationEpoch `json:"activation_epoch"`
}

func (handshake Handshake) Validate() error {
	if err := validateProtocol(handshake.ProtocolVersion); err != nil {
		return err
	}
	if handshake.LedgerVersion == "" {
		return fmt.Errorf("%w: no ledger version reported", ErrIncomplete)
	}
	if handshake.ControlKeyID == "" {
		return fmt.Errorf("%w: no control key id reported", ErrIncomplete)
	}
	if handshake.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}

	return nil
}

// Client is the whole protocol. Four operations, closed.
//
// Nothing here accepts an output name, a source path, a bucket, a consumer
// lifecycle or any product-domain value, and hangar/output's architecture guard
// fails the build if one appears. An implementation that needed a fifth
// operation would be describing a different protocol.
type Client interface {
	// Classify reports what is durably known. It is safe to call at any time
	// and it never changes anything.
	Classify(ctx context.Context, request ClassifyRequest) (ClassifyResult, error)

	// ObserveFinishOrStop returns a durable acknowledgement, or reports that
	// there is not one yet.
	ObserveFinishOrStop(ctx context.Context, request ObserveFinishOrStopRequest) (ObserveFinishOrStopResult, error)

	// RequestSourcePreservingStop interrupts the command and destroys nothing.
	RequestSourcePreservingStop(ctx context.Context, request RequestSourcePreservingStopRequest) (RequestSourcePreservingStopResult, error)

	// DestructiveCleanupEligible fails closed: unknown means no.
	DestructiveCleanupEligible(ctx context.Context, request DestructiveCleanupEligibleRequest) (DestructiveCleanupEligibleResult, error)
}
