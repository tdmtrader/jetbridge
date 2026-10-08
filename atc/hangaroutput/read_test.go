package hangaroutput_test

// Admitting a managed read, over a capture that really published.
//
// Everything here starts from the harness's own uninterrupted capture: a real
// output plane sealed a real directory, published to a real GCS emulator and
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
// response is the shape the ambiguity rule exists for -- the row is there and
// the caller does not know it -- and the injector wraps the real transactor and
// drops the reply AFTER the real commit.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// readWarrantKey is the output plane's materialization key. It is not the receipt
// key and it is not the foundation's strict-input key, and nothing in this file
// lets it be either.
var readWarrantKey = []byte("0123456789abcdef0123456789abcdef")

// registeredRef drives one whole capture to published and returns the tree
// ref it published.
func registeredRef(t *testing.T, h *harness) hangar.TreeRef {
	t.Helper()

	return registeredRefOf(t, h, "the bytes a producer wrote\n")
}

// registeredRefOf is registeredRef over content of the spec's choosing: two
// different contents are two different trees, so two different refs.
func registeredRefOf(t *testing.T, h *harness, content string) hangar.TreeRef {
	t.Helper()

	record := h.admit(t).produce(t, content).advance(t)
	ref, err := record.Ref()
	if err != nil {
		t.Fatalf("the capture is %s, so there is no published ref to read: %v", record.State, err)
	}

	return ref
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
	})
	if err != nil {
		t.Fatalf("deriving the harness namespace: %v", err)
	}

	objects, closeObjects, err := hangargcs.NewClient(context.Background(), h.Store.URL())
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

// claimOn acquires one consumer's claim on a ref through the production
// repository: a hold with no term.
func claimOn(t *testing.T, h *harness, ref hangar.TreeRef) output.ClaimID {
	t.Helper()

	id := output.ClaimID(uuid.NewString())
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)

	if _, err := h.Repository.AcquireClaim(context.Background(), tx, output.ClaimAcquisition{
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

// releaseClaim gives one claim back through the production repository.
func releaseClaim(t *testing.T, h *harness, id output.ClaimID, ref hangar.TreeRef) {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)

	if err := h.Repository.ReleaseClaim(context.Background(), db.HangarOutputTx{Tx: tx}, output.ClaimRelease{
		ProtocolVersion: output.ProtocolVersion,
		ClaimID:         id,
		Ref:             ref,
		RequestedAt:     output.NewTimestamp(time.Now()),
	}); err != nil {
		t.Fatalf("releasing claim %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// claimRows counts the rows one claim identity has: zero or one, since the
// identity is the primary key.
func claimRows(t *testing.T, h *harness, id output.ClaimID) int {
	t.Helper()

	var rows int
	if err := h.Conn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id = $1`,
		string(id)).Scan(&rows); err != nil {
		t.Fatalf("counting claim rows: %v", err)
	}

	return rows
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
	// signed is the claim the signer was last handed: the row, not the request.
	signed output.ClaimRecord
}

func (minter *countingMinter) Sign(claim output.ClaimRecord, destination output.ReadDestination, node executioncontrol.NodeUID) (string, error) {
	minter.calls++
	minter.signed = claim

	return minter.inner.Sign(claim, destination, node)
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
		Claims:     h.Repository,
		Stat:       harnessStat(t, h),
		Minter:     minter,
		Clock:      output.ClockFunc(func() time.Time { return time.Now().UTC() }),
	}, minter
}

// readRequest is one consumer's read: its own claim identity, generated
// before the attempt, which is what a retry after an ambiguous commit asks
// about.
func readRequest(t *testing.T, ref hangar.TreeRef) hangaroutput.ReadRequest {
	t.Helper()

	return hangaroutput.ReadRequest{
		ClaimID:                output.ClaimID(uuid.NewString()),
		Binding:                output.OpaqueID("result-read:consumer-handle"),
		Ref:                    ref,
		Destination:            output.ReadDestination{Handle: "consumer-handle", Volume: "input-0"},
		MaterializationTimeout: 10 * time.Minute,
		NodeUID:                harnessNode,
	}
}

func readVerifier(t *testing.T) *output.ReadWarrantVerifier {
	t.Helper()

	verifier, err := output.NewReadWarrantVerifier(readWarrantKey,
		output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		t.Fatalf("read warrant verifier: %v", err)
	}

	return verifier
}

// The control, asserted before every refusal below: a registered, marked
// generation admits a read; the read is a reader's claim whose term is the
// timeout plus the margin; and the warrant it hands back verifies with the
// production verifier and names that claim.
func TestAManagedReadOverAPublishedRefMintsAVerifiableWarrant(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	admission, minter := readAdmission(t, h)
	request := readRequest(t, ref)

	warrant, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("admitting a managed read over a registered ref: %v", err)
	}
	if minter.calls != 1 {
		t.Errorf("the signer ran %d times for one admission", minter.calls)
	}

	claims, err := readVerifier(t).Verify(warrant.Token, ref, request.Destination)
	if err != nil {
		t.Fatalf("the warrant a managed read handed back does not verify: %v", err)
	}
	if claims.ClaimID != request.ClaimID {
		t.Errorf("the warrant names claim %q, the request asked for %q", claims.ClaimID, request.ClaimID)
	}
	if claims.NodeUID != harnessNode {
		t.Errorf("the warrant names node %q, the request asked for %q", claims.NodeUID, harnessNode)
	}

	// The claim is the reader's: it expires, and its window is the request's
	// term on the database clock. The warrant's window is the claim's window
	// and nothing else: the instant of the mint is nowhere in the token.
	claim := warrant.Claim
	if claim.ExpiresAt == nil {
		t.Fatal("a read took a consumer's hold; a reader's claim expires")
	}
	if term := claim.ExpiresAt.Sub(claim.AcquiredAt.Time); term != request.Term() {
		t.Errorf("the claim's term is %s, the request derived %s", term, request.Term())
	}
	if !claims.IssuedAt.Equal(claim.AcquiredAt.Time) || !claims.ExpiresAt.Equal(claim.ExpiresAt.Time) {
		t.Errorf("the warrant's window [%s, %s] is not the claim's [%s, %s]",
			claims.IssuedAt, claims.ExpiresAt, claim.AcquiredAt, *claim.ExpiresAt)
	}
	if claimRows(t, h, request.ClaimID) != 1 {
		t.Error("the admitted read left no claim row behind")
	}
}

// A rolled-back admission mints nothing, and the signer never runs.
func TestARolledBackManagedReadNeverReachesTheSigner(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor}
	admission.Transactor = failing

	request := readRequest(t, ref)
	// Both the attempt and the identity-resolving repeat are refused: a
	// rolled-back admission is one whose claim never landed.
	failing.FailNext = 2

	if _, err := admission.Admit(context.Background(), request); err == nil {
		t.Fatal("a managed read whose transaction could not commit handed back a warrant")
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
	}
	if claimRows(t, h, request.ClaimID) != 0 {
		t.Error("a rolled-back admission left a reader's claim behind")
	}
}

// An ambiguous commit: the claim is there and the caller did not learn it.
//
// The admission resolves it by identity -- AcquireClaim again, with the
// CALLER's claim id, is idempotent and returns the committed row -- and mints
// from that row. A caller's own retry with the same request delivers the SAME
// bytes: nothing in the token comes from the instant of the mint, so there is
// no nonce to carry and nothing to re-mint differently. A second claim would
// be a second protection nobody would ever release.
func TestAnAmbiguousReadClaimCommitRedeliversTheSameWarrant(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	admission, minter := readAdmission(t, h)
	ambiguous := &ambiguousTransactor{inner: admission.Transactor}
	admission.Transactor = ambiguous

	request := readRequest(t, ref)
	ambiguous.LoseNext = true

	first, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("an ambiguous commit was not resolved by identity: %v", err)
	}
	if minter.calls != 1 {
		t.Errorf("the signer ran %d times for one admission whose commit answer was lost", minter.calls)
	}
	if minter.signed.ClaimID != request.ClaimID || minter.signed.ExpiresAt == nil {
		t.Errorf("the signer was handed %+v, not the committed reader's claim", minter.signed)
	}

	// The retry a caller performs with the same request.
	second, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("the retry after an ambiguous commit failed: %v", err)
	}
	if first.Token != second.Token {
		t.Error("the retry delivered a different warrant; requirement 37 asks for the same " +
			"bytes rather than another claim")
	}
	if minter.calls != 2 {
		t.Errorf("the signer ran %d times across one ambiguous admission and its retry",
			minter.calls)
	}
	if rows := claimRows(t, h, request.ClaimID); rows != 1 {
		t.Errorf("one ambiguous admission and one retry produced %d claims", rows)
	}
}

// Every refusal reaches no signer, and leaves no claim.
func TestAnUnadmittedManagedReadReachesNoSigner(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	reclaimed := registeredRefOf(t, h, "a generation the pass has already reclaimed\n")
	stampReclaimed(t, h, reclaimed)

	for _, test := range []struct {
		name  string
		spoil func(*hangaroutput.ReadRequest)
		class error
	}{
		{"a ref no receipt registered", func(request *hangaroutput.ReadRequest) {
			request.Ref.Generation++
		}, output.ErrNotFound},
		{"a generation already reclaimed", func(request *hangaroutput.ReadRequest) {
			request.Ref = reclaimed
		}, output.ErrNotFound},
		{"a destination that is a path", func(request *hangaroutput.ReadRequest) {
			request.Destination.Volume = "../elsewhere"
		}, output.ErrInvalidIdentity},
		{"no materialization timeout", func(request *hangaroutput.ReadRequest) {
			request.MaterializationTimeout = 0
		}, output.ErrIncomplete},
		{"no binding", func(request *hangaroutput.ReadRequest) {
			request.Binding = ""
		}, nil},
		{"no node", func(request *hangaroutput.ReadRequest) {
			request.NodeUID = ""
		}, output.ErrIncomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			admission, minter := readAdmission(t, h)
			request := readRequest(t, ref)
			test.spoil(&request)

			_, err := admission.Admit(context.Background(), request)
			if err == nil {
				t.Fatalf("%s was admitted", test.name)
			}
			if test.class != nil && !errors.Is(err, test.class) {
				t.Errorf("%s was refused as %v, want %v", test.name, err, test.class)
			}
			if minter.calls != 0 {
				t.Errorf("the signer ran for %s", test.name)
			}
			if claimRows(t, h, request.ClaimID) != 0 {
				t.Errorf("%s was refused and still left a claim", test.name)
			}
		})
	}
}

// A warrant is minted from the committed row, never from the request.
//
// The case is a claim id that already protects another generation, and a
// claim id that was given back. Minting over either would hand this caller a
// warrant for a protection that is not the one it asked for.
func TestAWarrantIsNeverMintedForAnotherReadsClaim(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	other := registeredRefOf(t, h, "another tree entirely\n")

	admission, minter := readAdmission(t, h)
	first := readRequest(t, ref)
	if _, err := admission.Admit(context.Background(), first); err != nil {
		t.Fatalf("the first read: %v", err)
	}
	minter.calls = 0

	elsewhere := readRequest(t, other)
	elsewhere.ClaimID = first.ClaimID
	if _, err := admission.Admit(context.Background(), elsewhere); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a read that reused another read's claim id on another ref was answered %v", err)
	}

	// The first read ends: its claim is released and tombstoned. The identity
	// is never a live hold again, so no warrant is ever minted over it again.
	releaseClaim(t, h, first.ClaimID, ref)
	if _, err := admission.Admit(context.Background(), first); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a read over a released claim identity was answered %v", err)
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for reads over a claim that is not theirs", minter.calls)
	}
}

// A refusal the SCHEMA makes at COMMIT is a refusal, not a lost answer.
//
// The integrity gate runs at commit. Its typed refusal must not become an
// ambiguous answer that the caller retries indefinitely with the same identity.
func TestAManagedReadUnderAnAtRiskPolicyIsRefusedRatherThanLeftUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	recordAtRiskPolicy(t, h)

	admission, minter := readAdmission(t, h)

	_, err := admission.Admit(context.Background(), readRequest(t, ref))
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

// And the other half: a commit that genuinely loses its answer -- TWICE, so
// that the identity-resolving repeat cannot settle it either -- is STILL
// unresolved.
//
// The pair is the whole finding. Mapping the schema's classes must not turn
// every commit failure into a refusal -- a dropped connection carries no class,
// nothing is known about whether the row landed, and the only honest answer is
// the one that sends the caller back with the same identity.
func TestACommitFailureWithNoSchemaClassStaysUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor}
	admission.Transactor = failing
	failing.FailNext = 2

	_, err := admission.Admit(context.Background(), readRequest(t, ref))
	if !errors.Is(err, output.ErrUnresolved) {
		t.Fatalf("a commit whose answer was lost was reported as %v", err)
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
	}
}

// A commit whose answer is lost ONCE is resolved by the repeat.
//
// The repeat is AcquireClaim again with the caller's identity: here the first
// commit really rolled back, so the repeat takes the claim now, and the warrant
// is minted from the row the repeat committed.
func TestACommitAnswerLostOnceIsResolvedByTheIdentityRepeat(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	admission, minter := readAdmission(t, h)
	failing := &failingCommitTransactor{inner: admission.Transactor}
	admission.Transactor = failing
	failing.FailNext = 1

	request := readRequest(t, ref)
	warrant, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("a commit whose answer was lost once was not resolved by the repeat: %v", err)
	}
	if minter.calls != 1 {
		t.Errorf("the signer ran %d times for one admission", minter.calls)
	}
	if claimRows(t, h, request.ClaimID) != 1 {
		t.Error("the repeat did not take the claim the lost commit rolled back")
	}
	if _, err := readVerifier(t).Verify(warrant.Token, ref, request.Destination); err != nil {
		t.Errorf("the warrant minted from the repeat's row does not verify: %v", err)
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
// its row landed. The reader's claim is exactly the row a caller must ask about
// again with the SAME identity, which is why the request carries one.
//
// The commit error goes through db.HangarCommitError rather than being hand-
// built: what is under test is the plane's reading of a mapped commit, so the
// mapping has to be the production one.
func TestACommitFailingWithAnUnrecognisedSQLSTATEStaysUnresolved(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

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
	failing.FailNext = 2

	_, err := admission.Admit(context.Background(), readRequest(t, ref))
	if !errors.Is(err, output.ErrUnresolved) {
		t.Fatalf("a commit that failed with a SQLSTATE this plane does not name was reported "+
			"as %v; an outcome nobody named is not a denial, and a caller told one stops "+
			"instead of retrying with the same claim identity", err)
	}
	if errors.Is(err, output.ErrInfrastructure) {
		t.Error("the unrecognised class was passed on as the answer, so the ambiguity rule " +
			"never ran")
	}
	if minter.calls != 0 {
		t.Errorf("the signer ran %d times for an admission that never committed", minter.calls)
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

	if err := h.Repository.RecordRuntimeAtRisk(context.Background(), tx, output.IntegrityFindingRecord{Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected object loss"}); err != nil {
		t.Fatalf("recording the runtime finding: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// stampReclaimed records a generation as gone, as the reclaim pass does after
// the store answered, without the pass: what a read of it finds is the row.
func stampReclaimed(t *testing.T, h *harness, ref hangar.TreeRef) {
	t.Helper()

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)

	if err := h.Repository.StampReclaimed(context.Background(), tx, ref); err != nil {
		t.Fatalf("stamping %v reclaimed: %v", ref, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// failingCommitTransactor makes the next FailNext commits fail with an error
// carrying NO schema class, which is not the same thing as a refusal: nothing
// is known about whether the rows landed, and the caller is sent back with the
// same identity.
type failingCommitTransactor struct {
	inner hangaroutput.Transactor
	// FailNext is how many commits in a row fail. The admission resolves one
	// lost answer by asking again, so proving an answer stays lost takes two.
	FailNext int

	// FailWith is what those commits answer, and it is a parameter because the
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
	if transactor.FailNext > 0 {
		transactor.FailNext--
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

// A registered generation the read finds missing fails closed, as before, and
// is recorded: a blocking integrity finding opens. The lifecycle row stays
// registered -- the reclaim pass is the one thing that stamps a generation
// reclaimed, and it does so once nothing holds it and the store confirms the
// absence.
func TestAManagedReadOfAMissingRegisteredGenerationRecordsTheAbsence(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)

	keys := h.bucketKeys(t)
	if len(keys) != 1 {
		t.Fatalf("want one object, have %v", keys)
	}
	deleter, closeDeleter, err := hangargcs.NewDeleteClient(context.Background(), h.Store.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeDeleter() }()
	if err := deleter.DeleteExact(context.Background(), h.Bucket, keys[0], ref.Generation); err != nil {
		t.Fatalf("removing the object behind the plane's back: %v", err)
	}

	admission, minter := readAdmission(t, h)
	admission.Absences = &db.HangarAbsences{Conn: h.Conn}
	request := readRequest(t, ref)
	if _, err := admission.Admit(context.Background(), request); !errors.Is(err, output.ErrNotFound) {
		t.Fatalf("a read of a missing generation answered %v, want not found", err)
	}
	if minter.calls != 0 {
		t.Error("a warrant was minted for a missing generation")
	}
	if claimRows(t, h, request.ClaimID) != 0 {
		t.Error("a read refused at the stat still took a claim; the stat runs before the transaction")
	}

	if reclaimedAt(t, h, ref) != nil {
		t.Error("a read stamped the generation reclaimed; only the reclaim pass does, after the store answered")
	}
	status := readStatus(t, h)
	if !status.AtRisk || status.Violations[output.ViolationOutOfBandAbsence] != 1 {
		t.Errorf("no blocking finding was recorded: %+v", status.Findings)
	}
}

// reclaimedAt is the lifecycle row's stamp: nil while the generation is
// registered.
func reclaimedAt(t *testing.T, h *harness, ref hangar.TreeRef) *time.Time {
	t.Helper()

	var stamped *time.Time
	if err := h.Conn.QueryRow(
		`SELECT reclaimed_at FROM hangar_exact_lifecycles WHERE scope = $1 AND digest = $2 AND generation = $3`,
		string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&stamped); err != nil {
		t.Fatalf("reading the lifecycle row of %v: %v", ref, err)
	}

	return stamped
}
