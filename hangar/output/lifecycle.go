package output

import (
	"encoding/json"
	"fmt"
	"time"

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
// tree-ref lifecycle record so a stale release cannot silently reactivate one.
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

// ClaimRecord is one claim as the plane records it -- active or tombstoned.
//
// The tombstone is a field rather than an absence, and that is the whole point:
// a released identity stays for the lifetime of the tree-ref lifecycle record,
// so "no claim is left behind" and "the tombstone is permanent" are different
// questions about the same row and a reader that could not see a released claim
// could not tell them apart.
type ClaimRecord struct {
	ClaimID           ClaimID        `json:"claim_id"`
	Ref               hangar.TreeRef `json:"ref"`
	ConsumerBindingID OpaqueID       `json:"consumer_binding_id"`
	AcquiredAt        Timestamp      `json:"acquired_at"`

	// ReleasedAt is nil while the claim is active. It is a pointer rather than
	// a zero time because "not released" is the absence of an instant, and a
	// zero time is an instant.
	ReleasedAt *Timestamp `json:"released_at,omitempty"`
}

// Active reports whether this claim still protects its generation.
func (record ClaimRecord) Active() bool { return record.ReleasedAt == nil }

func (record ClaimRecord) Validate() error {
	if err := record.ClaimID.Validate(); err != nil {
		return err
	}
	if err := record.Ref.Validate(); err != nil {
		return err
	}
	if err := record.ConsumerBindingID.Validate(); err != nil {
		return err
	}
	if err := record.AcquiredAt.Validate(); err != nil {
		return err
	}
	if record.ReleasedAt != nil {
		if err := record.ReleasedAt.Validate(); err != nil {
			return err
		}
		if record.ReleasedAt.Before(record.AcquiredAt.Time) {
			return fmt.Errorf("%w: claim %s was released before it was acquired", ErrIncomplete,
				record.ClaimID)
		}
	}

	return nil
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
// and policy revalidation. Minting the warrant that carries it is deliberately
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

// ReadLeaseRecord is a committed lease plus everything its warrant binds.
//
// It exists because minting is deliberately not atomic with the transaction
// that created the lease: after the commit, the minter loads the exact facts
// back rather than signing the ones it thought it wrote. An ambiguous commit is
// then answered by identity -- load by lease id and fence, mint only if what
// came back matches -- instead of by hoping.
type ReadLeaseRecord struct {
	Lease        ReadLease
	Destination  ReadDestination
	WarrantNonce string
}

func (record ReadLeaseRecord) Validate() error {
	if err := record.Lease.Validate(); err != nil {
		return err
	}
	if err := record.Destination.Validate(); err != nil {
		return err
	}

	return validateReadWarrantNonce(record.WarrantNonce)
}

// ReadLeaseValidation is the question the materializing daemon asks the control
// plane before it opens a single object.
//
// It repeats every field the warrant carried, and the control plane compares each
// one against the committed row rather than against the token. That is the
// whole point of asking: a valid HMAC bound to a lease that is missing,
// released, expired, superseded or reclaim-conflicted authorizes nothing, and
// only the database knows which of those is true.
//
// RequiredRemaining is the work the caller is about to start. Requirement 36
// lets work begin only with the operation's timeout plus two minutes left, and
// putting that here rather than in the daemon means the DATABASE clock decides
// it -- a node whose clock drifts cannot talk itself into starting.
type ReadLeaseValidation struct {
	ReadLeaseID       ReadLeaseID
	ClaimID           ClaimID
	Ref               hangar.TreeRef
	Destination       ReadDestination
	ActivationEpoch   executioncontrol.ActivationEpoch
	WarrantNonce      string
	RequiredRemaining time.Duration
}

func (validation ReadLeaseValidation) Validate() error {
	if err := validation.ReadLeaseID.Validate(); err != nil {
		return err
	}
	if err := validation.ClaimID.Validate(); err != nil {
		return err
	}
	if err := validation.Ref.Validate(); err != nil {
		return err
	}
	if err := validation.Destination.Validate(); err != nil {
		return err
	}
	if validation.ActivationEpoch == 0 {
		return fmt.Errorf("%w: a lease validation names no activation epoch", ErrIncomplete)
	}
	if err := validateReadWarrantNonce(validation.WarrantNonce); err != nil {
		return err
	}
	if validation.RequiredRemaining < 0 {
		return fmt.Errorf("%w: required remaining term is negative", ErrIncomplete)
	}

	return nil
}

// ReadWarrantFor is the validation a warrant's own claims imply.
//
// It exists so the daemon cannot compose a different question from the one the
// token answered: every field comes from the verified claims, and the only
// thing the caller adds is how much work it is about to start.
func ReadWarrantFor(claims ReadWarrantClaims, remaining time.Duration) ReadLeaseValidation {
	return ReadLeaseValidation{
		ReadLeaseID:       claims.ReadLeaseID,
		ClaimID:           claims.ClaimID,
		Ref:               claims.Ref,
		Destination:       claims.Destination,
		ActivationEpoch:   claims.ActivationEpoch,
		WarrantNonce:      claims.Nonce,
		RequiredRemaining: remaining,
	}
}

// DeletePrecondition is the exact generation a conditional delete must match,
// plus the metageneration the object was registered at.
//
// It is a required parameter of the one delete route rather than an option on
// it. GCS IAM cannot require a caller to send a generation precondition once
// delete permission exists (Req 55), so the only place that requirement can
// live is the signature -- and architecture_test.go fails the test suite if a delete
// appears anywhere without one.
//
// Metageneration is RECORDED EVIDENCE about the object at registration and is
// deliberately not sent as a delete precondition, which it used to be. A real
// object's generation is stable while its metageneration moves on any metadata
// change -- a SetStorageClass lifecycle transition, Autoclass, an ACL or
// metadata edit, a retention hold -- and none of those is a Delete rule, so the
// bucket keeps attesting safe. Conditioned on the metageneration recorded at
// receipt registration, every delete in such a bucket 412s;
// DeleteGenerationConflict is TERMINAL and there is no re-stat-and-re-register
// path anywhere, so the whole registered set became permanently unreclaimable,
// silently, forever. Req 47 asks for generation-exact deletion, not
// metadata-exact, and .Generation(g) plus ifGenerationMatch is already exact.
//
// The alternative was to keep the conjunct and add a re-stat arm that refreshes
// the registered metageneration and re-admits under the same lease. That is a
// new state machine on the delete path to buy a property the generation pin
// already has, so it is not what this does.
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
