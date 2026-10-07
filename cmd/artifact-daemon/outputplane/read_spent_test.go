package outputplane

import (
	"errors"
	"testing"
	"time"

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
