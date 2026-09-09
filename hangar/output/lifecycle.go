package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// ClaimAcquisition asks Hangar to protect one exact generation.
//
// A claim protects an immutable (scope, digest, generation). It cannot float to
// replacement content or a mutable alias, which is why every operation here
// takes a whole hangar.TreeRef and never a scope-and-digest pair.
//
// ConsumerBindingID is opaque and Hangar never interprets it. It exists so a
// consumer can find its own binding again after a crash; the moment Hangar
// could read it, Hangar would know what a Run is.
type ClaimAcquisition struct {
	ProtocolVersion   string         `json:"protocol_version"`
	ClaimID           ClaimID        `json:"claim_id"`
	Ref               hangar.TreeRef `json:"ref"`
	ConsumerBindingID OpaqueID       `json:"consumer_binding_id"`
	RequestedAt       Timestamp      `json:"requested_at"`
}

func (acquisition ClaimAcquisition) Validate() error {
	if err := validateProtocol(acquisition.ProtocolVersion); err != nil {
		return err
	}
	if err := acquisition.ClaimID.Validate(); err != nil {
		return err
	}
	if err := acquisition.Ref.Validate(); err != nil {
		return err
	}
	if err := acquisition.ConsumerBindingID.Validate(); err != nil {
		return err
	}

	return acquisition.RequestedAt.Validate()
}

// ClaimRelease gives up one claim.
//
// It names no consumer binding, because releasing is the consumer making its
// own binding unusable in the same transaction; there is nothing left for
// Hangar to hold onto. Releasing an active or already-released claim is
// idempotent, and released identities stay tombstoned for the lifetime of the
// exact-ref lifecycle record so a stale release cannot silently reactivate one.
type ClaimRelease struct {
	ProtocolVersion string         `json:"protocol_version"`
	ClaimID         ClaimID        `json:"claim_id"`
	Ref             hangar.TreeRef `json:"ref"`
	RequestedAt     Timestamp      `json:"requested_at"`
}

func (release ClaimRelease) Validate() error {
	if err := validateProtocol(release.ProtocolVersion); err != nil {
		return err
	}
	if err := release.ClaimID.Validate(); err != nil {
		return err
	}
	if err := release.Ref.Validate(); err != nil {
		return err
	}

	return release.RequestedAt.Validate()
}

// ReadLease is the fenced, renewable right to read one exact generation while a
// materialization is in flight.
//
// It exists because a claim is the consumer's protection and a read is the
// daemon's, and the two have different lifetimes: releasing the last claim
// during a transfer must not delete the bytes out from under a reader.
// Reclaim admission is refused while any read lease is active, even after the
// last claim is gone.
//
// The lease is created inside the caller's transaction, together with the claim
// and policy revalidation. Minting the grant that carries it is deliberately
// *not* atomic with that transaction: signing is not a database operation, and
// pretending otherwise would be the atomic-commit claim this design avoids
// everywhere else.
type ReadLease struct {
	ProtocolVersion string                           `json:"protocol_version"`
	ReadLeaseID     ReadLeaseID                      `json:"read_lease_id"`
	ClaimID         ClaimID                          `json:"claim_id"`
	Ref             hangar.TreeRef                   `json:"ref"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	LeaseFence      LeaseFence                       `json:"lease_fence"`
	GrantedAt       Timestamp                        `json:"granted_at"`
	ExpiresAt       Timestamp                        `json:"expires_at"`
}

func (lease ReadLease) Validate() error {
	if err := validateProtocol(lease.ProtocolVersion); err != nil {
		return err
	}
	if err := lease.ReadLeaseID.Validate(); err != nil {
		return err
	}
	if err := lease.ClaimID.Validate(); err != nil {
		return err
	}
	if err := lease.Ref.Validate(); err != nil {
		return err
	}
	if lease.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if lease.LeaseFence == 0 {
		return fmt.Errorf("%w: lease fence is zero", ErrIncomplete)
	}
	if err := lease.GrantedAt.Validate(); err != nil {
		return err
	}
	if err := lease.ExpiresAt.Validate(); err != nil {
		return err
	}
	if !lease.ExpiresAt.After(lease.GrantedAt.Time) {
		return fmt.Errorf("%w: read lease expires at or before it was granted", ErrIncomplete)
	}
	if term := lease.ExpiresAt.Sub(lease.GrantedAt.Time); term < MinLeaseTerm {
		return fmt.Errorf("%w: read lease term %s is shorter than the %s floor",
			ErrIncomplete, term, MinLeaseTerm)
	}

	return nil
}

// DeletePrecondition is the exact generation and metageneration a conditional
// delete must match.
//
// It is a required parameter of the one delete route rather than an option on
// it. GCS IAM cannot require a caller to send a generation precondition once
// delete permission exists (Req 55), so the only place that requirement can
// live is the signature -- and architecture_test.go fails the test suite if a delete
// appears anywhere without one.
type DeletePrecondition struct {
	Generation     int64 `json:"generation"`
	Metageneration int64 `json:"metageneration"`
}

func (precondition DeletePrecondition) Validate() error {
	if precondition.Generation <= 0 {
		return fmt.Errorf("%w: delete precondition names no generation; an unconditional delete "+
			"may never broaden from a conditional one", ErrIncomplete)
	}
	if precondition.Metageneration <= 0 {
		return fmt.Errorf("%w: delete precondition names no metageneration", ErrIncomplete)
	}

	return nil
}

// DeleteOutcome is the closed set of results a conditional delete may report.
//
// The foundation's DeleteTree collapses exact absence into ordinary success.
// This does not, and that difference is the whole reason the type exists:
// absence without a prior admitted delete is an out-of-band lifetime violation,
// not normal reclamation, and a store that cannot tell them apart cannot make
// a lifetime promise at all.
type DeleteOutcome string

const (
	DeleteConfirmed          DeleteOutcome = "deleted"
	DeleteAlreadyAbsent      DeleteOutcome = "already_absent"
	DeleteGenerationConflict DeleteOutcome = "generation_conflict"
	DeleteUnauthorized       DeleteOutcome = "unauthorized"
	DeleteTimedOut           DeleteOutcome = "timeout"
	DeleteInfrastructure     DeleteOutcome = "infrastructure_failure"
)

func DeleteOutcomes() []DeleteOutcome {
	return []DeleteOutcome{
		DeleteConfirmed,
		DeleteAlreadyAbsent,
		DeleteGenerationConflict,
		DeleteUnauthorized,
		DeleteTimedOut,
		DeleteInfrastructure,
	}
}

func ParseDeleteOutcome(value string) (DeleteOutcome, error) {
	for _, member := range DeleteOutcomes() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: delete outcome %q; the vocabulary is %v",
		ErrUnknownMember, value, DeleteOutcomes())
}

func (outcome *DeleteOutcome) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseDeleteOutcome(text)
	if err != nil {
		return err
	}
	*outcome = parsed

	return nil
}

func (outcome DeleteOutcome) Validate() error {
	_, err := ParseDeleteOutcome(string(outcome))

	return err
}
