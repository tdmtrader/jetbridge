package main

// Phase 1 of the root-relative-key migration: the tests that must be able to
// fail, written BEFORE the representation moves.
//
// Two of the three fail today. The third — guard-key agreement — passes today
// and is the dangerous one: a test written before a change, which passes
// before AND after, proves nothing. Its checkpoint therefore mutates the
// representation deliberately and observes the failure. Two tests in the
// predecessor track passed with and without their fix.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// AC1 — the reader's and the sweeper's guard keys must AGREE, by derivation.
//
// Asserting "they serialise" is satisfiable by a test that passes the same
// literal to both calls, which stays green when stepHandle is not migrated.
// The derivation is what is at risk, so the derivation is what is asserted.
func TestGuardKeys_ReaderAndSweeperAgree(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(lagertest.NewTestLogger("keys"), root, "node")
	if err != nil {
		t.Fatal(err)
	}

	// A real artifact, registered the way a stream-in registers one.
	const handle, output = "build-1", "out"
	dir := filepath.Join(root, "steps", handle, output)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Registry().Register(handle+"/"+output, dir)

	// The READER's key, derived exactly as a handler derives it: from the
	// value Lookup actually returns.
	v, found := s.Registry().Lookup(handle + "/" + output)
	if !found {
		t.Fatal("registry lost the entry")
	}
	readerKey := s.stepHandle(v)

	// The SWEEPER's key, derived exactly as the sweeper derives it: from the
	// directory entry name under steps/.
	entries, err := os.ReadDir(filepath.Join(root, "steps"))
	if err != nil {
		t.Fatal(err)
	}
	var sweeperKey string
	for _, e := range entries {
		if e.IsDir() {
			sweeperKey = e.Name()
		}
	}

	if readerKey != sweeperKey {
		t.Errorf("guard keys disagree — the read/sweep guard has silently stopped "+
			"excluding.\n  reader (stepHandle of a Lookup value): %q\n  sweeper (entry.Name):                  %q",
			readerKey, sweeperKey)
	}
}

// Proof the guard MECHANISM works, alongside the key-agreement test above.
// Failure direction is safe: a broken guard returns instantly rather than
// hanging, so this cannot flake into a false pass.
func TestGuardKeys_SweepBlocksWhileReadHeld(t *testing.T) {
	g := NewReadGuard()

	release := g.BeginRead("build-1")

	swept := make(chan struct{})
	go func() {
		r := g.BeginSweep("build-1")
		close(swept)
		r()
	}()

	select {
	case <-swept:
		t.Fatal("sweep acquired the lock while a read was held — the guard does not exclude")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case <-swept:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep never acquired the lock after the read released")
	}
}

// AC6 — sweeping build-4 must not evict build-42. FAILS TODAY:
// RemoveByPath compares with strings.HasPrefix, and "/…/steps/build-42" has
// "/…/steps/build-4" as a string prefix.
func TestRemoveByPath_DoesNotEvictASibling(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(lagertest.NewTestLogger("reg"), root)

	four := filepath.Join(root, "steps", "build-4")
	fortyTwo := filepath.Join(root, "steps", "build-42")
	// Register refuses what will not relativize, so the directories have to
	// exist for the symlink walk-up to resolve them.
	for _, d := range []string{four, fortyTwo} {
		if err := os.MkdirAll(filepath.Join(d, "out"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Register("build-4/out", filepath.Join(four, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("build-42/out", filepath.Join(fortyTwo, "out")); err != nil {
		t.Fatal(err)
	}

	r.RemoveByPath(four)

	if _, found := r.Lookup("build-42/out"); !found {
		t.Error("sweeping build-4 evicted build-42 — a string prefix is not a path boundary")
	}
	if _, found := r.Lookup("build-4/out"); found {
		t.Error("sweeping build-4 did not evict build-4")
	}
}

// AC4 migration half — AliasStore.Load must not drop relative values.
//
// FAILS TODAY: Load stats each value and drops misses as "stale"
// (alias_store.go:78). Relative values stat against the process CWD, so the
// migration would empty the alias store on first boot and log the wipe as
// routine — the loudest possible consequence written as the quietest possible
// log line.
func TestAliasStore_LoadKeepsRelativeValues(t *testing.T) {
	root := t.TempDir()

	// A real artifact, so the RELATIVE value resolves against the root even
	// though it does not resolve against the CWD.
	if err := os.MkdirAll(filepath.Join(root, "steps", "build-1", "out"), 0o755); err != nil {
		t.Fatal(err)
	}

	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	store := NewAliasStore(lagertest.NewTestLogger("alias"), root, handle)
	if err := store.Save(map[string]RelKey{"vol-a": "steps/build-1/out"}); err != nil {
		t.Fatal(err)
	}

	loaded, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := loaded["vol-a"]; !ok {
		t.Errorf("Load dropped a relative value as stale — it stats against the process CWD, "+
			"so migrating the representation would empty the alias store on first boot. Got: %v",
			loaded)
	}
}
