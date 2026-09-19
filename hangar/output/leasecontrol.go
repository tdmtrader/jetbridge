package output

// The lease-control protocol: the daemon's question and the control plane's
// answer.
//
// It exists because a read warrant is not authority by itself. The materializing
// daemon verifies the warrant's HMAC, which proves the control plane minted it,
// and then asks INDEPENDENTLY whether the exact lease that warrant names is still
// active -- because "this token was minted" and "this protection still holds"
// are different claims and only the database knows the second. A valid HMAC
// bound to a missing, released, expired, superseded or reclaim-conflicted lease
// authorizes nothing.
//
// The direction is the reverse of every other exchange in this plane: the ATC
// asks the daemon about executions, and here the daemon asks the ATC about a
// lease. So the authentication runs the other way too. The daemon signs its
// question with the SAME node control key it signs source-ledger and release
// statements with, under a domain of its own -- one node, one control identity,
// one epoch, and the domain is what keeps a lease question from being
// presentable as a release acknowledgement.
//
// THE QUESTION CARRIES THE WARRANT, not a set of fields copied out of it. A
// daemon that sent the fields could send different ones from the token it holds,
// and the control plane would be answering about a read nobody asked for.
//
// Three operations and no more. Validate before staging, renew while work
// proceeds, release after verified staging. There is no "extend indefinitely",
// no "take over" and no read of anything but the lease named by the warrant.

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// LeaseControlDomain separates a lease question from every other signature.
const LeaseControlDomain = "hangar-output-lease-control-v1"

// LeaseControlFacet is the authorization surface of the reverse direction.
//
// It is not CaptureFacet: a capture capability authorizes operations on a
// source incarnation, and nothing about a consumer's read should be reachable
// with one.
const LeaseControlFacet executioncontrol.Facet = "durable-output-read-lease"

// LeaseOperation is the closed set.
type LeaseOperation string

const (
	LeaseValidate LeaseOperation = "validate"
	LeaseRenew    LeaseOperation = "renew"
	LeaseRelease  LeaseOperation = "release"
)

func LeaseOperations() []LeaseOperation {
	return []LeaseOperation{LeaseValidate, LeaseRenew, LeaseRelease}
}

func (operation LeaseOperation) Validate() error {
	for _, member := range LeaseOperations() {
		if member == operation {
			return nil
		}
	}

	return fmt.Errorf("%w: lease operation %q; the vocabulary is %v",
		ErrUnknownMember, operation, LeaseOperations())
}

// MaxLeaseQuestionAge bounds how old a signed question may be when it arrives.
//
// A signed question with no freshness bound is a replayable one: a daemon's
// validate for a lease that has since been released could be replayed by
// anything that saw it, and the answer would still be about the lease rather
// than about the asker. One minute is the renewal interval -- a daemon that
// cannot get its question answered inside its own renewal period has a bigger
// problem than a stale signature.
const MaxLeaseQuestionAge = LeaseRenewInterval

// LeaseQuestion is what the daemon signs and sends.
type LeaseQuestion struct {
	ProtocolVersion string                   `json:"protocol_version"`
	Operation       LeaseOperation           `json:"operation"`
	KeyID           string                   `json:"key_id"`
	NodeUID         executioncontrol.NodeUID `json:"node_uid"`

	// Warrant is the token the consuming Pod presented, verbatim. The control
	// plane verifies it itself rather than trusting the daemon's reading of it:
	// a daemon that sent the fields could send different ones from the token it
	// holds.
	Warrant string `json:"warrant"`

	// RequiredRemainingSeconds is the work the daemon is about to start, or
	// zero for a release. The DATABASE decides whether that fits.
	//
	// A validate or a renew may not name ZERO, and that refusal is Req 36's
	// margin rather than tidiness. "Work starts only with the operation's
	// timeout plus two minutes remaining" is enforced by this number and by
	// nothing else -- the database's check is
	// `expires_at < now() + required_remaining` -- so a caller that passes zero
	// degenerates the whole rule to "not yet expired", and a read admitted with
	// thirty seconds left starts and is cut off mid-transfer. Every production
	// caller passed zero.
	RequiredRemainingSeconds int64 `json:"required_remaining_seconds"`

	IssuedAt  Timestamp `json:"issued_at"`
	Signature string    `json:"signature"`
}

func (question LeaseQuestion) Validate() error {
	if err := validateProtocol(question.ProtocolVersion); err != nil {
		return err
	}
	if err := question.Operation.Validate(); err != nil {
		return err
	}
	if question.KeyID == "" {
		return fmt.Errorf("%w: a lease question names no signing key", ErrIncomplete)
	}
	if len(question.KeyID) > MaxKeyIDBytes {
		return fmt.Errorf("%w: key id is %d bytes, the bound is %d",
			ErrLimitExceeded, len(question.KeyID), MaxKeyIDBytes)
	}
	if question.NodeUID == "" {
		return fmt.Errorf("%w: a lease question names no node", ErrInvalidIdentity)
	}
	if question.Warrant == "" || len(question.Warrant) > MaxReadWarrantBytes {
		return fmt.Errorf("%w: a lease question carries the warrant it is about, and this one is "+
			"%d bytes", ErrIncomplete, len(question.Warrant))
	}
	if question.RequiredRemainingSeconds < 0 {
		return fmt.Errorf("%w: a lease question asks for a negative remaining term", ErrIncomplete)
	}
	if question.Operation == LeaseRelease && question.RequiredRemainingSeconds != 0 {
		return fmt.Errorf("%w: a release names work it is about to start; a release starts none",
			ErrIncomplete)
	}
	if question.Operation != LeaseRelease && question.RequiredRemainingSeconds == 0 {
		return fmt.Errorf("%w: a %s names no work at all. Req 36 lets work begin only with the "+
			"operation's timeout plus %s remaining, and the database applies that as "+
			"`expires_at < now() + required_remaining` -- so zero asks only whether the lease "+
			"has expired, which is a different and much weaker rule. The bound itself stays the "+
			"caller's to compute, because only the caller knows what it is about to start",
			ErrIncomplete, question.Operation, LeaseStartMargin)
	}
	if question.Signature == "" {
		return fmt.Errorf("%w: a lease question is unsigned", ErrUnsigned)
	}

	return question.IssuedAt.Validate()
}

// RequiredRemaining is the term as a duration.
func (question LeaseQuestion) RequiredRemaining() time.Duration {
	return time.Duration(question.RequiredRemainingSeconds) * time.Second
}

// CanonicalLeaseQuestionBytes is what the signature covers.
func CanonicalLeaseQuestionBytes(question LeaseQuestion) []byte {
	writer := &fieldWriter{}
	writer.field(LeaseControlDomain)
	writer.field(question.ProtocolVersion)
	writer.field(string(question.Operation))
	writer.field(question.KeyID)
	writer.field(string(question.NodeUID))
	writer.field(question.Warrant)
	writer.number(uint64(question.RequiredRemainingSeconds))
	writer.field(question.IssuedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"))
	writer.field(question.Signature)

	return writer.out
}

// SignLeaseQuestion is the daemon's half, on the same signer as its other
// statements.
func (signer *CaptureStatementSigner) SignLeaseQuestion(question LeaseQuestion) (LeaseQuestion, error) {
	probe := question
	probe.Signature = "unsigned"
	if err := probe.Validate(); err != nil {
		return LeaseQuestion{}, err
	}

	question.Signature = ""
	question.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(signer.private, CanonicalLeaseQuestionBytes(question)))

	return question, nil
}

// VerifyLeaseQuestion checks a question against the node's pinned public key.
func VerifyLeaseQuestion(question LeaseQuestion, public ed25519.PublicKey) error {
	if err := question.Validate(); err != nil {
		return err
	}

	return verifyStatement(question.Signature, func(unsigned string) []byte {
		copied := question
		copied.Signature = unsigned

		return CanonicalLeaseQuestionBytes(copied)
	}, public, fmt.Sprintf("the %s lease question from node %s", question.Operation,
		question.NodeUID))
}

// LeaseAnswer is what the control plane says back.
//
// A refused answer carries a CLASS and no detail. The daemon needs to know
// whether to stop or to retry; which field of which row disagreed is the
// operator's question, and it belongs in the control plane's own log rather
// than in a body a Pod's init container can read.
type LeaseAnswer struct {
	ProtocolVersion string          `json:"protocol_version"`
	Operation       LeaseOperation  `json:"operation"`
	Admitted        bool            `json:"admitted"`
	Refusal         LeaseRefusal    `json:"refusal,omitempty"`
	Lease           ReadLease       `json:"lease,omitempty"`
	Destination     ReadDestination `json:"destination,omitempty"`

	// Warrant is the RE-MINTED token for the window this answer describes, and a
	// renewal is the only operation that carries one.
	//
	// A warrant is dated with its lease's granted-at and expires-at -- nothing in
	// it comes from the instant it was minted, which is what makes requirement
	// 37's byte-identical re-mint possible. The consequence is that a renewal
	// moves the row and cannot move a token already handed out, so unless the
	// renewal answers with a current one the reader carries a token describing
	// a window that has passed. That token still BINDS the same read, and the
	// control plane decides the window on the row; but the daemon checks the
	// warrant's own window before it opens anything, and a reader whose token
	// never caught up would fail that check while its lease was perfectly live.
	//
	// It is empty on a validate, on a release and on every refusal: a caller
	// told no reads no facts it was not admitted to, and that includes a token.
	Warrant string `json:"warrant,omitempty"`
}

// LeaseRefusal is the closed set of reasons a lease question is answered no.
type LeaseRefusal string

const (
	LeaseRefusedUnauthorized LeaseRefusal = "unauthorized"
	LeaseRefusedNotFound     LeaseRefusal = "not_found"
	LeaseRefusedConflict     LeaseRefusal = "conflict"
	LeaseRefusedExpired      LeaseRefusal = "expired"
	LeaseRefusedInfra        LeaseRefusal = "infrastructure_failure"
)

func LeaseRefusals() []LeaseRefusal {
	return []LeaseRefusal{
		LeaseRefusedUnauthorized, LeaseRefusedNotFound, LeaseRefusedConflict,
		LeaseRefusedExpired, LeaseRefusedInfra,
	}
}

func (answer LeaseAnswer) Validate() error {
	if err := validateProtocol(answer.ProtocolVersion); err != nil {
		return err
	}
	if err := answer.Operation.Validate(); err != nil {
		return err
	}
	if answer.Admitted {
		if answer.Refusal != "" {
			return fmt.Errorf("%w: an admitted answer carries the refusal %q", ErrIncomplete,
				answer.Refusal)
		}
		if err := answer.Lease.Validate(); err != nil {
			return err
		}
		if answer.Operation == LeaseRenew && answer.Warrant == "" {
			return fmt.Errorf("%w: an admitted renewal carries no re-minted warrant; the reader's "+
				"token would still name the window this renewal moved", ErrIncomplete)
		}
		if answer.Operation != LeaseRenew && answer.Warrant != "" {
			return fmt.Errorf("%w: a %s answer carries a warrant; only a renewal re-mints one",
				ErrIncomplete, answer.Operation)
		}
		if len(answer.Warrant) > MaxReadWarrantBytes {
			return fmt.Errorf("%w: the re-minted warrant is %d bytes, the bound is %d",
				ErrLimitExceeded, len(answer.Warrant), MaxReadWarrantBytes)
		}

		return answer.Destination.Validate()
	}
	if err := answer.Refusal.Validate(); err != nil {
		return err
	}
	if answer.Lease.ReadLeaseID != "" || answer.Destination != (ReadDestination{}) ||
		answer.Warrant != "" {
		return fmt.Errorf("%w: a refused answer describes a lease; a caller told no would read "+
			"facts it was not admitted to", ErrIncomplete)
	}

	return nil
}

func (refusal LeaseRefusal) Validate() error {
	for _, member := range LeaseRefusals() {
		if member == refusal {
			return nil
		}
	}

	return fmt.Errorf("%w: lease refusal %q; the vocabulary is %v",
		ErrUnknownMember, refusal, LeaseRefusals())
}
