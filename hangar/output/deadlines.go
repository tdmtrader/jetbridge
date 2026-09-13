package output

import (
	"fmt"
	"time"
)

// The frozen defaults and their configurable ranges.
//
// They are constants rather than configuration defaults scattered across flag
// declarations because several of them constrain each other, and the
// constraints are the interesting part: publication grace must exceed the
// maximum capture deadline by an hour (Req 39), a lease term must exceed the
// operation it covers plus a margin (Reqs 36, 48), and work may only begin with
// enough of the lease left to finish (Reqs 36, 48). Startup validates them
// together, so a deployment cannot be configured into a state where a
// reclaimer's lease can expire mid-delete.
const (
	// DefaultCaptureDeadline is how long a capture may remain unresolved before
	// it must be terminally settled. It is measured on the database clock.
	DefaultCaptureDeadline = 24 * time.Hour
	MinCaptureDeadline     = time.Hour
	MaxCaptureDeadline     = 7 * 24 * time.Hour

	// DefaultSealDeadline bounds writer drain and the container-status proof.
	// An unconfirmed drain is ErrSealUnconfirmed and publishes no receipt.
	DefaultSealDeadline = 5 * time.Minute
	MinSealDeadline     = 30 * time.Second
	MaxSealDeadline     = 30 * time.Minute

	// DefaultPublicationGrace is how long a marked, unregistered object is left
	// alone before inventory may treat it as an orphan. Grace reduces work and
	// provides recovery margin; it is never the claim/reclaim mutex.
	DefaultPublicationGrace = 8 * 24 * time.Hour
	MaxPublicationGrace     = 30 * 24 * time.Hour

	// PublicationGraceMargin is how far publication grace must exceed the
	// configured maximum capture deadline. Without it, an object could become
	// adoptable while its own capture was still legitimately retrying.
	PublicationGraceMargin = time.Hour

	// MinLeaseTerm is the floor for every renewable database-clock lease:
	// capture ownership, read leases and reclaim work.
	MinLeaseTerm = 15 * time.Minute

	// MaxLeaseTerm is the ceiling, and it is the same number the read-lease
	// table's CHECK stops at (86400 seconds). A protection longer than a day is
	// a generation pinned against reclaim for a day by one request, and the
	// caller who asked for it is the party being protected.
	//
	// It lives here, beside the derivation, so that the bound is read where the
	// policy is. A request refused only by the column comes back carrying a
	// constraint's text, which names a column no consumer has heard of.
	MaxLeaseTerm = 24 * time.Hour

	// LeaseRenewInterval is the slowest acceptable renewal. A lease renewed
	// less often than this cannot be distinguished from an owner that died.
	LeaseRenewInterval = time.Minute

	// LeaseTermMargin is added to a covered operation's timeout when deriving a
	// lease term, and LeaseStartMargin is how much of the lease must remain
	// before work may begin. Work that starts with less has no way to finish
	// inside its own authority.
	LeaseTermMargin  = 5 * time.Minute
	LeaseStartMargin = 2 * time.Minute

	// The bounds on one inventory pass. Every one of them is a stop condition,
	// not a target: a pass that hits any of them commits its per-object
	// dispositions and stops without advancing further.
	MaxInventoryPageObjects       = 100
	MaxInventoryPageMetadataBytes = 8 << 20
	MaxInventoryPassDuration      = 30 * time.Second

	// MaxPolicyEvidenceAge is the bounded staleness of the lifetime-policy
	// attestation. It is explicitly a detection window and not prevention: a
	// functioning monitor notices a changed lifecycle rule within it, and
	// cannot stop a deletion inside it.
	MaxPolicyEvidenceAge = 15 * time.Minute

	// WorkerFallbackInterval is the slowest acceptable periodic wake for every
	// worker in this plane. NOTIFY accelerates work; it is never the only way
	// work is found, because component.Runner with a zero interval wakes only
	// on NOTIFY and a missed one would strand eligible work until restart.
	WorkerFallbackInterval = time.Minute
)

func ValidateCaptureDeadline(deadline time.Duration) error {
	if deadline < MinCaptureDeadline || deadline > MaxCaptureDeadline {
		return fmt.Errorf("%w: capture deadline %s is outside %s..%s",
			ErrIncomplete, deadline, MinCaptureDeadline, MaxCaptureDeadline)
	}

	return nil
}

func ValidateSealDeadline(deadline time.Duration) error {
	if deadline < MinSealDeadline || deadline > MaxSealDeadline {
		return fmt.Errorf("%w: seal deadline %s is outside %s..%s",
			ErrIncomplete, deadline, MinSealDeadline, MaxSealDeadline)
	}

	return nil
}

// ValidatePublicationGrace is the cross-constraint from Req 39. It takes the
// configured maximum capture deadline rather than the constant, because a
// deployment that lowered its capture deadline may lower its grace with it.
func ValidatePublicationGrace(grace, maxCaptureDeadline time.Duration) error {
	if err := ValidateCaptureDeadline(maxCaptureDeadline); err != nil {
		return err
	}
	if grace > MaxPublicationGrace {
		return fmt.Errorf("%w: publication grace %s exceeds the maximum %s",
			ErrIncomplete, grace, MaxPublicationGrace)
	}
	if grace < maxCaptureDeadline+PublicationGraceMargin {
		return fmt.Errorf("%w: publication grace %s does not exceed the maximum capture deadline "+
			"%s by at least %s; an object could become adoptable while its own capture was still "+
			"legitimately retrying", ErrIncomplete, grace, maxCaptureDeadline, PublicationGraceMargin)
	}

	return nil
}

// LeaseTermFor derives the lease term covering an operation with the given
// timeout: at least MinLeaseTerm, and at least the timeout plus LeaseTermMargin.
func LeaseTermFor(operationTimeout time.Duration) time.Duration {
	derived := operationTimeout + LeaseTermMargin
	if derived < MinLeaseTerm {
		return MinLeaseTerm
	}

	return derived
}

// ValidateMaterializationTimeout checks a covered operation's timeout against
// the lease term that will be derived from it.
//
// One spelling, called from both request surfaces: the leaf's ReadLeaseRequest
// and the control plane's ReadRequest ask the same question, and two copies of
// a bound are two chances for one of them to be the one that was not updated.
//
// Only the ceiling can be crossed. LeaseTermFor floors at MinLeaseTerm, so no
// positive timeout can derive a term below it.
func ValidateMaterializationTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("%w: no materialization timeout; the lease term is derived from it",
			ErrIncomplete)
	}
	if term := LeaseTermFor(timeout); term > MaxLeaseTerm {
		return fmt.Errorf("%w: a materialization timeout of %s derives a lease term of %s and "+
			"the bound is %s; a read protects a generation against reclaim for as long as its "+
			"lease lasts", ErrIncomplete, timeout, term, MaxLeaseTerm)
	}

	return nil
}

// MayStartWork reports whether enough of a lease remains to begin an operation
// with the given timeout. It is the guard that keeps a delete or a
// materialization from starting under authority it will outlive.
func MayStartWork(remaining, operationTimeout time.Duration) bool {
	return remaining >= operationTimeout+LeaseStartMargin
}

// ValidatePolicyEvidenceAge fails closed on stale attestation.
func ValidatePolicyEvidenceAge(age time.Duration) error {
	if age < 0 {
		return fmt.Errorf("%w: policy evidence is dated in the future by %s", ErrIncomplete, -age)
	}
	if age > MaxPolicyEvidenceAge {
		return fmt.Errorf("%w: policy evidence is %s old, the bound is %s",
			ErrAtRisk, age, MaxPolicyEvidenceAge)
	}

	return nil
}
