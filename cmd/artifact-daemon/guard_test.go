package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

// The sweeper's RemoveAll races in-flight resolve copies: cp -R silently
// omits files deleted before it enumerates their directory, so a concurrent
// sweep yields a "complete" partial artifact (observed as npm ci failing on
// a missing package-lock.json). These tests pin the coordination contract:
// reads hold the guard shared; the sweeper takes it exclusive per directory
// and re-checks the mtime before deleting.

func expiredStepDir(t *testing.T, storagePath, handle string) string {
	t.Helper()
	dir := filepath.Join(storagePath, "steps", handle)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSweeper_BlocksOnInFlightRead_AndSparesTouchedDir(t *testing.T) {
	storagePath := t.TempDir()
	dir := expiredStepDir(t, storagePath, "in-use")

	guard := NewReadGuard()
	sweeper := NewSweeper(lagertest.NewTestLogger("sweeper"), storagePath, 2*time.Hour, 5*time.Minute, nil)
	sweeper.SetGuard(guard)

	release := guard.BeginRead("in-use")

	sweepDone := make(chan struct{})
	go func() {
		sweeper.SweepOnce()
		close(sweepDone)
	}()

	// The sweep must not delete the dir while a read is in flight.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-sweepDone:
		t.Fatal("sweep completed while a read held the guard")
	default:
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir removed while a read held the guard: %v", err)
	}

	// The reader touches the dir (as resolve does), then releases.
	now := time.Now()
	os.Chtimes(dir, now, now)
	release()

	<-sweepDone

	// The sweeper re-checked the mtime under the exclusive lock and spared it.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected freshly-read dir to survive the sweep: %v", err)
	}
}

func TestSweeper_RemovesExpiredDirAfterReadReleases_WhenNotTouched(t *testing.T) {
	storagePath := t.TempDir()
	dir := expiredStepDir(t, storagePath, "stale")

	guard := NewReadGuard()
	sweeper := NewSweeper(lagertest.NewTestLogger("sweeper"), storagePath, 2*time.Hour, 5*time.Minute, nil)
	sweeper.SetGuard(guard)

	release := guard.BeginRead("stale")
	sweepDone := make(chan struct{})
	go func() {
		sweeper.SweepOnce()
		close(sweepDone)
	}()
	time.Sleep(50 * time.Millisecond)
	release()
	<-sweepDone

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected untouched expired dir to be removed, stat err: %v", err)
	}
}

// resolveOne must hold the guard for the duration of its copy so the sweeper
// cannot delete the source mid-cp.
func TestResolve_HoldsReadGuardDuringCopy(t *testing.T) {
	storagePath := t.TempDir()
	srv := newServerT(t, lagertest.NewTestLogger("server"), storagePath, "node")

	dir := filepath.Join(storagePath, "steps", "handle-y", "output")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Hold the guard exclusively (as the sweeper would mid-removal); a
	// resolve must block rather than copy from a directory being deleted.
	release := srv.Guard().BeginSweep("handle-y")

	// A destination inside the store, which is the only shape production
	// produces: dest is a hostPath mount under the daemon's own storage path,
	// and both the handlers and the copy require containment.
	destParent := filepath.Join(storagePath, "steps", "consumer-y")
	if err := os.MkdirAll(destParent, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(destParent, "input-0")
	resolved := make(chan resolveResponse, 1)
	go func() {
		resolved <- srv.resolveOne(context.Background(), "handle-y/output", dest)
	}()

	select {
	case <-resolved:
		t.Fatal("resolve completed while the sweeper held the guard exclusively")
	case <-time.After(150 * time.Millisecond):
	}

	release()

	select {
	case resp := <-resolved:
		if resp.Status != "ok" {
			t.Fatalf("expected resolve ok after guard release, got %+v", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolve never completed after guard release")
	}

	if _, err := os.Stat(filepath.Join(dest, "data.txt")); err != nil {
		t.Errorf("resolved copy incomplete: %v", err)
	}
}

// The guard is keyed per step handle: work on one handle must not block
// work on another (a 5-minute mirror PUT of one artifact must not stall
// every read/replace on the node).
func TestReadGuard_IsKeyedPerHandle(t *testing.T) {
	guard := NewReadGuard()

	releaseRead := guard.BeginRead("handle-a")
	defer releaseRead()

	done := make(chan struct{})
	go func() {
		release := guard.BeginSweep("handle-b")
		release()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep of handle-b blocked behind read of handle-a")
	}
}
