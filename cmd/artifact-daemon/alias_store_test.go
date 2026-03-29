package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestAliasStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	// Create real directories for the alias paths.
	path1 := filepath.Join(dir, "steps", "container-abc", "result")
	path2 := filepath.Join(dir, "steps", "container-def", "output")
	os.MkdirAll(path1, 0755)
	os.MkdirAll(path2, 0755)

	aliases := map[string]string{
		"vol-abc123": path1,
		"vol-def456": path2,
	}

	if err := store.Save(aliases); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists.
	aliasFile := filepath.Join(dir, "aliases.json")
	if _, err := os.Stat(aliasFile); err != nil {
		t.Fatalf("aliases.json not found: %v", err)
	}

	// Load and verify.
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 aliases, got %d", len(loaded))
	}
	if loaded["vol-abc123"] != path1 {
		t.Errorf("vol-abc123: expected %q, got %q", path1, loaded["vol-abc123"])
	}
	if loaded["vol-def456"] != path2 {
		t.Errorf("vol-def456: expected %q, got %q", path2, loaded["vol-def456"])
	}
}

func TestAliasStore_LoadSkipsStaleEntries(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	// Create only one of the two paths.
	validPath := filepath.Join(dir, "steps", "container-abc", "result")
	os.MkdirAll(validPath, 0755)

	aliases := map[string]string{
		"vol-valid": validPath,
		"vol-stale": "/nonexistent/path/that/does/not/exist",
	}

	if err := store.Save(aliases); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 valid alias, got %d", len(loaded))
	}
	if _, ok := loaded["vol-valid"]; !ok {
		t.Error("expected vol-valid to be loaded")
	}
	if _, ok := loaded["vol-stale"]; ok {
		t.Error("expected vol-stale to be skipped")
	}
}

func TestAliasStore_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected empty map, got %d entries", len(loaded))
	}
}

func TestAliasStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	path1 := filepath.Join(dir, "steps", "a", "b")
	os.MkdirAll(path1, 0755)

	if err := store.Save(map[string]string{"k": path1}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No .tmp file should remain.
	tmpFile := filepath.Join(dir, "aliases.json.tmp")
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("temp file should not exist after successful save")
	}

	// Verify JSON is valid.
	data, _ := os.ReadFile(filepath.Join(dir, "aliases.json"))
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("aliases.json is not valid JSON: %v", err)
	}
}

func TestAliasStore_CorruptedFile(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	os.WriteFile(filepath.Join(dir, "aliases.json"), []byte("not valid json"), 0644)

	_, err := store.Load()
	if err == nil {
		t.Error("expected error on corrupted file")
	}
}
