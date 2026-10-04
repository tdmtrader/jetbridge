package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestRegistryPersistRetriesSameGenerationAfterRealFailure(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	logger := lagertest.NewTestLogger("retry-generation")
	registry := NewRegistry(logger, dir)
	store := NewAliasStore(logger, dir, root)
	registry.SetAliasStore(store)
	target := filepath.Join(dir, "steps", "entry", "out")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "aliases.json.tmp")
	if err := os.Mkdir(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterAlias("entry", target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aliases.json")); !os.IsNotExist(err) {
		t.Fatalf("expected real write failure, got %v", err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	// No new mutation: another waiter must retry this exact failed generation.
	registry.persistAliases()
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["entry"] != "steps/entry/out" {
		t.Fatalf("failed generation was treated as saved: %v", got)
	}
}
