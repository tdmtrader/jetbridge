package hangaroutput

// Admitting a managed-output read, with the commit boundary in the type system.
//
// A read is a reader's claim and a warrant minted over it:
//
//  1. The exact-generation stat runs OUTSIDE every lock. It is a network call,
//     and no Hangar transaction holds a lock across one. It is the control
//     plane's pre-check, and the one place a registered generation found
//     missing becomes an integrity finding.
//  2. ONE caller-owned transaction acquires the reader's claim on the
//     registered generation, with a term of the materialization timeout plus
//     output.ReadClaimMargin. It signs nothing and calls nobody.
//  3. Only after that transaction's commit is AUTHORITATIVE may a usable warrant
//     be minted, and it is minted from the row the database returned rather
//     than from the values the caller passed in.
//
// The third step is the one that is easy to get subtly wrong, so it is a
// separate method taking a value only a committed row can produce, and
// read_guard_test.go fails the package if any function in this file both opens
// a transaction and signs.
//
// AMBIGUITY IS ANSWERED BY IDENTITY. If the commit's answer is lost, this does
// not guess: it acquires the same claim again, in a new transaction, by the
// identity the CALLER generated. AcquireClaim is idempotent on that identity
// and returns the committed row, so the retry mints the same bytes rather than
// taking a second claim.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// ExactStat is the metadata stat performed outside the locks.
//
// It is the publisher's StatExactObject, declared here as the one operation
// this package consumes: an admission may read an exact generation's metadata
// and may do nothing else to the bucket.
type ExactStat interface {
	StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error)
}

// WarrantMinter turns a committed reader's claim into a usable token.
type WarrantMinter interface {
	Sign(claim output.ClaimRecord, destination output.ReadDestination, node executioncontrol.NodeUID) (string, error)
}

// ReadRequest is what a consumer asks for.
//
// ClaimID is the CALLER's, generated before the attempt, so that a retry after
// an ambiguous commit asks about the same claim rather than taking a second
// one. That is the same rule the capture side follows for its capture key, and
// for the same reason. Binding is the caller's opaque name for this read, which
// it uses to find the claim again (a task's input reads are released by it).
type ReadRequest struct {
	ClaimID                output.ClaimID
	Binding                output.OpaqueID
	Ref                    hangar.TreeRef
	Destination            output.ReadDestination
	MaterializationTimeout time.Duration
	// NodeUID is the node whose daemon serves the read; the warrant opens
	// nothing on any other.
	NodeUID executioncontrol.NodeUID
}

func (request ReadRequest) Validate() error {
	if err := request.ClaimID.Validate(); err != nil {
		return err
	}
	if err := request.Binding.Validate(); err != nil {
		return err
	}
	if err := request.Ref.Validate(); err != nil {
		return err
	}
	if err := request.Destination.Validate(); err != nil {
		return err
	}
	if request.MaterializationTimeout <= 0 {
		return fmt.Errorf("%w: no materialization timeout; the reader's claim term is derived from it",
			output.ErrIncomplete)
	}
	if request.NodeUID == "" {
		return fmt.Errorf("%w: a managed read names no node", output.ErrIncomplete)
	}

	return nil
}

// Term is the reader's claim term: the read's own timeout plus the margin.
func (request ReadRequest) Term() time.Duration {
	return request.MaterializationTimeout + output.ReadClaimMargin
}

// ReadWarrant is what a consumer receives: the token and the claim it carries.
type ReadWarrant struct {
	Token string
	Claim output.ClaimRecord
}

// ReadAdmission is the control plane's managed-output read.
type ReadAdmission struct {
	Transactor Transactor
	Claims     *db.HangarOutputRepository
	Stat       ExactStat
	Minter     WarrantMinter
	Clock      output.Clock

	// Absences, when set, records a registered generation the stat found
	// missing as an integrity finding that blocks new admission. A read that
	// found the object gone fails closed either way; this makes it visible.
	Absences *db.HangarAbsences
}

// Admit performs the whole boundary: stat, one transaction, then mint.
func (admission *ReadAdmission) Admit(ctx context.Context, request ReadRequest) (ReadWarrant, error) {
	if err := request.Validate(); err != nil {
		return ReadWarrant{}, err
	}
	if err := admission.wired(); err != nil {
		return ReadWarrant{}, err
	}

	// (1) Outside the locks.
	if _, err := admission.Stat.StatExactObject(ctx, request.Ref); err != nil {
		if errors.Is(err, output.ErrNotFound) && admission.Absences != nil {
			if recordErr := admission.Absences.RecordUnexpectedAbsence(ctx, request.Ref); recordErr != nil {
				err = errors.Join(err, fmt.Errorf("recording the absence: %w", recordErr))
			}
		}
		return ReadWarrant{}, fmt.Errorf("the exact-generation stat a managed read is admitted on: %w",
			err)
	}

	// (2) One transaction. It takes the claim and does nothing else.
	claim, committed, err := admission.commitClaim(ctx, request)
	if err != nil {
		return ReadWarrant{}, err
	}
	if !committed {
		// The commit's answer was lost. Resolve by identity: the claim id is
		// the caller's, so the question "did my claim commit" has an answer
		// that does not depend on having seen one. The repeat is idempotent:
		// it returns the committed row, or takes the claim now.
		claim, committed, err = admission.commitClaim(ctx, request)
		if err != nil {
			return ReadWarrant{}, err
		}
		if !committed {
			return ReadWarrant{}, fmt.Errorf("%w: the read claim's commit answer was lost twice "+
				"for claim %s; the caller retries with the same identity", output.ErrUnresolved,
				request.ClaimID)
		}
	}

	// (3) After the commit is authoritative, and only then.
	return admission.mint(claim, request.Destination, request.NodeUID)
}

// commitClaim is step 2, and it is the only function here that opens a
// transaction. It reports whether the commit was authoritative; an ambiguous
// answer is not an error, it is a question for step 2b.
func (admission *ReadAdmission) commitClaim(ctx context.Context, request ReadRequest) (output.ClaimRecord, bool, error) {
	tx, err := admission.Transactor.Begin()
	if err != nil {
		return output.ClaimRecord{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	claim, err := admission.Claims.AcquireClaim(ctx, tx, output.ClaimAcquisition{
		ProtocolVersion:   output.ProtocolVersion,
		ClaimID:           request.ClaimID,
		Ref:               request.Ref,
		ConsumerBindingID: request.Binding,
		RequestedAt:       output.NewTimestamp(admission.Clock.Now().UTC()),
		Term:              request.Term(),
	})
	if err != nil {
		return output.ClaimRecord{}, false, err
	}
	if claim.ExpiresAt == nil {
		return output.ClaimRecord{}, false, fmt.Errorf("%w: claim %s is a consumer's hold, not a "+
			"reader's; a read warrant is minted over an expiring claim", output.ErrConflict,
			request.ClaimID)
	}

	if err := tx.Commit(); err != nil {
		// A refusal the database made is an answer; a lost answer is not. Only
		// the second one may be resolved by asking again, because asking again
		// after a refusal would turn a denial into a retry loop.
		//
		// hangar_policy_admits_new_protection is DEFERRED, so an unresolved
		// runtime finding refuses HERE, at the commit, and it is the one the
		// ambiguity rule would misfile most expensively: a caller told "your
		// answer was lost, retry with the same identity" against a finding
		// only an operator can reconcile retries until something else stops
		// it. Every adapter that hands this package a transaction maps the
		// schema's SQLSTATE at commit, so a class that arrived is a class this
		// reads. Anything with no class -- a dropped connection, a cancelled
		// context -- falls through, and falling through is the honest answer:
		// nothing is known about whether the row landed.
		if refused(err) {
			return output.ClaimRecord{}, false, err
		}

		return output.ClaimRecord{}, false, nil
	}

	return claim, true, nil
}

// refused reports whether an error is the database's ANSWER rather than the
// absence of one.
//
// The list is the closed set of classes this plane's schema raises (JB001
// conflict, JB002 at risk, JB003 stale fence, JB004 incomplete) plus not
// found. output.ErrInfrastructure is deliberately absent: it is what an
// unrecognised SQLSTATE maps to, and an outcome nobody named is not one this
// may treat as a denial.
func refused(err error) bool {
	return errors.Is(err, output.ErrConflict) || errors.Is(err, output.ErrNotFound) ||
		errors.Is(err, output.ErrAtRisk) || errors.Is(err, output.ErrTimeout) ||
		errors.Is(err, output.ErrIncomplete) || errors.Is(err, executioncontrol.ErrStaleFence)
}

// mint is step 3. It opens no transaction, which is a rule this file's guard
// enforces rather than a habit.
func (admission *ReadAdmission) mint(claim output.ClaimRecord, destination output.ReadDestination, node executioncontrol.NodeUID) (ReadWarrant, error) {
	if err := claim.Validate(); err != nil {
		return ReadWarrant{}, err
	}

	token, err := admission.Minter.Sign(claim, destination, node)
	if err != nil {
		return ReadWarrant{}, err
	}

	return ReadWarrant{Token: token, Claim: claim}, nil
}

func (admission *ReadAdmission) wired() error {
	switch {
	case admission == nil || admission.Transactor == nil:
		return fmt.Errorf("%w: a managed read needs a transactor", output.ErrIncomplete)
	case admission.Claims == nil:
		return fmt.Errorf("%w: a managed read needs a claim store", output.ErrIncomplete)
	case admission.Stat == nil:
		return fmt.Errorf("%w: a managed read needs the exact-generation stat it is admitted on",
			output.ErrIncomplete)
	case admission.Minter == nil:
		return fmt.Errorf("%w: a managed read needs a warrant minter", output.ErrIncomplete)
	case admission.Clock == nil:
		return fmt.Errorf("%w: a managed read needs a clock", output.ErrIncomplete)
	}

	return nil
}
