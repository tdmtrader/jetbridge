package output

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
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
//
// A claim is the one refcount on a generation. A consumer's hold -- a Run's
// result binding, an input publication, a capture's own -- has no term and
// lasts until released. A reader's hold has a Term: it expires on the
// database clock, so an abandoned read pins nothing for the life of the
// deployment. Zero is a consumer's hold.
type ClaimAcquisition struct {
	ProtocolVersion   string         `json:"protocol_version"`
	ClaimID           ClaimID        `json:"claim_id"`
	Ref               hangar.TreeRef `json:"ref"`
	ConsumerBindingID OpaqueID       `json:"consumer_binding_id"`
	RequestedAt       Timestamp      `json:"requested_at"`
	Term              time.Duration  `json:"term,omitempty"`
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
	if acquisition.Term < 0 {
		return fmt.Errorf("%w: a claim's term is never negative", ErrIncomplete)
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
//
// A reader's claim carries ExpiresAt; a consumer's does not. Whether an
// expiring claim still holds is the database clock's question, asked where it
// matters (reclaim's live-claim check), never a node's.
type ClaimRecord struct {
	ClaimID           ClaimID        `json:"claim_id"`
	Ref               hangar.TreeRef `json:"ref"`
	ConsumerBindingID OpaqueID       `json:"consumer_binding_id"`
	AcquiredAt        Timestamp      `json:"acquired_at"`

	// ExpiresAt is nil for a consumer's hold, and the instant a reader's hold
	// lapses on the database clock.
	ExpiresAt *Timestamp `json:"expires_at,omitempty"`

	// ReleasedAt is nil while the claim is active. It is a pointer rather than
	// a zero time because "not released" is the absence of an instant, and a
	// zero time is an instant.
	ReleasedAt *Timestamp `json:"released_at,omitempty"`
}

// Active reports whether this claim was given back. A reader's claim that
// was not given back also lapses at ExpiresAt; the database judges that.
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
	if record.ExpiresAt != nil {
		if err := record.ExpiresAt.Validate(); err != nil {
			return err
		}
		if !record.ExpiresAt.After(record.AcquiredAt.Time) {
			return fmt.Errorf("%w: claim %s expires at or before it was acquired", ErrIncomplete,
				record.ClaimID)
		}
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

// DeletePrecondition is the exact generation a conditional delete must match.
//
// A delete is conditioned on the exact generation and is never unconditional;
// hangar/output/architecture_test.go fails on a delete route without one.
type DeletePrecondition struct {
	Generation int64 `json:"generation"`
}

func (precondition DeletePrecondition) Validate() error {
	if precondition.Generation <= 0 {
		return fmt.Errorf("%w: delete precondition names no generation; an unconditional delete "+
			"may never broaden from a conditional one", ErrIncomplete)
	}

	return nil
}
