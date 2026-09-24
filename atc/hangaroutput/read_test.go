package hangaroutput_test

// Admitting a managed read, over a capture that really published.
//
// Everything here starts from the harness's own uninterrupted capture: a real
// output daemon sealed a real directory, published to a real GCS emulator and
// registered a real receipt in real PostgreSQL. So the ref a read is admitted
// against is one the plane actually produced, and the stat that admits it is a
// stat of an object that is actually there.
//
// The stat is the REAL publisher's StatExactObject against the same bucket the
// daemon published into, through the same derived namespace. A stub would have
// been asserting the fixture's opinion of the object; requirement 35 is about
// the object.
//
// What is injected, and only this: the ANSWER to the commit. A lost commit
// response is the shape the ambiguity rule exists for -- the rows are there and
// the caller does not know it -- and the injector wraps the real transactor and
// drops the reply AFTER the real commit.

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// readWarrantKey is the output plane's materialization key. It is not the receipt
// key and it is not the foundation's strict-input key, and nothing in this file
// lets it be either.
var readWarrantKey = []byte("0123456789abcdef0123456789abcdef")

// registeredRef drives one whole capture to a registered receipt and returns
// the tree ref it published.
func registeredRef(t *testing.T, h *harness) hangar.TreeRef {
	t.Helper()

	c := h.admit(t).hold(t).finish(t, true)
	c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Fatalf("the capture is %s, so there is no registered ref to read", record.State)
	}
	if record.Ref.Validate() != nil {
		t.Fatalf("the registered capture has no tree ref: %+v", record.Ref)
	}

	return record.Ref
}

// harnessStat is the real publisher's exact-generation stat, over the same
// bucket and derived namespace the daemon published into.
func harnessStat(t *testing.T, h *harness) hangaroutput.ExactStat {
	t.Helper()

	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           h.Bucket,
		DeploymentPrefix: "harness/one",
		TenantID:         "harness",
		ActivationEpoch:  harnessEpoch,
	})
	if err != nil {
		t.Fatalf("deriving the harness namespace: %v", err)
	}

	objects, closeObjects, err := hangargcs.NewObjectClient(context.Background(), h.Store.URL())
	if err != nil {
		t.Fatalf("object client for the emulator: %v", err)
	}
	t.Cleanup(func() { _ = closeObjects() })
	stat, err := publisher.New(namespace, publisher.Restrict(objects), 30*time.Second)
	if err != nil {
		t.Fatalf("publisher over the emulator: %v", err)
	}

	return stat
}

// claimOn acquires one claim on a ref through the production repository.
func claimOn(t *testing.T, h *harness, ref hangar.TreeRef) output.ClaimID {
	t.Helper()

	id := output.ClaimID(uuid.NewString())
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)

	if err := h.Repository.AcquireClaim(context.Background(), tx, output.ClaimAcquisition{
		ProtocolVersion:   output.ProtocolVersion,
		ClaimID:           id,
		Ref:               ref,
		ConsumerBindingID: output.OpaqueID("opaque-consumer-binding"),
		RequestedAt:       output.NewTimestamp(time.Now()),
	}); err != nil {
		t.Fatalf("acquiring a claim: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return id
}

// countingMinter is the real signer with a call counter around it.
//
// The counter is the assertion. "No usable warrant exists before the commit" is
// weaker than what requirement 35 asks for: the SIGNER must not have run, so
// that a rolled-back admission cannot have leaked a token into a log, a metric
// or an error message on its way out.
type countingMinter struct {
	inner *output.ReadWarrantSigner
	calls int
}

func (minter *countingMinter) Sign(lease output.ReadLease, destination output.ReadDestination, nonce string) (string, error) {
	minter.calls++

	return minter.inner.Sign(lease, destination, nonce)
}

func readAdmission(t *testing.T, h *harness) (*hangaroutput.ReadAdmission, *countingMinter) {
	t.Helper()

	signer, err := output.NewReadWarrantSigner(readWarrantKey)
	if err != nil {
		t.Fatalf("read warrant signer: %v", err)
	}
	minter := &countingMinter{inner: signer}

	return &hangaroutput.ReadAdmission{
		Transactor: h.Coordinator.Transactor,
		Leases:     h.Repository,
		Stat:       harnessStat(t, h),
		Minter:     minter,
		Clock:      output.ClockFunc(func() time.Time { return time.Now().UTC() }),
	}, minter
}

func readRequest(t *testing.T, claimID output.ClaimID, ref hangar.TreeRef) hangaroutput.ReadRequest {
	t.Helper()

	nonce, err := output.NewReadWarrantNonce(rand.Reader)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}

	return hangaroutput.ReadRequest{
		ReadLeaseID:            output.ReadLeaseID(uuid.NewString()),
		WarrantNonce:           nonce,
		ClaimID:                claimID,
		Ref:                    ref,
		Destination:            output.ReadDestination{Handle: "consumer-handle", Volume: "input-0"},
		ActivationEpoch:        harnessEpoch,
		MaterializationTimeout: 10 * time.Minute,
	}
}

// The control, asserted before every refusal below: a claimed, registered,
// marked generation admits a read and the warrant it hands back verifies with the
// production verifier.
func TestAManagedReadOverAPublishedRefMintsAVerifiableWarrant(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	request := readRequest(t, claimID, ref)

	warrant, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("admitting a managed read over a claimed registered ref: %v", err)
	}
	if minter.calls != 1 {
		t.Errorf("the signer ran %d times for one admission", minter.calls)
	}

	verifier, err := output.NewReadWarrantVerifier(readWarrantKey,
		output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		t.Fatalf("read warrant verifier: %v", err)
	}
	claims, err := verifier.Verify(warrant.Token, ref, request.Destination)
	if err != nil {
		t.Fatalf("the warrant a managed read handed back does not verify: %v", err)
	}
	if claims.ReadLeaseID != request.ReadLeaseID {
		t.Errorf("the warrant names lease %q, the request asked for %q",
			claims.ReadLeaseID, request.ReadLeaseID)
	}
	if claims.Nonce != request.WarrantNonce {
		t.Error("the warrant carries a nonce the caller did not generate; a re-mint could not be " +
			"byte-identical")
	}

	// And the daemon's own independent question is answerable for it.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)
	if _, err := h.Repository.ValidateReadLease(context.Background(), tx,
		output.ReadWarrantFor(claims, request.MaterializationTimeout+output.LeaseStartMargin)); err != nil {
		t.Fatalf("the control plane refused the lease its own warrant names: %v", err)
	}
}

// A rolled-back admission mints nothing, and the signer never runs.
func TestARolledBackManagedReadNeverReachesTheSigner(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor}
	admission.Transactor = failing

	request := readRequest(t, claimID, ref)
	failing.FailNext = true

	if _, err := admission.Admit(context.Background(), request); err == nil {
		t.Fatal("a managed read whose transaction could not commit handed back a warrant")
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
	}

	var leases int
	if err := h.Conn.QueryRow(
		`SELECT count(*) FROM hangar_read_leases WHERE read_lease_id = $1`,
		string(request.ReadLeaseID)).Scan(&leases); err != nil {
		t.Fatalf("counting leases: %v", err)
	}
	if leases != 0 {
		t.Error("a rolled-back admission left a read lease behind")
	}
}

// An ambiguous commit: the lease is there and the caller did not learn it.
//
// The retry asks about the SAME lease identity and delivers the SAME bytes. A
// second lease would be a second protection nobody would ever release.
func TestAnAmbiguousReadLeaseCommitRedeliversTheSameWarrant(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	ambiguous := &ambiguousTransactor{inner: admission.Transactor}
	admission.Transactor = ambiguous

	request := readRequest(t, claimID, ref)
	ambiguous.LoseNext = true

	first, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("an ambiguous commit was not resolved by identity: %v", err)
	}

	// The retry a caller performs with the same request.
	second, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("the retry after an ambiguous commit failed: %v", err)
	}
	if first.Token != second.Token {
		t.Error("the retry delivered a different warrant; requirement 37 asks for the same " +
			"nonce-bound bytes rather than another lease")
	}
	if minter.calls != 2 {
		t.Errorf("the signer ran %d times across one ambiguous admission and its retry",
			minter.calls)
	}

	var leases int
	if err := h.Conn.QueryRow(
		`SELECT count(*) FROM hangar_read_leases WHERE read_lease_id = $1`,
		string(request.ReadLeaseID)).Scan(&leases); err != nil {
		t.Fatalf("counting leases: %v", err)
	}
	if leases != 1 {
		t.Errorf("one ambiguous admission and one retry produced %d leases", leases)
	}
}

// Every refusal reaches no signer.
func TestAnUnadmittedManagedReadReachesNoSigner(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	for _, test := range []struct {
		name  string
		spoil func(*hangaroutput.ReadRequest)
	}{
		{"a claim nobody acquired", func(request *hangaroutput.ReadRequest) {
			request.ClaimID = output.ClaimID(uuid.NewString())
		}},
		{"a ref no receipt registered", func(request *hangaroutput.ReadRequest) {
			request.Ref.Generation++
		}},
		{"an epoch that is not the ref's", func(request *hangaroutput.ReadRequest) {
			request.ActivationEpoch = harnessEpoch + 1
		}},
		{"a destination that is a path", func(request *hangaroutput.ReadRequest) {
			request.Destination.Volume = "../elsewhere"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			admission, minter := readAdmission(t, h)
			request := readRequest(t, claimID, ref)
			test.spoil(&request)

			if _, err := admission.Admit(context.Background(), request); err == nil {
				t.Fatalf("%s was admitted", test.name)
			}
			if minter.calls != 0 {
				t.Errorf("the signer ran for %s", test.name)
			}
		})
	}
}

// A warrant is minted from the committed row, never from the request.
//
// The case is a lease id that already belongs to another read. Minting over it
// would hand this caller a warrant for protection somebody else owns.
func TestAWarrantIsNeverMintedForAnotherReadsLease(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, _ := readAdmission(t, h)
	first := readRequest(t, claimID, ref)
	if _, err := admission.Admit(context.Background(), first); err != nil {
		t.Fatalf("the first read: %v", err)
	}

	second := readRequest(t, claimID, ref)
	second.ReadLeaseID = first.ReadLeaseID

	_, err := admission.Admit(context.Background(), second)
	if !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a read that reused another read's lease id was answered %v", err)
	}
}

// A refusal the SCHEMA makes at COMMIT is a refusal, not a lost answer.
//
// The integrity gate runs at commit. Its typed refusal must not become an
// ambiguous answer that the caller retries indefinitely with the same identity.
func TestAManagedReadUnderAnAtRiskPolicyIsRefusedRatherThanLeftUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)
	recordAtRiskPolicy(t, h)

	admission, minter := readAdmission(t, h)

	_, err := admission.Admit(context.Background(), readRequest(t, claimID, ref))
	if !errors.Is(err, output.ErrAtRisk) {
		t.Fatalf("a read refused at commit by the integrity gate was answered %v", err)
	}
	if errors.Is(err, output.ErrUnresolved) {
		t.Error("a refusal the database made was reported as a lost commit answer; the caller " +
			"would retry with the same identity until an operator reconciled the finding")
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for a refused admission", minter.calls)
	}
}

// And the other half: a commit that genuinely loses its answer is STILL
// unresolved.
//
// The pair is the whole finding. Mapping the schema's classes must not turn
// every commit failure into a refusal -- a dropped connection carries no class,
// nothing is known about whether the rows landed, and the only honest answer is
// the one that sends the caller back with the same identity.
func TestACommitFailureWithNoSchemaClassStaysUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor}
	admission.Transactor = failing
	failing.FailNext = true

	_, err := admission.Admit(context.Background(), readRequest(t, claimID, ref))
	if !errors.Is(err, output.ErrUnresolved) {
		t.Fatalf("a commit whose answer was lost was reported as %v", err)
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
	}
}

// AND A COMMIT CARRYING A CLASS NOBODY NAMED IS STILL A LOST ANSWER.
//
// The spec above presents a commit failure with no SQLSTATE at all, which falls
// through the adapter's mapping unchanged. This is the other half, and it is the
// one refused()'s deliberate exclusion of ErrInfrastructure is written for: the
// adapter maps every SQLSTATE it does not recognise onto ErrInfrastructure, so a
// class really does arrive here -- 53300, "too many clients", is a database that
// could not even be asked -- and reading "a class arrived" as "the database
// answered no" would tell a caller to stop when nothing is known about whether
// its rows landed. The read lease is exactly the row a caller must ask about
// again with the SAME identity, which is why the request carries one.
//
// The commit error goes through db.HangarCommitError rather than being hand-
// built: what is under test is the plane's reading of a mapped commit, so the
// mapping has to be the production one.
func TestACommitFailingWithAnUnrecognisedSQLSTATEStaysUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	// The control: the mapping really does produce a class here, so a green
	// below is not "the exclusion was never exercised".
	unrecognised := db.HangarCommitError(&pgconn.PgError{
		Code: "53300", Message: "sorry, too many clients already"})
	if !errors.Is(unrecognised, output.ErrInfrastructure) {
		t.Fatalf("53300 mapped to %v; this spec is about the class an unrecognised SQLSTATE "+
			"produces and there is no longer one", unrecognised)
	}

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor, FailWith: unrecognised}
	admission.Transactor = failing
	failing.FailNext = true

	_, err := admission.Admit(context.Background(), readRequest(t, claimID, ref))
	if !errors.Is(err, output.ErrUnresolved) {
		t.Fatalf("a commit that failed with a SQLSTATE this plane does not name was reported "+
			"as %v; an outcome nobody named is not a denial, and a caller told one stops "+
			"instead of retrying with the same lease identity", err)
	}
	if errors.Is(err, output.ErrInfrastructure) {
		t.Error("the unrecognised class was passed on as the answer, so the ambiguity rule " +
			"never ran")
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
	}
}

// A TERM THE SCHEMA WOULD REFUSE IS REFUSED BY THE REQUEST SURFACE.
//
// lease_term_seconds is bounded at both ends by the column's CHECK. The floor is
// unreachable -- LeaseTermFor floors at MinLeaseTerm -- and the ceiling was
// reachable from a caller's own timeout: anything past 23h55m derives a term
// over 86400 seconds and the INSERT failed as SQLSTATE 23514. Nothing was lost
// in class (the adapter maps it to ErrIncomplete either way); what came back was
// the constraint's text, which names a column a consumer has never heard of and
// says nothing about what to ask for instead.
//
// So the bound is read where the policy is: the request. The schema's CHECK is
// the second line of defence, which is what a constraint is for.
func TestATermPastTheBoundIsRefusedByTheRequestAndNotByTheColumn(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	request := readRequest(t, claimID, ref)
	request.MaterializationTimeout = 24 * time.Hour

	_, err := admission.Admit(context.Background(), request)
	if !errors.Is(err, output.ErrIncomplete) {
		t.Fatalf("a timeout deriving a term past the bound answered %v", err)
	}
	if !strings.Contains(err.Error(), "the bound is") {
		t.Errorf("the refusal does not name the bound: %v", err)
	}
	if strings.Contains(err.Error(), "lease_term_seconds") {
		t.Errorf("the COLUMN refused this, not the request: %v. A caller reading a check "+
			"constraint's text cannot tell what to ask for instead", err)
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for a request that was never admitted", minter.calls)
	}

	var leases int
	if err := h.Conn.QueryRow(`SELECT count(*) FROM hangar_read_leases WHERE read_lease_id = $1`,
		string(request.ReadLeaseID)).Scan(&leases); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if leases != 0 {
		t.Error("a refused read still created a lease")
	}
}

// recordAtRiskPolicy records unexpected object loss through the runtime seam.
func recordAtRiskPolicy(t *testing.T, h *harness) {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)

	if err := h.Repository.RecordRuntimeAtRisk(context.Background(), tx, int64(harnessEpoch), output.PolicyFinding{Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected object loss"}); err != nil {
		t.Fatalf("recording the runtime finding: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// failingCommitTransactor makes a commit fail with an error carrying NO schema
// class, which is not the same thing as a refusal: nothing is known about
// whether the rows landed, and the caller is sent back with the same identity.
type failingCommitTransactor struct {
	inner    hangaroutput.Transactor
	FailNext bool

	// FailWith is what that commit answers, and it is a parameter because the
	// two shapes of unanswered commit are different facts. An error carrying no
	// SQLSTATE at all is a dropped connection. An error carrying a SQLSTATE
	// THIS PLANE DOES NOT NAME has been through the adapter's mapping and come
	// out as ErrInfrastructure -- which is the case refused()'s exclusion is
	// written for, and the one a "class arrived, so it is a denial" reading
	// would get wrong. Nil means commitRefused.
	FailWith error
}

var commitRefused = errors.New("the commit was refused")

func (transactor *failingCommitTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.inner.Begin()
	if err != nil {
		return nil, err
	}
	if transactor.FailNext {
		transactor.FailNext = false
		failure := transactor.FailWith
		if failure == nil {
			failure = commitRefused
		}

		return &refusingTransaction{Transaction: tx, failure: failure}, nil
	}

	return tx, nil
}

type refusingTransaction struct {
	hangaroutput.Transaction
	failure error
}

func (tx *refusingTransaction) Commit() error {
	_ = tx.Transaction.Rollback()

	return tx.failure
}
