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
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// readGrantKey is the output plane's materialization key. It is not the receipt
// key and it is not the foundation's strict-input key, and nothing in this file
// lets it be either.
var readGrantKey = []byte("0123456789abcdef0123456789abcdef")

// registeredRef drives one whole capture to a registered receipt and returns
// the exact ref it published.
func registeredRef(t *testing.T, h *harness) hangar.TreeRef {
	t.Helper()

	c := h.admit(t).hold(t).finish(t, true)
	c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Fatalf("the capture is %s, so there is no registered ref to read", record.State)
	}
	if record.Ref.Validate() != nil {
		t.Fatalf("the registered capture has no exact ref: %+v", record.Ref)
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

	client, err := hangargcs.NewStorageClient(context.Background(), h.Store.URL())
	if err != nil {
		t.Fatalf("storage client for the emulator: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	objects, err := hangargcs.NewObjectClient(client)
	if err != nil {
		t.Fatalf("object client for the emulator: %v", err)
	}
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
// The counter is the assertion. "No usable grant exists before the commit" is
// weaker than what requirement 35 asks for: the SIGNER must not have run, so
// that a rolled-back admission cannot have leaked a token into a log, a metric
// or an error message on its way out.
type countingMinter struct {
	inner *output.ReadGrantSigner
	calls int
}

func (minter *countingMinter) Sign(lease output.ReadLease, destination output.ReadDestination, nonce string) (string, error) {
	minter.calls++

	return minter.inner.Sign(lease, destination, nonce)
}

func readAdmission(t *testing.T, h *harness) (*hangaroutput.ReadAdmission, *countingMinter) {
	t.Helper()

	signer, err := output.NewReadGrantSigner(readGrantKey)
	if err != nil {
		t.Fatalf("read grant signer: %v", err)
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

	nonce, err := output.NewReadGrantNonce(rand.Reader)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}

	return hangaroutput.ReadRequest{
		ReadLeaseID:            output.ReadLeaseID(uuid.NewString()),
		GrantNonce:             nonce,
		ClaimID:                claimID,
		Ref:                    ref,
		Destination:            output.ReadDestination{Handle: "consumer-handle", Volume: "input-0"},
		ActivationEpoch:        harnessEpoch,
		MaterializationTimeout: 10 * time.Minute,
	}
}

// The control, asserted before every refusal below: a claimed, registered,
// marked generation admits a read and the grant it hands back verifies with the
// production verifier.
func TestAManagedReadOverAPublishedRefMintsAVerifiableGrant(t *testing.T) {
	h := newHarness(t)
	ref := registeredRef(t, h)
	claimID := claimOn(t, h, ref)

	admission, minter := readAdmission(t, h)
	request := readRequest(t, claimID, ref)

	grant, err := admission.Admit(context.Background(), request)
	if err != nil {
		t.Fatalf("admitting a managed read over a claimed registered ref: %v", err)
	}
	if minter.calls != 1 {
		t.Errorf("the signer ran %d times for one admission", minter.calls)
	}

	verifier, err := output.NewReadGrantVerifier(readGrantKey,
		output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		t.Fatalf("read grant verifier: %v", err)
	}
	claims, err := verifier.Verify(grant.Token, ref, request.Destination)
	if err != nil {
		t.Fatalf("the grant a managed read handed back does not verify: %v", err)
	}
	if claims.ReadLeaseID != request.ReadLeaseID {
		t.Errorf("the grant names lease %q, the request asked for %q",
			claims.ReadLeaseID, request.ReadLeaseID)
	}
	if claims.Nonce != request.GrantNonce {
		t.Error("the grant carries a nonce the caller did not generate; a re-mint could not be " +
			"byte-identical")
	}

	// And the daemon's own independent question is answerable for it.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer db.Rollback(tx)
	if _, err := h.Repository.ValidateReadLease(context.Background(), tx,
		output.ReadGrantFor(claims, request.MaterializationTimeout+output.LeaseStartMargin)); err != nil {
		t.Fatalf("the control plane refused the lease its own grant names: %v", err)
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
		t.Fatal("a managed read whose transaction could not commit handed back a grant")
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
func TestAnAmbiguousReadLeaseCommitRedeliversTheSameGrant(t *testing.T) {
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
		t.Error("the retry delivered a different grant; requirement 37 asks for the same " +
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

// A grant is minted from the committed row, never from the request.
//
// The case is a lease id that already belongs to another read. Minting over it
// would hand this caller a grant for protection somebody else owns.
func TestAGrantIsNeverMintedForAnotherReadsLease(t *testing.T) {
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

// failingCommitTransactor makes a commit REFUSE, which is not the same thing as
// losing its answer: a refusal is an answer, and answering it by asking again
// would turn a denial into a retry loop.
type failingCommitTransactor struct {
	inner    hangaroutput.Transactor
	FailNext bool
}

var commitRefused = errors.New("the commit was refused")

func (transactor *failingCommitTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.inner.Begin()
	if err != nil {
		return nil, err
	}
	if transactor.FailNext {
		transactor.FailNext = false

		return &refusingTransaction{Transaction: tx}, nil
	}

	return tx, nil
}

type refusingTransaction struct{ hangaroutput.Transaction }

func (tx *refusingTransaction) Commit() error {
	_ = tx.Transaction.Rollback()

	return commitRefused
}
