package output

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar"
)

// LeaseReadProfile is the daemon's half of a managed read.
//
// It is the thing that turns `hangar.Materializer.MaterializeManaged` from a
// shape into a transfer: Admit asks the control plane whether this warrant's lease
// may authorize work of the length about to be attempted, Renew keeps it current
// while bytes move, and Release gives the protection back on BOTH paths.
//
// The ordering it enforces is the requirement rather than a convenience.
// Requirement 36: a caller-provided warrant is not authority; the committed lease
// is, and the daemon validates the exact lease is still active BEFORE it opens
// anything. A profile that opened first and asked afterwards would be reading
// under authority the control plane may already have given away -- and the
// bytes would be on disk by the time it found out.
//
// It holds no claim logic of its own. The claim, the registered lifecycle, the
// fresh stat proof and the policy and reclaim exclusion are all decided in ONE
// caller-owned transaction on the control plane before a warrant is minted at all;
// this type's job is to keep the daemon from reading outside what that
// transaction decided.
type LeaseReadProfile struct {
	// Control is the lease-control client. It is the port and not a URL,
	// because the deployment's transport is mutually authenticated and this
	// package must not decide that.
	Control *LeaseControlClient

	// Warrant is the lease-bound token this read was delivered with. It moves:
	// a renewal answers with a re-minted token for the new window, and the
	// daemon's own pre-open window check is what would otherwise start failing
	// on a lease that is perfectly live.
	warrant string

	// materializationTimeout is how long the transfer this profile authorizes
	// is allowed to take, and it is what makes Req 36's margin real.
	//
	// Admit and Renew ask the control plane whether the lease has the
	// operation's timeout PLUS LeaseStartMargin left, and both used to ask for
	// zero -- which asks the database whether the lease has expired, a
	// different and much weaker rule. A read admitted with thirty seconds left
	// would start and be cut off mid-transfer, which is exactly what the margin
	// exists to prevent. The repository looked correct because the spec that
	// appeared to pin it supplied the margin itself.
	materializationTimeout time.Duration

	// mutex guards warrant. Renew runs on a ticker in RenewWhile's goroutine
	// while Release may run on the caller's, and a token read half-written is
	// a token no verifier accepts.
	mutex sync.Mutex

	// released makes Release idempotent in this process as well as on the
	// control plane. MaterializeManaged releases on the success path and on
	// the failure path, and a caller that also releases explicitly is the
	// normal shape rather than a mistake.
	released bool
}

// Deferred: a managed read reaches a consumer pod through a read lease the ATC
// acquires before the Pod is built, and that acquisition -- the claim, the lease
// transaction and the init-container route -- is the half of the Phase 8
// managed-read box this phase did not land. The profile itself is composed
// against the real control plane and the real materializer in atc/hangaroutput
//
// NewLeaseReadProfile binds one profile to one delivered warrant and to the
// timeout of the transfer it is about.
//
// The timeout is a parameter and not an option: it is the whole of Req 36's
// "work starts only with the timeout plus two minutes remaining", and a profile
// that could be built without one is a profile that asks the control plane
// whether the lease has expired.
func NewLeaseReadProfile(control *LeaseControlClient, warrant string,
	materializationTimeout time.Duration) (*LeaseReadProfile, error) {
	if control == nil {
		return nil, fmt.Errorf("%w: a managed read needs a lease-control client; an output "+
			"read is authorized by a committed lease and never by a warrant alone",
			ErrUnauthorized)
	}
	if warrant == "" {
		return nil, fmt.Errorf("%w: a managed read needs its lease-bound warrant",
			ErrIncomplete)
	}
	if err := ValidateMaterializationTimeout(materializationTimeout); err != nil {
		return nil, err
	}

	return &LeaseReadProfile{
		Control: control, warrant: warrant, materializationTimeout: materializationTimeout,
	}, nil
}

// requiredRemaining is Req 36's term, in one spelling: the operation's timeout
// plus the start margin. MayStartWork applies the same sum on the reclaim path.
func (profile *LeaseReadProfile) requiredRemaining() time.Duration {
	return profile.materializationTimeout + LeaseStartMargin
}

// Admit validates the lease before anything is opened, and returns how long the
// reader may work.
//
// The answer's own remaining time is the working term, not the caller's
// opinion of one: the control plane owns the row, and a reader that chose its
// own deadline would be choosing how long to hold a protection somebody else
// is waiting to reclaim.
//
// The ref, handle and volume are checked against the ANSWER rather than sent as
// the question's subject. The warrant already binds them -- a tree ref, a
// destination and a lease UUID are inside the signed bytes -- so what matters
// here is that the lease the control plane just described is the one this
// materialization is about. A profile that took the caller's word for that
// would let a warrant for one object authorize a read of another.
func (profile *LeaseReadProfile) Admit(ctx context.Context, ref hangar.TreeRef,
	handle, volume string) (time.Duration, error) {
	answer, err := profile.Control.ValidateLease(ctx, profile.current(), profile.requiredRemaining())
	if err != nil {
		return 0, err
	}
	if !answer.Admitted {
		return 0, fmt.Errorf("%w: the control plane refused this read lease: %s",
			ErrUnauthorized, answer.Refusal)
	}
	if err := profile.matches(answer, ref, handle, volume); err != nil {
		return 0, err
	}

	remaining := answer.Lease.ExpiresAt.Time.Sub(profile.Control.Clock.Now().UTC())
	if remaining <= 0 {
		return 0, fmt.Errorf("%w: the lease was admitted with no time left on it",
			ErrUnauthorized)
	}

	return remaining, nil
}

// Deferred: a managed read reaches a consumer pod through a read lease the ATC
// acquires before the Pod is built, and that acquisition -- the claim, the lease
// transaction and the init-container route -- is the half of the Phase 8
// managed-read box this phase did not land. The profile itself is composed
// against the real control plane and the real materializer in atc/hangaroutput
//
// Renew extends the lease and takes the re-minted token with it.
func (profile *LeaseReadProfile) Renew(ctx context.Context) error {
	answer, err := profile.Control.RenewLease(ctx, profile.current(), profile.requiredRemaining())
	if err != nil {
		return err
	}
	if !answer.Admitted {
		return fmt.Errorf("%w: the control plane refused to renew this read lease: %s",
			ErrUnauthorized, answer.Refusal)
	}
	if answer.Warrant == "" {
		return fmt.Errorf("%w: the renewal carried no re-minted warrant. A warrant is dated with "+
			"its lease's own instants, so a renewal that did not answer with a current token "+
			"would leave this reader holding one describing a window that has passed -- and "+
			"the pre-open window check would refuse a lease that is perfectly live",
			ErrIncomplete)
	}

	profile.mutex.Lock()
	profile.warrant = answer.Warrant
	profile.mutex.Unlock()

	return nil
}

// Release gives the protection back, whatever the staging did.
//
// Protection nobody is using is protection that has to be given back: an
// abandoned lease keeps reclaim admission refusing for its whole term, and the
// generation it names is pinned for exactly as long.
//
// The staging error is taken and not used, deliberately. A release that only
// happened on success would be one that never happened on the path that needs
// it most, and a release conditioned on the staging outcome is one a future
// edit turns into that.
func (profile *LeaseReadProfile) Release(ctx context.Context, _ error) error {
	profile.mutex.Lock()
	if profile.released {
		profile.mutex.Unlock()

		return nil
	}
	profile.released = true
	warrant := profile.warrant
	profile.mutex.Unlock()

	answer, err := profile.Control.ReleaseLease(ctx, warrant)
	if err != nil {
		return err
	}
	if !answer.Admitted {
		return fmt.Errorf("%w: the control plane refused to release this read lease: %s",
			ErrUnauthorized, answer.Refusal)
	}

	return nil
}

func (profile *LeaseReadProfile) current() string {
	profile.mutex.Lock()
	defer profile.mutex.Unlock()

	return profile.warrant
}

// matches is the check that makes the lease the authority rather than the
// caller's parameters.
func (profile *LeaseReadProfile) matches(answer LeaseAnswer, ref hangar.TreeRef,
	handle, volume string) error {
	if answer.Lease.Ref != ref {
		return fmt.Errorf("%w: this read is for %s/%s/%d and the admitted lease names "+
			"%s/%s/%d. A warrant for one object may not authorize a read of another",
			ErrUnauthorized, ref.Scope, ref.Digest, ref.Generation,
			answer.Lease.Ref.Scope, answer.Lease.Ref.Digest, answer.Lease.Ref.Generation)
	}
	if answer.Destination.Handle != handle || answer.Destination.Volume != volume {
		return fmt.Errorf("%w: this read stages into %s/%s and the admitted lease names "+
			"%s/%s. A destination the lease does not describe is a managed output landing "+
			"somewhere the control plane never agreed to",
			ErrUnauthorized, handle, volume,
			answer.Destination.Handle, answer.Destination.Volume)
	}

	return nil
}
