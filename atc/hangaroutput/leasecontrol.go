package hangaroutput

// The control plane's half of lease control: three authenticated, product-
// neutral endpoints the materializing daemon calls.
//
// Nothing here knows what a build, a job, a Run, a workflow or a consumer is.
// The whole vocabulary is a signed question from a node, a grant, and a lease.
//
// EVERY ANSWER COMES OUT OF THE DATABASE. The handler verifies who is asking
// (the node's Ed25519 control key, under the lease-control domain), verifies
// the grant itself rather than the daemon's reading of it, and then asks the
// repository -- inside a transaction it owns -- whether that exact fenced lease
// may authorize work of the length the daemon named. A valid HMAC over a
// missing, released, expired, superseded or reclaim-conflicted lease is
// answered `no` here, which is the property requirement 37 is about.
//
// THE REFUSAL IS A CLASS. The caller is a container in a task's Pod one hop
// away; it needs to know whether to stop or to retry, and it does not need to
// know which column disagreed. The detail goes to this process's log.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// LeaseControlStore is the durable half of the three operations.
type LeaseControlStore interface {
	ValidateReadLease(ctx context.Context, tx output.Tx, validation output.ReadLeaseValidation) (output.ReadLeaseRecord, error)
	RenewReadLease(ctx context.Context, tx output.Tx, lease output.ReadLease) (output.ReadLease, error)
	ReleaseReadLease(ctx context.Context, tx output.Tx, lease output.ReadLease) error
}

// NodeKeys answers which public key a node's statements are checked against.
//
// It is a lookup rather than a single key because a cluster has many nodes and
// one of them being compromised must not make every other node's statements
// checkable by the same material. What it is NOT is a reader of the message: the
// key is chosen by node identity, never taken out of the statement that claims
// it.
type NodeKeys interface {
	PublicKeyFor(node executioncontrol.NodeUID, keyID string) (ed25519.PublicKey, error)
}

// NodeKeysFunc adapts a function.
type NodeKeysFunc func(executioncontrol.NodeUID, string) (ed25519.PublicKey, error)

func (fn NodeKeysFunc) PublicKeyFor(node executioncontrol.NodeUID, keyID string) (ed25519.PublicKey, error) {
	return fn(node, keyID)
}

// LeaseControl serves the three endpoints.
type LeaseControl struct {
	Transactor Transactor
	Leases     LeaseControlStore
	Grants     *output.ReadGrantVerifier
	Keys       NodeKeys
	Clock      output.Clock
	Logger     lager.Logger

	// Minter re-mints the grant a RENEWAL produces.
	//
	// A grant is dated with its lease's own instants, so a renewal moves the
	// row and cannot move the token the reader already holds. Without a token
	// for the new window the daemon's own pre-open window check -- which is
	// where a stale grant is supposed to be caught -- would start failing on a
	// lease that is perfectly live. It is the same signer the admission uses,
	// over the row this transaction just wrote.
	Minter GrantMinter
}

// Routes are the three paths, versioned and product-neutral.
const (
	LeaseValidatePath = "POST /read-lease/v1/validate"
	LeaseRenewPath    = "POST /read-lease/v1/renew"
	LeaseReleasePath  = "POST /read-lease/v1/release"
)

func (control *LeaseControl) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(LeaseValidatePath, control.serve(output.LeaseValidate))
	mux.Handle(LeaseRenewPath, control.serve(output.LeaseRenew))
	mux.Handle(LeaseReleasePath, control.serve(output.LeaseRelease))

	return mux
}

func (control *LeaseControl) serve(operation output.LeaseOperation) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		answer, status := control.answer(request, operation)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(answer)
	})
}

// answer is the whole decision, and it is deliberately one function: an
// authorization split across a middleware and a handler is one where the
// handler's author has to remember what the middleware checked.
func (control *LeaseControl) answer(request *http.Request, operation output.LeaseOperation) (output.LeaseAnswer, int) {
	refuse := func(refusal output.LeaseRefusal, err error) (output.LeaseAnswer, int) {
		if control.Logger != nil && err != nil {
			// The operation and the refusal class, and nothing else. The error's
			// own text names a lease, a ref or a node, and a log line is the one
			// place those accumulate where nobody audits them. An operator who
			// needs more looks at the row.
			control.Logger.Info("read-lease-refused", lager.Data{
				"transition": string(operation), "class": string(refusal),
			})
		}
		status := http.StatusForbidden
		switch refusal {
		case output.LeaseRefusedNotFound:
			status = http.StatusNotFound
		case output.LeaseRefusedConflict, output.LeaseRefusedExpired:
			status = http.StatusConflict
		case output.LeaseRefusedInfra:
			status = http.StatusServiceUnavailable
		}

		return output.LeaseAnswer{
			ProtocolVersion: output.ProtocolVersion,
			Operation:       operation,
			Admitted:        false,
			Refusal:         refusal,
		}, status
	}

	if err := control.wired(); err != nil {
		return refuse(output.LeaseRefusedInfra, err)
	}

	var question output.LeaseQuestion
	decoder := json.NewDecoder(http.MaxBytesReader(nil, request.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&question); err != nil {
		return refuse(output.LeaseRefusedUnauthorized, err)
	}
	if question.Operation != operation {
		return refuse(output.LeaseRefusedUnauthorized,
			fmt.Errorf("a %s question arrived at the %s route", question.Operation, operation))
	}

	// Who is asking. The key is chosen by the node identity in the question and
	// then used to check that identity's own signature; a key read out of the
	// message would authorize whoever wrote the message.
	public, err := control.Keys.PublicKeyFor(question.NodeUID, question.KeyID)
	if err != nil {
		return refuse(output.LeaseRefusedUnauthorized, err)
	}
	if err := output.VerifyLeaseQuestion(question, public); err != nil {
		return refuse(output.LeaseRefusedUnauthorized, err)
	}

	// And when. A signed question with no freshness bound is a replayable one.
	age := control.Clock.Now().UTC().Sub(question.IssuedAt.UTC())
	if age < -output.MaxLeaseQuestionAge || age > output.MaxLeaseQuestionAge {
		return refuse(output.LeaseRefusedUnauthorized,
			fmt.Errorf("the question is %s away from now, the bound is %s",
				age, output.MaxLeaseQuestionAge))
	}

	// The grant, verified HERE. The daemon holds it and could have sent
	// different fields from the ones it holds.
	claims, err := control.grantClaims(question)
	if err != nil {
		return refuse(output.LeaseRefusedUnauthorized, err)
	}

	record, err := control.decide(request.Context(), operation, claims, question.RequiredRemaining())
	if err != nil {
		return refuse(classify(err), err)
	}

	answer := output.LeaseAnswer{
		ProtocolVersion: output.ProtocolVersion,
		Operation:       operation,
		Admitted:        true,
		Lease:           record.Lease,
		Destination:     record.Destination,
	}

	// The re-mint, AFTER the transaction that renewed the row has committed and
	// out of the function that opened it -- the same ordering the admission
	// keeps, and for the same reason: a token minted beside an uncommitted row
	// is a token for a lease that may never have existed.
	if operation == output.LeaseRenew {
		token, err := control.Minter.Sign(record.Lease, record.Destination, record.GrantNonce)
		if err != nil {
			return refuse(output.LeaseRefusedInfra, err)
		}
		answer.Grant = token
	}

	return answer, http.StatusOK
}

// grantClaims verifies the grant against the ref and destination the grant
// itself names.
//
// There is nothing else to check it against: the question carries the token and
// no separate copy of its fields, which is exactly what stops a daemon from
// asking about a read its token does not authorize.
func (control *LeaseControl) grantClaims(question output.LeaseQuestion) (output.ReadGrantClaims, error) {
	var unverified output.ReadGrantClaims
	if err := output.DecodeReadGrantClaims(question.Grant, &unverified); err != nil {
		return output.ReadGrantClaims{}, err
	}

	// VerifyBinding, not Verify: what the token BINDS is the MAC's to settle and
	// whether the lease is still live is the ROW's, on the database clock. A
	// renewal moves the row's expiry and cannot move a token already handed
	// out, so a control plane that refused on the token's window would answer
	// `unauthorized` to a renewed reader asking to RELEASE -- and the
	// protection nobody needs would be held until recovery closed it. The
	// daemon's own Verify, before it opens anything, is where a stale window
	// stops a read.
	return control.Grants.VerifyBinding(question.Grant, unverified.Ref, unverified.Destination)
}

func (control *LeaseControl) decide(ctx context.Context, operation output.LeaseOperation, claims output.ReadGrantClaims, remaining time.Duration) (output.ReadLeaseRecord, error) {
	tx, err := control.Transactor.Begin()
	if err != nil {
		return output.ReadLeaseRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Validation runs for all three: a renewal or a release presented for a
	// lease this grant does not describe is the same forgery a staging request
	// would be, and answering it would let a token for one read close another
	// read's protection.
	record, err := control.Leases.ValidateReadLease(ctx, tx,
		output.ReadGrantFor(claims, remaining))
	if err != nil {
		return output.ReadLeaseRecord{}, err
	}

	switch operation {
	case output.LeaseValidate:
		return record, nil

	case output.LeaseRenew:
		renewed, err := control.Leases.RenewReadLease(ctx, tx, record.Lease)
		if err != nil {
			return output.ReadLeaseRecord{}, err
		}
		record.Lease = renewed

	case output.LeaseRelease:
		if err := control.Leases.ReleaseReadLease(ctx, tx, record.Lease); err != nil {
			return output.ReadLeaseRecord{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return output.ReadLeaseRecord{}, err
	}

	return record, nil
}

// classify maps a typed repository answer onto the refusal class the daemon
// sees. Anything unrecognised is infrastructure, never admission.
func classify(err error) output.LeaseRefusal {
	switch {
	case errors.Is(err, output.ErrNotFound):
		return output.LeaseRefusedNotFound
	case errors.Is(err, output.ErrTimeout):
		return output.LeaseRefusedExpired
	case errors.Is(err, output.ErrConflict), errors.Is(err, executioncontrol.ErrStaleFence):
		return output.LeaseRefusedConflict
	case errors.Is(err, output.ErrUnauthorized), errors.Is(err, output.ErrIncomplete),
		errors.Is(err, output.ErrInvalidIdentity):
		return output.LeaseRefusedUnauthorized
	}

	return output.LeaseRefusedInfra
}

func (control *LeaseControl) wired() error {
	switch {
	case control == nil || control.Transactor == nil:
		return fmt.Errorf("%w: lease control needs a transactor", output.ErrIncomplete)
	case control.Leases == nil:
		return fmt.Errorf("%w: lease control needs a lease store", output.ErrIncomplete)
	case control.Grants == nil:
		return fmt.Errorf("%w: lease control needs a grant verifier", output.ErrIncomplete)
	case control.Minter == nil:
		return fmt.Errorf("%w: lease control needs the minter a renewal re-mints its grant with",
			output.ErrIncomplete)
	case control.Keys == nil:
		return fmt.Errorf("%w: lease control needs the node keys it checks questions against",
			output.ErrIncomplete)
	case control.Clock == nil:
		return fmt.Errorf("%w: lease control needs a clock", output.ErrIncomplete)
	}

	return nil
}
