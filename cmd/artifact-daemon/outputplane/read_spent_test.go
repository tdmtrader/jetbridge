package outputplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func readWarrant(claim string, expires time.Time) hangar.Warrant {
	return hangar.Warrant{Purpose: hangar.PurposeReadResult, ClaimID: claim, ExpiresAt: expires.UnixNano()}
}

// A read warrant opens one read on a node, and the node remembers it across a
// restart, keyed by the reader's claim. The web gives the claim back when the
// read ends; this is the node's half, and the node never asks the web.
func TestAReadWarrantIsSpentOnceItsReadEnds(t *testing.T) {
	dir := t.TempDir()
	store, err := openControlStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	reads, err := openSpentReads(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	claims := readWarrant("11111111-1111-4111-8111-111111111111", now.Add(time.Hour))

	if err := reads.begin(claims); err != nil {
		t.Fatalf("a fresh warrant was refused: %v", err)
	}
	if err := reads.begin(claims); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a warrant already in use was admitted again: %v", err)
	}

	// A failure a retry can change does not spend it.
	if err := reads.end(claims, output.ErrInfrastructure); err != nil {
		t.Fatal(err)
	}
	if err := reads.begin(claims); err != nil {
		t.Fatalf("a transient failure spent the warrant: %v", err)
	}
	if err := reads.end(claims, nil); err != nil {
		t.Fatal(err)
	}
	if err := reads.begin(claims); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a spent warrant was admitted: %v", err)
	}

	// Durable: a restarted daemon still refuses it.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openControlStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted, err := openSpentReads(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.begin(claims); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a restart forgot a spent warrant: %v", err)
	}

	// A refusal the reader caused spends it too.
	refused := readWarrant("22222222-2222-4222-8222-222222222222", now.Add(time.Hour))
	if err := restarted.begin(refused); err != nil {
		t.Fatal(err)
	}
	if err := restarted.end(refused, output.ErrConflict); err != nil {
		t.Fatal(err)
	}
	if err := restarted.begin(refused); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a conflicted read left its warrant usable: %v", err)
	}

	// A read in flight is durable before it begins: a process that dies
	// mid-read leaves the warrant used, not reusable.
	crashed := readWarrant("44444444-4444-4444-8444-444444444444", now.Add(time.Hour))
	if err := restarted.begin(crashed); err != nil {
		t.Fatal(err)
	}
	afterCrash, err := openSpentReads(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := afterCrash.begin(crashed); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a warrant in flight at a crash was admitted again: %v", err)
	}

	// And an entry past its warrant's window is pruned on reopen.
	later := func() time.Time { return now.Add(2 * time.Hour) }
	pruned, err := openSpentReads(store, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.used) != 0 {
		t.Errorf("expired spent warrants survived a reopen: %v", pruned.used)
	}
}

// A read warrant names one node, and the node's daemon refuses a warrant
// minted for another -- before it opens anything or spends anything. Together
// with the spent record this makes a warrant one read in the whole cluster.
func TestAReadWarrantForAnotherNodeIsRefusedAndSpendsNothing(t *testing.T) {
	fixture := newRoutes(t)
	// The read warrant's window is its claim's, on the wall clock, so this
	// server verifies on the wall clock too.
	warrants, err := hangar.NewVerifier(hangarKey(), hangar.MaxWarrantTTL, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openControlStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	spent, err := openSpentWarrants(store, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(fixture.daemon, fixture.ledger, fixture.capture, warrants, spent)
	if err := server.configureReads(fixture.config, store); err != nil {
		t.Fatal(err)
	}
	webSigner, err := hangar.NewSigner(hangarKey(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := output.ReadWarrantMinter{Signer: webSigner}

	now := time.Now().UTC()
	ref := fixture.daemon.Namespace().Ref(hangar.Digest("sha256:"+strings.Repeat("ab", 32)), 1)
	expires := output.NewTimestamp(now.Add(10 * time.Minute))
	claim := output.ClaimRecord{
		ClaimID:           "66666666-6666-4666-8666-666666666666",
		Ref:               ref,
		ConsumerBindingID: "result-read:consumer",
		AcquiredAt:        output.NewTimestamp(now.Add(-time.Minute)),
		ExpiresAt:         &expires,
	}
	destination := output.ReadDestination{Handle: "consumer", Volume: "input-0"}
	token, err := signer.Sign(claim, destination, "another-node")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(output.ManagedReadRequest{Ref: ref, Destination: destination, Warrant: token})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/read/v1/materialize", bytes.NewReader(body)))
	if recorder.Code != http.StatusForbidden {
		t.Errorf("a warrant for another node answered %d", recorder.Code)
	}
	if len(server.reads.spent.used) != 0 {
		t.Errorf("a refused warrant was spent: %v", server.reads.spent.used)
	}

	// The control: the same warrant minted for THIS node passes authorization
	// (the read then fails further on: there is no object in the bucket).
	token, err = signer.Sign(claim, destination, executioncontrol.NodeUID(fixture.config.NodeUID))
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(output.ManagedReadRequest{Ref: ref, Destination: destination, Warrant: token})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/read/v1/materialize", bytes.NewReader(body)))
	if recorder.Code == http.StatusForbidden {
		t.Errorf("a warrant for this node was refused as unauthorized")
	}
}
