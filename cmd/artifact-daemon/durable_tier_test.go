package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

func newTier(t *testing.T, store durable.Store) (*DurableTier, *Server) {
	t.Helper()

	logger := lagertest.NewTestLogger("durable-test")
	server := newServerT(t, logger, t.TempDir(), "node-a")
	tier := NewDurableTier(logger, store, server.Metrics(), time.Minute)
	server.SetDurableTier(tier)

	return tier, server
}

// writeDir seeds a directory inside the server's storage root and returns its
// location, which is what every caller passes on to tarDirectory or Store.
func writeDir(t *testing.T, s *Server, name string, files map[string]string) RelKey {
	t.Helper()

	dir := filepath.Join(s.storagePath, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return RelKey(name)
}

func mustFS(t *testing.T) durable.Store {
	t.Helper()

	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	return store
}

// Restore promotes a staging directory too, and its destination is a container
// mount like every other resolve destination — so it takes the same
// world-traversable rule. Asserted here rather than in dest_permissions_test.go
// because the durable tier is driven directly, not over HTTP.
func TestRestoreLeavesATraversableDestination(t *testing.T) {
	tier, server := newTier(t, mustFS(t))
	work := t.TempDir()

	src := writeDir(t, server, "src-perm", map[string]string{"payload": "cached bytes"})
	tier.Store(context.Background(), "rc-perm", tarOf(server, src))

	dest := filepath.Join(work, "restored-perm")
	if !tier.Restore(context.Background(), "rc-perm", dest) {
		t.Fatal("Restore reported failure")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat restored dest: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o055 != 0o055 {
		t.Errorf("restored destination is mode %04o; a non-root task container cannot read it (want at least o+rx)", perm)
	}
}

func TestStoreThenRestoreRoundTripsADirectory(t *testing.T) {
	tier, server := newTier(t, mustFS(t))
	work := t.TempDir() // restore destinations are container mounts, outside the store

	src := writeDir(t, server, "src", map[string]string{"payload": "cached bytes"})

	tier.Store(context.Background(), "rc-1", tarOf(server, src))

	if !tier.Has(context.Background(), "rc-1") {
		t.Fatal("Store did not put the object")
	}

	dest := filepath.Join(work, "restored")
	if !tier.Restore(context.Background(), "rc-1", dest) {
		t.Fatal("Restore reported failure")
	}

	got, err := os.ReadFile(filepath.Join(dest, "payload"))
	if err != nil {
		t.Fatalf("read restored payload: %v", err)
	}
	if string(got) != "cached bytes" {
		t.Fatalf("restored %q, want %q", got, "cached bytes")
	}
}

func TestRestoreOfAnAbsentKeyLeavesTheDestinationAlone(t *testing.T) {
	tier, _ := newTier(t, mustFS(t))
	dest := filepath.Join(t.TempDir(), "restored")

	if tier.Restore(context.Background(), "rc-404", dest) {
		t.Fatal("Restore of an absent key reported success")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a failed Restore created the destination")
	}
}

// brokenStore fails every operation: an unreachable bucket, an expired
// credential, a bad endpoint.
type brokenStore struct{}

func (brokenStore) Stat(context.Context, string) (durable.Attributes, bool, error) {
	return durable.Attributes{}, false, errors.New("bucket unreachable")
}
func (brokenStore) Get(context.Context, string) (io.ReadCloser, bool, error) {
	return nil, false, errors.New("bucket unreachable")
}
func (brokenStore) Put(context.Context, string, io.Reader) error {
	return errors.New("bucket unreachable")
}
func (brokenStore) Delete(context.Context, string) error {
	return errors.New("bucket unreachable")
}
func (brokenStore) List(context.Context, func(durable.Attributes) error) error {
	return errors.New("bucket unreachable")
}

func TestABrokenStoreDegradesInsteadOfPropagating(t *testing.T) {
	// The property the whole design rests on. Every artifact here is
	// re-derivable, so an unreachable bucket must cost a re-download and
	// nothing else. The methods return bool precisely so there is no error for
	// a caller to accidentally turn into a failed build.
	tier, server := newTier(t, brokenStore{})
	work := t.TempDir() // restore destinations are container mounts, outside the store
	src := writeDir(t, server, "src", map[string]string{"payload": "x"})

	if tier.Has(context.Background(), "rc-1") {
		t.Error("Has against a broken store reported a hit")
	}
	if tier.Restore(context.Background(), "rc-1", filepath.Join(work, "dest")) {
		t.Error("Restore against a broken store reported success")
	}
	if tier.Delete(context.Background(), "rc-1") {
		t.Error("Delete against a broken store reported success")
	}

	// Store returns nothing; the assertion is that it neither panics nor
	// blocks, since it is meant to run detached from a request.
	tier.Store(context.Background(), "rc-1", tarOf(server, src))
}

func TestANilTierIsSafe(t *testing.T) {
	// The tier is optional, and call sites reach it through a possibly-nil
	// pointer rather than an interface check.
	var tier *DurableTier

	if tier.Has(context.Background(), "rc-1") {
		t.Error("nil tier reported a hit")
	}
	if tier.Restore(context.Background(), "rc-1", t.TempDir()) {
		t.Error("nil tier reported a restore")
	}
	if tier.Delete(context.Background(), "rc-1") {
		t.Error("nil tier reported a delete")
	}
	tier.Store(context.Background(), "rc-1", nil)
}

func TestConcurrentStoresOfOneKeyCollapse(t *testing.T) {
	counting := &countingStore{inner: mustFS(t)}
	tier, server := newTier(t, counting)
	src := writeDir(t, server, "src", map[string]string{"payload": "x"})

	done := make(chan struct{})
	for range 5 {
		go func() {
			defer func() { done <- struct{}{} }()
			tier.Store(context.Background(), "rc-5", tarOf(server, src))
		}()
	}
	for range 5 {
		<-done
	}

	if got := counting.puts.Load(); got > 1 {
		t.Fatalf("5 concurrent Stores produced %d uploads, want 1", got)
	}
}

// countingStore counts Puts so a test can prove concurrent uploads of one key
// collapse to a single transfer.
type countingStore struct {
	inner durable.Store
	puts  atomic.Int64
}

func (c *countingStore) Stat(ctx context.Context, key string) (durable.Attributes, bool, error) {
	return c.inner.Stat(ctx, key)
}
func (c *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	return c.inner.Get(ctx, key)
}
func (c *countingStore) Put(ctx context.Context, key string, body io.Reader) error {
	c.puts.Add(1)
	// Slow enough that the other callers are genuinely concurrent.
	time.Sleep(50 * time.Millisecond)

	return c.inner.Put(ctx, key, body)
}
func (c *countingStore) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}
func (c *countingStore) List(ctx context.Context, fn func(durable.Attributes) error) error {
	return c.inner.List(ctx, fn)
}

// A hostile object in the shared bucket must not escape the restore
// destination. This is the second ingest call site: Restore reaches the same
// extraction helper as a peer fetch, so the containment property has to hold
// here too — and requirement 9 of the track spec asks that this be asserted
// rather than assumed to follow from the shared helper.
func TestRestoreRefusesAnObjectThatEscapesItsDestination(t *testing.T) {
	tier, _ := newTier(t, mustFS(t))
	work := t.TempDir() // restore destinations are container mounts, outside the store

	// A file the archive will try to overwrite from inside the destination.
	outside := filepath.Join(work, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Put a hostile tar into the bucket directly, bypassing tarDirectory: the
	// premise is that bucket contents are untrusted and need not have been
	// produced by us.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777})
	body := []byte("PWNED")
	tw.WriteHeader(&tar.Header{Name: "hatch/victim.txt", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644})
	tw.Write(body)
	tw.Close()

	if err := tier.ObjectStore().Put(context.Background(), "rc-hostile", &buf); err != nil {
		t.Fatalf("seed hostile object: %v", err)
	}

	dest := filepath.Join(work, "restored")
	if tier.Restore(context.Background(), "rc-hostile", dest) {
		t.Error("Restore reported success for an object that escapes its destination")
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim unreadable: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("ESCAPE: file outside the destination was modified; got %q, want %q", got, "original")
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("a refused Restore promoted the destination directory")
	}

	// The tier extracts to a temp dir beside dest and renames on success. A
	// refusal must clean it, not leave residue for the sweeper to find.
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Errorf("refused Restore left temp residue: %s", e.Name())
		}
	}
}

// tarOf adapts tarDirectory to Store's callback, which no longer takes a path.
func tarOf(s *Server, loc RelKey) func(io.Writer) error {
	return func(w io.Writer) error { return s.tarDirectory(w, loc) }
}
