package output

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// LogicalResolution binds the server-derived scope and digest to a Stage 2
// reservation.
//
// It exists at exactly one moment: after canonicalization has produced the
// bytes' logical identity, and before the first object create. That ordering is
// the whole point — a possibly-created object must have a pre-existing
// reservation that recovery and inventory can correlate, and the correlation
// handle must be one Hangar derived rather than a key some task supplied.
//
// Scope and Digest are fields the control plane fills in from the canonical
// bytes, never parameters an API accepts. ReservationID names the Stage 2
// reservation whose owner and fence must still be current; a stale owner
// resolves nothing.
type LogicalResolution struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	ReservationID   ReservationID                    `json:"reservation_id"`
	CaptureFence    CaptureFence                     `json:"capture_fence"`
	Scope           hangar.Scope                     `json:"scope"`
	Digest          hangar.Digest                    `json:"digest"`
	LogicalBytes    int64                            `json:"logical_bytes"`
	ResolvedAt      Timestamp                        `json:"resolved_at"`
}

func (resolution LogicalResolution) Validate() error {
	if err := validateProtocol(resolution.ProtocolVersion); err != nil {
		return err
	}
	if err := resolution.Execution.Validate(); err != nil {
		return err
	}
	if resolution.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if err := resolution.HandoffID.Validate(); err != nil {
		return err
	}
	if err := resolution.ReservationID.Validate(); err != nil {
		return err
	}
	if resolution.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero; a stale owner may not resolve a logical "+
			"reservation", ErrIncomplete)
	}
	if err := resolution.Scope.Validate(); err != nil {
		return err
	}
	if err := resolution.Digest.Validate(); err != nil {
		return err
	}
	if resolution.LogicalBytes < 0 {
		return fmt.Errorf("%w: logical size is negative", ErrCorrupt)
	}

	return resolution.ResolvedAt.Validate()
}

// ReceiptAdmission is a verified receipt being admitted into durable lifecycle
// state.
//
// ChallengeNonce is the one-use, database-clock-bounded challenge the daemon
// signed over. It is the nonce rather than the whole StatChallenge because the
// challenge is a *row*: the caller's transaction consumes it and revalidates
// every fact it bound, and a copy travelling beside the receipt would be a
// second, unauthoritative statement of facts the receipt already carries.
//
// It must be the nonce the receipt itself names. The row it consumes and the
// signature it presents have to be about the same challenge, or the one-use
// rule is spending one capture's nonce on another capture's evidence.
//
// Metageneration is what the exact-generation stat observed. The generation
// itself is in the receipt's signed claims; the metageneration is not signed,
// because it changes when metadata does and a signature over it would expire
// for a reason that has nothing to do with the bytes.
type ReceiptAdmission struct {
	ProtocolVersion string    `json:"protocol_version"`
	Receipt         Receipt   `json:"receipt"`
	ChallengeNonce  string    `json:"challenge_nonce"`
	Metageneration  int64     `json:"metageneration"`
	AdmittedAt      Timestamp `json:"admitted_at"`
}

func (admission ReceiptAdmission) Validate() error {
	if err := validateProtocol(admission.ProtocolVersion); err != nil {
		return err
	}
	if err := admission.Receipt.Validate(); err != nil {
		return err
	}
	if admission.ChallengeNonce == "" {
		return fmt.Errorf("%w: receipt admission carries no challenge nonce; a signature over "+
			"old facts proves only that the facts were once true", ErrIncomplete)
	}
	if admission.ChallengeNonce != admission.Receipt.Claims.ChallengeNonce {
		return fmt.Errorf("%w: the admission consumes challenge %s and the receipt answers %s. "+
			"Consuming a nonce the receipt never named would let a fresh observation of one "+
			"challenge settle a different one", ErrConflict,
			admission.ChallengeNonce, admission.Receipt.Claims.ChallengeNonce)
	}
	if admission.Metageneration <= 0 {
		return fmt.Errorf("%w: receipt admission observed no metageneration", ErrIncomplete)
	}

	return admission.AdmittedAt.Validate()
}

// CaptureRepository is the capture-side half of the caller-owned transaction.
//
// Every method takes the caller's Tx and nothing else that could commit: a
// consumer's binding write and the Hangar operation beside it either commit
// together or roll back together, which is only true if the transaction belongs
// to the caller. Hangar never begins, commits or rolls one back.
//
// The method set is deliberately disjoint by stage, so that no pre-start fact
// can be mistaken for capture authority. PredeclareHandoff records only
// caller-generated identities before a Pod may start and is never accepted by a
// sealing or publication API; CommitCaptureReservation is the only door a
// successful finish walks through; ResolveLogicalReservation cannot be reached
// from a predeclaration. The three disposition branches are permanently
// exclusive and each has its own recording method, so "which branch won" is a
// question about which method was called rather than about a field somebody set.
//
// The two-step release methods are two steps on purpose. A caller records its
// exact fenced release intent and a daemon acknowledges that exact intent;
// nothing here describes the pair as an atomic commit, because across two
// systems it is not one, and recovery repeats the same identity until
// committed-versus-not is known.
//
// PostgreSQL implements this in atc/db, where atc/db.Tx already exists. It is
// declared here so that the shape is frozen with the rest of the contract,
// before persistence work begins, rather than invented outside the leaf.
type CaptureRepository interface {
	// PredeclareHandoff records the pre-start, non-authorizing predeclaration:
	// caller-generated handoff and source-hold ids, the exact execution, the
	// declared output and the activation epoch. It carries no producer
	// checkpoint, success fact, capture lease, scope, digest, receipt, seal or
	// claim, and CaptureAdmission has nowhere to put one.
	PredeclareHandoff(ctx context.Context, tx Tx, admission CaptureAdmission) error

	// CommitCaptureReservation is Stage 2. Only an authoritative *successful*
	// finish witness may enter it. It records the opaque producer checkpoint,
	// wins the one-row disposition arbiter as capture, and creates a distinct
	// unresolved reservation -- whose id it returns, because that id is the
	// correlation handle every later step needs. Winning the arbiter is not
	// capture authority: it seals, publishes and binds nothing.
	CommitCaptureReservation(ctx context.Context, tx Tx, disposition SuccessfulFinishDisposition) (ReservationID, error)

	// ResolveLogicalReservation binds the server-derived scope and digest,
	// after canonicalization and before the first object create.
	ResolveLogicalReservation(ctx context.Context, tx Tx, resolution LogicalResolution) error

	// RecordNoCaptureIntent wins the arbiter as no_capture, for an ordinary
	// authoritative non-success or a typed unresolved or lost reconciliation,
	// and records the first half of an exact fenced release.
	RecordNoCaptureIntent(ctx context.Context, tx Tx, disposition NoCaptureDisposition) error

	// AcknowledgeNoCaptureRelease completes that second half. Until it exists
	// the source stays held and destructive cleanup is forbidden.
	AcknowledgeNoCaptureRelease(ctx context.Context, tx Tx, acknowledgement ReleaseAcknowledgement) error

	// RecordPreReservationCancelIntent wins the third exclusive branch, when
	// cancellation wins before Stage 2. With no acknowledged hold it closes
	// without a daemon call at all; with one it records its own exact fenced
	// release intent.
	RecordPreReservationCancelIntent(ctx context.Context, tx Tx, disposition PreReservationCancelDisposition) error

	// AcknowledgePreReservationCancelRelease completes only the held form of
	// that branch.
	AcknowledgePreReservationCancelRelease(ctx context.Context, tx Tx, acknowledgement ReleaseAcknowledgement) error

	// RegisterReceipt resolves logical and exact lifecycle state from a
	// verified receipt, consuming the stat challenge it was signed against.
	RegisterReceipt(ctx context.Context, tx Tx, admission ReceiptAdmission) error
}
