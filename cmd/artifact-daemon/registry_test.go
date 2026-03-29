package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	r.Register("vol-abc", "/data/steps/abc/result")

	path, ok := r.Lookup("vol-abc")
	if !ok {
		t.Fatal("expected to find registered key")
	}
	if path != "/data/steps/abc/result" {
		t.Errorf("expected /data/steps/abc/result, got %s", path)
	}
}

func TestRegistry_LookupMissing(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	_, ok := r.Lookup("nonexistent")
	if ok {
		t.Error("expected lookup to return false for missing key")
	}
}

func TestRegistry_Remove(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	r.Register("vol-xyz", "/data/steps/xyz/dir")
	r.Remove("vol-xyz")

	_, ok := r.Lookup("vol-xyz")
	if ok {
		t.Error("expected key to be removed")
	}
}

func TestRegistry_OverwriteExisting(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	r.Register("vol-1", "/old/path")
	r.Register("vol-1", "/new/path")

	path, ok := r.Lookup("vol-1")
	if !ok {
		t.Fatal("expected to find key")
	}
	if path != "/new/path" {
		t.Errorf("expected /new/path, got %s", path)
	}
}

func TestRegistry_Len(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	if r.Len() != 0 {
		t.Errorf("expected 0, got %d", r.Len())
	}

	r.Register("a", "/a")
	r.Register("b", "/b")

	if r.Len() != 2 {
		t.Errorf("expected 2, got %d", r.Len())
	}
}

func TestRegistry_ScanHostPath(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	storagePath := t.TempDir()

	// Create step output directories mimicking real artifact layout.
	stepsDir := filepath.Join(storagePath, "steps")

	// Handle abc with two outputs
	os.MkdirAll(filepath.Join(stepsDir, "handle-abc", "result"), 0755)
	os.MkdirAll(filepath.Join(stepsDir, "handle-abc", "logs"), 0755)

	// Handle def with one output
	os.MkdirAll(filepath.Join(stepsDir, "handle-def", "dir"), 0755)

	// A non-directory file in steps/ (should be skipped)
	os.WriteFile(filepath.Join(stepsDir, "stale-file.tmp"), []byte("x"), 0644)

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath: %v", err)
	}

	if r.Len() != 3 {
		t.Errorf("expected 3 registered artifacts, got %d (keys: %v)", r.Len(), r.Keys())
	}

	// Check specific registrations.
	path, ok := r.Lookup("handle-abc/result")
	if !ok {
		t.Error("expected handle-abc/result to be registered")
	}
	if path != filepath.Join(stepsDir, "handle-abc", "result") {
		t.Errorf("unexpected path: %s", path)
	}

	_, ok = r.Lookup("handle-abc/logs")
	if !ok {
		t.Error("expected handle-abc/logs to be registered")
	}

	_, ok = r.Lookup("handle-def/dir")
	if !ok {
		t.Error("expected handle-def/dir to be registered")
	}
}

func TestRegistry_RegisterAliasPersists(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	// Create real directory for the alias path.
	diskPath := filepath.Join(dir, "steps", "container-abc", "result")
	os.MkdirAll(diskPath, 0755)

	// Registry 1: register an alias and verify it persists.
	r1 := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r1.SetAliasStore(store)

	r1.RegisterAlias("vol-handle-xyz", diskPath)

	// Registry 2: load from the same store and verify the alias is there.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	if err := r2.LoadAliases(); err != nil {
		t.Fatalf("LoadAliases: %v", err)
	}

	path, ok := r2.Lookup("vol-handle-xyz")
	if !ok {
		t.Fatal("expected alias to be loaded from disk")
	}
	if path != diskPath {
		t.Errorf("expected %q, got %q", diskPath, path)
	}
}

func TestRegistry_RemoveByPath(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	path1 := filepath.Join(dir, "steps", "abc", "result")
	path2 := filepath.Join(dir, "steps", "abc", "logs")
	path3 := filepath.Join(dir, "steps", "def", "output")
	os.MkdirAll(path1, 0755)
	os.MkdirAll(path2, 0755)
	os.MkdirAll(path3, 0755)

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	r.RegisterAlias("vol-1", path1)
	r.RegisterAlias("vol-2", path2)
	r.RegisterAlias("vol-3", path3)

	// Remove all entries under steps/abc.
	r.RemoveByPath(filepath.Join(dir, "steps", "abc"))

	if _, ok := r.Lookup("vol-1"); ok {
		t.Error("vol-1 should have been removed")
	}
	if _, ok := r.Lookup("vol-2"); ok {
		t.Error("vol-2 should have been removed")
	}
	if _, ok := r.Lookup("vol-3"); !ok {
		t.Error("vol-3 should still exist")
	}

	// Verify persistence: only vol-3 should be in aliases.json.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	r2.LoadAliases()
	if r2.Len() != 1 {
		t.Errorf("expected 1 persisted alias, got %d", r2.Len())
	}
}

func TestRegistry_RemoveAlsoUpdatesAliasFile(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	diskPath := filepath.Join(dir, "steps", "abc", "result")
	os.MkdirAll(diskPath, 0755)

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	r.RegisterAlias("vol-abc", diskPath)
	r.Remove("vol-abc")

	// Load fresh and verify it's gone.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	r2.LoadAliases()
	if _, ok := r2.Lookup("vol-abc"); ok {
		t.Error("expected vol-abc to be removed from persisted aliases")
	}
}

func TestRegistry_ScanHostPath_EmptyDir(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	storagePath := t.TempDir()
	// No artifacts/steps/ directory at all.

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath on empty dir: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("expected 0, got %d", r.Len())
	}
}
