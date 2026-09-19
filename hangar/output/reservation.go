package output

// The reservation: the one location a capture will hold, issued before the
// producing Pod exists.
//
// Phase 4 found the seam this type closes. A capture-selected producer used to
// write into `steps/<handle>/<output>` -- the directory its Pod mounts -- while
// the daemon's hold protected `steps/<executionID>.<handleGeneration>/<output>`,
// the incarnation `AcknowledgeHold` created. They are SIBLINGS, so every
// path-keyed guard correctly answered `unmanaged` for the bytes the producer
// actually wrote, and the hold protected an empty directory.
//
// The fix is not to let the ATC name the incarnation: Req 7 forbids any API
// accepting a caller-chosen path, and a handle generation the control plane
// picked would be the reuse confusion SourceIncarnation exists to prevent. It
// is to move the ISSUANCE earlier. The daemon mints the incarnation at
// reservation time, before the Pod is built; the ATC repeats the answer into
// the Pod's volume; the control init's hold binds to that same reservation and
// is refused for any other; and Phase 5 seals the bytes the producer wrote.
//
// The request body is CaptureAdmission unchanged. A reservation and a hold are
// admitted for exactly the same facts -- the exact execution and its fence, the
// handoff, the source hold, the declared output and the epoch -- and a second
// request type carrying the same seven fields would be two spellings of one
// contract, which is how the two of them come to disagree.

import (
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// Directory is the ONE derivation of a location from an incarnation, and it
// lives here rather than in the daemon because two readers need it: the daemon,
// which creates and resolves the directory, and the read-only classifier, which
// must key on the identical string or its refusals miss.
//
// It is a RELATIVE path under the managed steps root, and it never escapes:
// every component comes from a validated identity, the handle generation is in
// the name so a reused execution id at a new generation is a different
// directory, and the daemon resolves it through an os.Root handle that refuses
// a climb regardless.
//
// The ATC must not call this. It repeats ReservedIncarnation.Directory, which
// is the daemon's own answer on the wire, and
// TestNoATCCodeComposesAnIncarnationName fails if any file under atc/ composes
// one instead.
func (incarnation SourceIncarnation) Directory() string {
	return fmt.Sprintf("%s.%d/%s",
		incarnation.ExecutionID, incarnation.HandleGeneration, incarnation.Output)
}

// ReservedIncarnation is the daemon's answer: the location this capture will
// hold, and the daemon's own name for it.
//
// It is deliberately NOT a CaptureAcknowledgement. An acknowledgement is a
// signed statement about a state that has been reached; a reservation is a
// location that has been set aside, it authorizes no writing, and it proves
// nothing about the execution beyond the admission the daemon already checked.
// Making it a sixth acknowledgement kind would put a statement in the ledger's
// signed vocabulary that no receipt ever needs to verify.
//
// Directory is on the wire for one reason: so the ATC never composes one. It is
// redundant with Incarnation by construction -- Validate refuses a pair that
// disagrees -- and the redundancy is the point, because the alternative is a
// control plane that knows how to spell a source path.
type ReservedIncarnation struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	SourceHoldID    SourceHoldID                     `json:"source_hold_id"`
	NodeUID         executioncontrol.NodeUID         `json:"node_uid"`
	Incarnation     SourceIncarnation                `json:"incarnation"`
	Directory       string                           `json:"directory"`
	LedgerSequence  executioncontrol.LedgerSequence  `json:"ledger_sequence"`
	ObservedAt      Timestamp                        `json:"observed_at"`
}

func (reserved ReservedIncarnation) Validate() error {
	if err := validateProtocol(reserved.ProtocolVersion); err != nil {
		return err
	}
	if err := reserved.Execution.Validate(); err != nil {
		return err
	}
	if reserved.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero; a reservation records the epoch it was "+
			"issued under or it is not a reservation", ErrIncomplete)
	}
	if err := reserved.HandoffID.Validate(); err != nil {
		return err
	}
	if err := reserved.SourceHoldID.Validate(); err != nil {
		return err
	}
	if reserved.NodeUID == "" {
		return fmt.Errorf("%w: the reservation names no node", ErrIncomplete)
	}
	if reserved.LedgerSequence == 0 {
		return fmt.Errorf("%w: ledger sequence is zero", ErrIncomplete)
	}
	if err := reserved.Incarnation.Validate(); err != nil {
		return err
	}
	if reserved.Incarnation.ExecutionID != reserved.Execution.ExecutionID {
		return fmt.Errorf("%w: the reserved incarnation belongs to execution %s and the "+
			"reservation is for %s", ErrInvalidIdentity,
			reserved.Incarnation.ExecutionID, reserved.Execution.ExecutionID)
	}
	if reserved.Incarnation.NodeUID != reserved.NodeUID {
		return fmt.Errorf("%w: the reserved incarnation belongs to node %s and the reservation "+
			"was issued by %s", ErrInvalidIdentity, reserved.Incarnation.NodeUID, reserved.NodeUID)
	}
	// The redundancy check. A Directory that does not derive from the
	// Incarnation beside it is a caller-chosen path wearing a server-issued
	// identity, which is the one thing Req 7 forbids outright.
	if reserved.Directory != reserved.Incarnation.Directory() {
		return fmt.Errorf("%w: the reservation names directory %q and the incarnation it carries "+
			"derives %q; a directory that does not derive from its incarnation is a chosen path",
			ErrInvalidIdentity, reserved.Directory, reserved.Incarnation.Directory())
	}

	return reserved.ObservedAt.Validate()
}
