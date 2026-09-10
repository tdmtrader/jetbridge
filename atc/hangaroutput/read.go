package hangaroutput

// Admitting a managed-output read, with the commit boundary in the type system.
//
// Requirement 35 and the plan's "managed-read admission has an explicit commit
// boundary" describe one shape and it is worth stating plainly:
//
//  1. The exact-generation stat runs OUTSIDE every lock. It is a network call,
//     and no Hangar transaction holds a lock across one.
//  2. ONE caller-owned transaction revalidates the active claim, the registered
//     marked lifecycle, the policy and epoch, the reclaim exclusion and that
//     stat, and creates the fenced read lease. It signs nothing and calls
//     nobody.
//  3. Only after that transaction's commit is AUTHORITATIVE may a usable grant
//     be minted, and it is minted from the row loaded back rather than from the
//     values the caller passed in.
//
// The third step is the one that is easy to get subtly wrong, so it is a
// separate method taking a value only a committed row can produce, and
// read_guard_test.go fails the package if any function in this file both opens
// a transaction and signs.
//
// AMBIGUITY IS ANSWERED BY IDENTITY. If the commit's answer is lost, this does
// not guess: it reopens a transaction and asks for the lease by the identity
// the CALLER generated, and mints only if the committed row describes the read
// that was asked for. The nonce is the lease's own, so the retry delivers the
// same bytes rather than creating a second lease -- which is the whole reason
// the nonce is durable.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// ReadLeaseStore is the durable half of a managed read.
type ReadLeaseStore interface {
	AcquireReadLease(ctx context.Context, tx output.Tx, request output.ReadLeaseRequest) (output.ReadLease, error)
	LoadReadLease(ctx context.Context, tx output.Tx, id output.ReadLeaseID) (output.ReadLeaseRecord, error)
}

// ExactStat is the metadata stat performed outside the locks.
//
// It is the publisher's StatExactObject, declared here as the one operation
// this package consumes: an admission may read an exact generation's metadata
// and may do nothing else to the bucket.
type ExactStat interface {
	StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error)
}

// GrantMinter turns a committed lease into a usable token.
type GrantMinter interface {
	Sign(lease output.ReadLease, destination output.ReadDestination, nonce string) (string, error)
}

// ReadRequest is what a consumer asks for.
//
// ReadLeaseID and GrantNonce are the CALLER's, generated before the attempt, so
// that a retry after an ambiguous commit asks about the same lease rather than
// creating a second one. That is the same rule the capture side follows for its
// handoff identity, and for the same reason.
type ReadRequest struct {
	ReadLeaseID            output.ReadLeaseID
	GrantNonce             string
	ClaimID                output.ClaimID
	Ref                    hangar.TreeRef
	Destination            output.ReadDestination
	ActivationEpoch        executioncontrol.ActivationEpoch
	MaterializationTimeout time.Duration
}

func (request ReadRequest) Validate() error {
	if err := request.ReadLeaseID.Validate(); err != nil {
		return err
	}
	if err := request.ClaimID.Validate(); err != nil {
		return err
	}
	if err := request.Ref.Validate(); err != nil {
		return err
	}
	if err := request.Destination.Validate(); err != nil {
		return err
	}
	if request.ActivationEpoch == 0 {
		return fmt.Errorf("%w: a managed read names no activation epoch", output.ErrIncomplete)
	}
	if request.MaterializationTimeout <= 0 {
		return fmt.Errorf("%w: a managed read names no materialization timeout; the lease term "+
			"is derived from it", output.ErrIncomplete)
	}

	return nil
}

// ReadGrant is what a consumer receives: the token and the facts it carries.
type ReadGrant struct {
	Token  string
	Lease  output.ReadLease
	Record output.ReadLeaseRecord
}

// ReadAdmission is the control plane's managed-output read.
type ReadAdmission struct {
	Transactor Transactor
	Leases     ReadLeaseStore
	Stat       ExactStat
	Minter     GrantMinter
	Clock      output.Clock
}

// Admit performs the whole boundary: stat, one transaction, then mint.
func (admission *ReadAdmission) Admit(ctx context.Context, request ReadRequest) (ReadGrant, error) {
	if err := request.Validate(); err != nil {
		return ReadGrant{}, err
	}
	if err := admission.wired(); err != nil {
		return ReadGrant{}, err
	}

	// (1) Outside the locks.
	observed := output.NewTimestamp(admission.Clock.Now().UTC())
	object, err := admission.Stat.StatExactObject(ctx, request.Ref)
	if err != nil {
		return ReadGrant{}, fmt.Errorf("the exact-generation stat a managed read is admitted on: %w",
			err)
	}

	// (2) One transaction. It creates the lease and does nothing else.
	committed, err := admission.commitLease(ctx, request, object, observed)
	if err != nil {
		return ReadGrant{}, err
	}
	if !committed {
		// The commit's answer was lost. Resolve by identity: the lease id and
		// the nonce are the caller's, so the question "did my lease commit" has
		// an answer that does not depend on having seen one.
		if err := admission.resolveAmbiguity(ctx, request); err != nil {
			return ReadGrant{}, err
		}
	}

	// (3) After the commit is authoritative, and only then.
	record, err := admission.load(ctx, request)
	if err != nil {
		return ReadGrant{}, err
	}

	return admission.mint(record)
}

// commitLease is step 2, and it is the only function here that opens a
// transaction. It reports whether the commit was authoritative; an ambiguous
// answer is not an error, it is a question for step 2b.
func (admission *ReadAdmission) commitLease(ctx context.Context, request ReadRequest, object output.PublishedObject, observed output.Timestamp) (bool, error) {
	tx, err := admission.Transactor.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := admission.Leases.AcquireReadLease(ctx, tx, output.ReadLeaseRequest{
		ReadLeaseID:            request.ReadLeaseID,
		ClaimID:                request.ClaimID,
		Ref:                    request.Ref,
		ActivationEpoch:        request.ActivationEpoch,
		RequestedAt:            output.NewTimestamp(admission.Clock.Now().UTC()),
		MaterializationTimeout: request.MaterializationTimeout,
		Destination:            request.Destination,
		GrantNonce:             request.GrantNonce,
		StatProof:              object,
		StatObservedAt:         observed,
	}); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		// A refusal the database made is an answer; a lost answer is not. Only
		// the second one may be resolved by asking again, because asking again
		// after a refusal would turn a denial into a retry loop.
		//
		// The refusals this commit can carry are not only the ones raised while
		// the statements ran. hangar_policy_admits_new_protection and
		// hangar_reclaim_exclusion are DEFERRED, so an at-risk lifetime policy
		// and a racing reclaim both refuse HERE, at the commit, and they are
		// the two the ambiguity rule would misfile most expensively: a caller
		// told "your answer was lost, retry with the same identity" against a
		// policy only an attestor can change retries until something else
		// stops it.
		//
		// What makes the difference visible is the transactor: every adapter
		// that hands this package a transaction maps the schema's SQLSTATE at
		// commit, so a class that arrived is a class this reads. Anything with
		// no class -- a dropped connection, a cancelled context -- falls
		// through, and falling through is the honest answer: nothing is known
		// about whether the rows landed.
		if refused(err) {
			return false, err
		}

		return false, nil
	}

	return true, nil
}

// refused reports whether an error is the database's ANSWER rather than the
// absence of one.
//
// The list is the closed set of classes this plane's schema raises (JB001
// conflict, JB002 at risk, JB003 stale fence, JB004 incomplete) plus the
// timeout a stale stat proof produces. output.ErrInfrastructure is deliberately
// absent: it is what an unrecognised SQLSTATE maps to, and an outcome nobody
// named is not one this may treat as a denial.
func refused(err error) bool {
	return errors.Is(err, output.ErrConflict) || errors.Is(err, output.ErrNotFound) ||
		errors.Is(err, output.ErrAtRisk) || errors.Is(err, output.ErrTimeout) ||
		errors.Is(err, output.ErrIncomplete) || errors.Is(err, executioncontrol.ErrStaleFence)
}

// resolveAmbiguity asks whether the lost commit landed.
func (admission *ReadAdmission) resolveAmbiguity(ctx context.Context, request ReadRequest) error {
	record, err := admission.load(ctx, request)
	if err != nil {
		return fmt.Errorf("%w: the read lease commit's answer was lost and no committed lease %s "+
			"describes this read; the caller retries with the same identity", output.ErrUnresolved,
			request.ReadLeaseID)
	}
	_ = record

	return nil
}

// load reads the committed lease back.
//
// Loading rather than remembering is the point: a grant is minted from the row
// the database has, not from the values the caller passed in, so an ambiguous
// commit is answered by asking and a mint cannot describe a lease that never
// committed.
//
// It does NOT re-compare the row against the request. AcquireReadLease is
// idempotent on the identity and refuses that identity for different facts, so
// a row under this lease id is this read's or the transaction failed; a
// comparison here would be code no state can reach. The refusal a caller
// reusing another read's lease id receives is the repository's, and there is a
// spec for it.
func (admission *ReadAdmission) load(ctx context.Context, request ReadRequest) (output.ReadLeaseRecord, error) {
	tx, err := admission.Transactor.Begin()
	if err != nil {
		return output.ReadLeaseRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()

	return admission.Leases.LoadReadLease(ctx, tx, request.ReadLeaseID)
}

// mint is step 3. It opens no transaction, which is a rule this file's guard
// enforces rather than a habit.
func (admission *ReadAdmission) mint(record output.ReadLeaseRecord) (ReadGrant, error) {
	if err := record.Validate(); err != nil {
		return ReadGrant{}, err
	}

	token, err := admission.Minter.Sign(record.Lease, record.Destination, record.GrantNonce)
	if err != nil {
		return ReadGrant{}, err
	}

	return ReadGrant{Token: token, Lease: record.Lease, Record: record}, nil
}

func (admission *ReadAdmission) wired() error {
	switch {
	case admission == nil || admission.Transactor == nil:
		return fmt.Errorf("%w: a managed read needs a transactor", output.ErrIncomplete)
	case admission.Leases == nil:
		return fmt.Errorf("%w: a managed read needs a lease store", output.ErrIncomplete)
	case admission.Stat == nil:
		return fmt.Errorf("%w: a managed read needs the exact-generation stat it is admitted on",
			output.ErrIncomplete)
	case admission.Minter == nil:
		return fmt.Errorf("%w: a managed read needs a grant minter", output.ErrIncomplete)
	case admission.Clock == nil:
		return fmt.Errorf("%w: a managed read needs a clock", output.ErrIncomplete)
	}

	return nil
}
