package outputplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func readClaims(lease string, expires time.Time) output.ReadWarrantClaims {
	return output.ReadWarrantClaims{ReadLeaseID: output.ReadLeaseID(lease), ExpiresAt: output.NewTimestamp(expires)}
}

// A read warrant opens one read on a node, and the node remembers it across a
// restart. This is what the read lease's release used to buy, kept on the node
// now that the node no longer asks the web.
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
	claims := readClaims("11111111-1111-4111-8111-111111111111", now.Add(time.Hour))

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
	refused := readClaims("22222222-2222-4222-8222-222222222222", now.Add(time.Hour))
	if err := restarted.begin(refused); err != nil {
		t.Fatal(err)
	}
	if err := restarted.end(refused, output.ErrConflict); err != nil {
		t.Fatal(err)
	}
	if err := restarted.begin(refused); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a conflicted read left its warrant usable: %v", err)
	}

	// And an entry past its warrant's window is pruned on reopen.
	later := func() time.Time { return now.Add(2 * time.Hour) }
	pruned, err := openSpentReads(store, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.spent) != 0 {
		t.Errorf("expired spent warrants survived a reopen: %v", pruned.spent)
	}
}

// A read warrant names one node, and the node's daemon refuses a warrant
// minted for another -- before it opens anything or spends anything. Together
// with the spent record this makes a warrant one read in the whole cluster.
func TestAReadWarrantForAnotherNodeIsRefusedAndSpendsNothing(t *testing.T) {
	fixture := newRoutes(t, "")
	verifier, err := executioncontrol.NewCapabilityVerifier(capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(fixture.daemon, fixture.ledger, fixture.source, verifier, "")
	store, err := openControlStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := server.configureReads(fixture.config, store); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(fixture.config.MaterializationKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := output.NewReadWarrantSigner(key)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	ref := fixture.daemon.Namespace().Ref(hangar.Digest("sha256:"+strings.Repeat("ab", 32)), 1)
	lease := output.ReadLease{
		ProtocolVersion: output.ProtocolVersion,
		ReadLeaseID:     "33333333-3333-4333-8333-333333333333",
		ClaimID:         "66666666-6666-4666-8666-666666666666",
		Ref:             ref,
		ActivationEpoch: fixture.daemon.ActivationEpoch(),
		LeaseFence:      1,
		GrantedAt:       output.NewTimestamp(now.Add(-time.Minute)),
		ExpiresAt:       output.NewTimestamp(now.Add(30 * time.Minute)),
	}
	destination := output.ReadDestination{Handle: "consumer", Volume: "input-0"}
	token, err := signer.Sign(lease, destination, "another-node", "AAAAAAAAAAAAAAAAAAAAAA")
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
	if len(server.reads.spent.spent) != 0 {
		t.Errorf("a refused warrant was spent: %v", server.reads.spent.spent)
	}

	// The control: the same warrant minted for THIS node passes authorization
	// (the read then fails further on: there is no object in the bucket).
	token, err = signer.Sign(lease, destination, executioncontrol.NodeUID(fixture.config.NodeUID), "AAAAAAAAAAAAAAAAAAAAAA")
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
