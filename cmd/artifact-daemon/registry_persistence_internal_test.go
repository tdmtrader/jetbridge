package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestRegistryAliasPersistenceFollowsMutationOrder(t *testing.T) {
	storage := t.TempDir()
	oldPath := filepath.Join(storage, "steps", "old", "output")
	newPath := filepath.Join(storage, "steps", "new", "output")
	for _, path := range []string{oldPath, newPath} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	logger := lagertest.NewTestLogger("registry-persistence-order")
	store := NewAliasStore(logger, storage)
	registry := NewRegistry(logger)
	registry.SetAliasStore(store)
	registry.RegisterAlias("cache", oldPath)

	oldSnapshotReady := make(chan struct{})
	allowOldSnapshotSave := make(chan struct{})
	var pauseOldSnapshot sync.Once
	registry.beforePersistAliases = func(snapshot map[string]string) {
		if len(snapshot) == 0 {
			pauseOldSnapshot.Do(func() {
				close(oldSnapshotReady)
				<-allowOldSnapshotSave
			})
		}
	}

	removeDone := make(chan struct{})
	go func() {
		registry.RemoveIf("cache", oldPath)
		close(removeDone)
	}()
	<-oldSnapshotReady

	registerDone := make(chan struct{})
	go func() {
		registry.RegisterAlias("cache", newPath)
		close(registerDone)
	}()
	select {
	case <-registerDone:
		// Without persistence sequencing the newer save completes first and the
		// paused stale empty snapshot is about to overwrite it.
	case <-time.After(100 * time.Millisecond):
		// With sequencing, re-registration waits for the older mutation's save.
	}
	close(allowOldSnapshotSave)
	<-removeDone
	<-registerDone

	reloaded := NewRegistry(logger)
	reloaded.SetAliasStore(store)
	if err := reloaded.LoadAliases(); err != nil {
		t.Fatal(err)
	}
	path, found := reloaded.Lookup("cache")
	if !found || path != newPath {
		t.Fatalf("persisted re-registration = %q, %v; want %q", path, found, newPath)
	}
}
