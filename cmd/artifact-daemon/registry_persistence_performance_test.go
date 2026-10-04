package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func persistenceFixture(t *testing.T) (*daemon.Registry, *daemon.AliasStore, string) {
	t.Helper()
	registry, root := newRegistryAt(t)
	store := daemon.NewAliasStore(lagertest.NewTestLogger("persistence"), root, openRootT(t, root))
	registry.SetAliasStore(store)
	return registry, store, root
}

func assertPersistedAliases(t *testing.T, store *daemon.AliasStore, want map[string]daemon.RelKey) {
	t.Helper()
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted aliases: got %v, want %v", got, want)
	}
}

func TestRegistryPersistenceVisibleOnReturn(t *testing.T) {
	registry, store, root := persistenceFixture(t)
	first := mkStep(t, root, "steps", "first", "out")
	second := mkStep(t, root, "steps", "second", "out")
	if _, err := registry.RegisterAlias("first", first); err != nil {
		t.Fatal(err)
	}
	assertPersistedAliases(t, store, map[string]daemon.RelKey{"first": "steps/first/out"})
	if _, err := registry.RegisterReadOnlyAlias("second", second); err != nil {
		t.Fatal(err)
	}
	assertPersistedAliases(t, store, map[string]daemon.RelKey{"first": "steps/first/out", "second": "steps/second/out"})
	registry.Remove("first")
	assertPersistedAliases(t, store, map[string]daemon.RelKey{"second": "steps/second/out"})
	registry.RemoveByPath(filepath.Dir(second))
	assertPersistedAliases(t, store, map[string]daemon.RelKey{})
}

func TestRegistryPersistenceRetriesAfterRealWriteFailure(t *testing.T) {
	registry, store, root := persistenceFixture(t)
	first := mkStep(t, root, "steps", "first", "out")
	blocked := filepath.Join(root, "aliases.json.tmp")
	if err := os.Mkdir(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	// The real atomic-write path cannot replace a directory with file bytes.
	if _, err := registry.RegisterAlias("first", first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "aliases.json")); !os.IsNotExist(err) {
		t.Fatalf("failed write unexpectedly persisted: %v", err)
	}
	if _, found := registry.Lookup("first"); !found {
		t.Fatal("failed persistence discarded in-memory registration")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	second := mkStep(t, root, "steps", "second", "out")
	if _, err := registry.RegisterAlias("second", second); err != nil {
		t.Fatal(err)
	}
	assertPersistedAliases(t, store, map[string]daemon.RelKey{"first": "steps/first/out", "second": "steps/second/out"})
}

func TestRegistryPersistenceConcurrentWritersRetainEveryCompletedMutation(t *testing.T) {
	registry, store, root := persistenceFixture(t)
	const count = 64
	paths := make([]string, count)
	want := make(map[string]daemon.RelKey, count)
	for i := range paths {
		key := fmt.Sprintf("entry-%d", i)
		paths[i] = mkStep(t, root, "steps", key, "out")
		want[key] = daemon.RelKey("steps/" + key + "/out")
	}
	start := make(chan struct{})
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("entry-%d", i)
			if _, err := registry.RegisterAlias(key, paths[i]); err != nil {
				errors <- err
				return
			}
			snapshot, err := store.Load()
			if err != nil {
				errors <- err
				return
			}
			if snapshot[key] != want[key] {
				errors <- fmt.Errorf("completed registration %s missing from disk", key)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	assertPersistedAliases(t, store, want)
	// Remove disjoint entries concurrently and check the final snapshot too.
	for i := range paths {
		if i%2 != 0 {
			continue
		}
		wg.Add(1)
		go func(i int) { defer wg.Done(); registry.Remove(fmt.Sprintf("entry-%d", i)) }(i)
	}
	wg.Wait()
	for i := range paths {
		if i%2 == 0 {
			delete(want, fmt.Sprintf("entry-%d", i))
		}
	}
	assertPersistedAliases(t, store, want)
}
