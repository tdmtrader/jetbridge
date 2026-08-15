package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

func newTier(t *testing.T, store durable.Store) (*DurableTier, *Server) {
	t.Helper()

	logger := lagertest.NewTestLogger("durable-test")
	server := NewServer(logger, t.TempDir(), "node-a")
	tier := NewDurableTier(logger, store, server.Metrics(), time.Minute)
	server.SetDurableTier(tier)

	return tier, server
}

func writeDir(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	return dir
}

func mustFS(t *testing.T) durable.Store {
	t.Helper()

	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	return store
}

func unavailableFS(t *testing.T) durable.Store {
	t.Helper()

	root := filepath.Join(t.TempDir(), "store")
	store, err := durable.NewFS(root, 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatalf("remove empty store root: %v", err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("replace store root with a regular file: %v", err)
	}

	return store
}

func TestStoreThenRestoreRoundTripsADirectory(t *testing.T) {
	tier, server := newTier(t, mustFS(t))
	work := t.TempDir()

	src := writeDir(t, work, "src", map[string]string{"payload": "cached bytes"})

	tier.Store(context.Background(), "rc-1", src, server.tarDirectory)

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

func TestABrokenStoreDegradesInsteadOfPropagating(t *testing.T) {
	// The property the whole design rests on. Every artifact here is
	// re-derivable, so an unreachable bucket must cost a re-download and
	// nothing else. The methods return bool precisely so there is no error for
	// a caller to accidentally turn into a failed build.
	tier, server := newTier(t, unavailableFS(t))
	work := t.TempDir()
	src := writeDir(t, work, "src", map[string]string{"payload": "x"})

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
	tier.Store(context.Background(), "rc-1", src, server.tarDirectory)
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
	tier.Store(context.Background(), "rc-1", t.TempDir(), nil)
}

func TestConcurrentStoresOfOneKeyCollapse(t *testing.T) {
	const concurrentStores = 5
	const key = "rc-5"

	protocol := newS3ProtocolState(t)
	store := protocol.store(t)
	tier, server := newTier(t, store)
	src := writeDir(t, t.TempDir(), "src", map[string]string{"payload": "x"})
	// Register the gate last so failure cleanup releases the transfer before
	// temporary source and destination directories are removed.
	transfer := protocol.gateTransfer(t, http.MethodPut, key)

	ready := make(chan struct{}, concurrentStores)
	start := make(chan struct{})
	done := make(chan struct{}, concurrentStores)
	for range concurrentStores {
		go func() {
			ready <- struct{}{}
			<-start
			tier.Store(context.Background(), key, src, server.tarDirectory)
			done <- struct{}{}
		}()
	}
	for range concurrentStores {
		<-ready
	}
	close(start)

	transfer.waitUntilEntered(t)

	// The transfer is still held at the S3 boundary. Every other Store must
	// therefore have observed that key as busy and returned without uploading.
	for range concurrentStores - 1 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a same-key Store did not collapse behind the in-flight upload")
		}
	}
	if got := len(protocol.requestsFor(http.MethodPut, key)); got != 1 {
		t.Fatalf("%d overlapping Stores produced %d S3 uploads, want exactly 1", concurrentStores, got)
	}

	transfer.open()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the admitted S3 upload did not finish after the backend became available")
	}

	if !tier.Has(context.Background(), key) {
		t.Fatal("the one admitted upload did not leave the object in the durable store")
	}
}
