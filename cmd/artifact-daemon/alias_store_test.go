package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// newStoreAt returns a store and the root its values are relative to. The
// existence check now runs through the root HANDLE, so tests have to supply one
// — with a nil handle Load keeps everything and the staleness tests below would
// pass without exercising anything.
func newStoreAt(t *testing.T) (*daemon.AliasStore, string) {
	t.Helper()
	dir := t.TempDir()
	handle, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	return daemon.NewAliasStore(lagertest.NewTestLogger("alias-store"), dir, handle), dir
}

func TestAliasStore_SaveAndLoad(t *testing.T) {
	store, dir := newStoreAt(t)

	// Create real directories for the alias paths.
	path1 := filepath.Join(dir, "steps", "container-abc", "result")
	path2 := filepath.Join(dir, "steps", "container-def", "output")
	os.MkdirAll(path1, 0755)
	os.MkdirAll(path2, 0755)

	aliases := map[string]daemon.RelKey{
		"vol-abc123": "steps/container-abc/result",
		"vol-def456": "steps/container-def/output",
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
	if loaded["vol-abc123"] != "steps/container-abc/result" {
		t.Errorf("vol-abc123: got %q", loaded["vol-abc123"])
	}
	if loaded["vol-def456"] != "steps/container-def/output" {
		t.Errorf("vol-def456: got %q", loaded["vol-def456"])
	}
}

func TestAliasStore_LoadSkipsStaleEntries(t *testing.T) {
	store, dir := newStoreAt(t)

	// Create only one of the two paths.
	validPath := filepath.Join(dir, "steps", "container-abc", "result")
	os.MkdirAll(validPath, 0755)

	// The stale entry is now a location INSIDE the root that does not exist.
	// It used to be "/nonexistent/…", which under the new rule is refused for
	// being outside the root before staleness is ever considered — so the test
	// would still have gone green while testing a different rejection.
	aliases := map[string]daemon.RelKey{
		"vol-valid": "steps/container-abc/result",
		"vol-stale": "steps/container-gone/result",
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
	store, _ := newStoreAt(t)

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected empty map, got %d entries", len(loaded))
	}
}

func TestAliasStore_AtomicWrite(t *testing.T) {
	store, dir := newStoreAt(t)

	path1 := filepath.Join(dir, "steps", "a", "b")
	os.MkdirAll(path1, 0755)

	if err := store.Save(map[string]daemon.RelKey{"k": "steps/a/b"}); err != nil {
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
	store, dir := newStoreAt(t)

	os.WriteFile(filepath.Join(dir, "aliases.json"), []byte("not valid json"), 0644)

	_, err := store.Load()
	if err == nil {
		t.Error("expected error on corrupted file")
	}
}

// An aliases.json written by an earlier version holds ABSOLUTE paths. A node
// keeps its alias file across the upgrade that changes the format, so refusing
// to read the old form would drop every cache-hit alias on first boot — and
// present as a cold cache rather than as a failure.
func TestAliasStore_LoadAcceptsTheLegacyAbsoluteForm(t *testing.T) {
	store, dir := newStoreAt(t)

	inside := filepath.Join(dir, "steps", "legacy", "result")
	os.MkdirAll(inside, 0755)

	// Written by hand in the old format, not through Save — Save can no longer
	// produce it.
	legacy := map[string]string{
		"vol-legacy":  inside,
		"vol-escaped": "/etc",
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aliases.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded["vol-legacy"]; got != "steps/legacy/result" {
		t.Errorf("a legacy absolute value was not relativized: got %q", got)
	}
	if _, ok := loaded["vol-escaped"]; ok {
		t.Error("a persisted path outside the storage root was restored — " +
			"aliases.json is exactly where a pre-containment entry survives")
	}
}
