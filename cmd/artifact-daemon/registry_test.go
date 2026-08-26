package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// newRegistryAt returns a registry and the storage root its values are relative
// to.
//
// Every test in this file used to register invented absolute paths — "/data/…",
// "/old/path", "/a" — that existed nowhere. Register now REFUSES anything that
// does not lie within the storage root, and that refusal is the point of the
// change, so the fixtures have to be real locations under a real root.
func newRegistryAt(t *testing.T) (*daemon.Registry, string) {
	t.Helper()
	root := t.TempDir()
	return daemon.NewRegistry(lagertest.NewTestLogger("registry"), root), root
}

// mkStep creates a step output directory under root and returns its absolute
// path.
func mkStep(t *testing.T, root string, segments ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, segments...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r, root := newRegistryAt(t)
	disk := mkStep(t, root, "steps", "abc", "result")

	if _, err := r.Register("vol-abc", disk); err != nil {
		t.Fatal(err)
	}

	rel, ok := r.Lookup("vol-abc")
	if !ok {
		t.Fatal("expected to find registered key")
	}
	// Lookup answers in the relative form. The absolute one is available, but
	// only through the name that says what it costs.
	if rel != "steps/abc/result" {
		t.Errorf("expected steps/abc/result, got %s", rel)
	}
	abs, ok := r.LookupAmbientPath("vol-abc")
	if !ok || abs != disk {
		t.Errorf("expected ambient path %q, got %q (found=%v)", disk, abs, ok)
	}
}

// Register refuses a path outside the storage root instead of storing it and
// leaving every consumer to re-check. This is the behaviour the whole
// representation change exists to make possible.
func TestRegistry_RegisterRefusesOutsideTheRoot(t *testing.T) {
	r, root := newRegistryAt(t)
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"a sibling of the root", outside},
		{"a traversal out of the root", filepath.Join(root, "steps", "..", "..", "etc")},
		{"the root itself", root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Register("vol-bad", tc.path); err == nil {
				t.Fatalf("Register accepted %q", tc.path)
			}
			if _, ok := r.Lookup("vol-bad"); ok {
				t.Error("a refused path was stored anyway")
			}
		})
	}
}

func TestRegistry_LookupMissing(t *testing.T) {
	r, _ := newRegistryAt(t)

	_, ok := r.Lookup("nonexistent")
	if ok {
		t.Error("expected lookup to return false for missing key")
	}
}

func TestRegistry_Remove(t *testing.T) {
	r, root := newRegistryAt(t)
	disk := mkStep(t, root, "steps", "xyz", "dir")

	if _, err := r.Register("vol-xyz", disk); err != nil {
		t.Fatal(err)
	}
	r.Remove("vol-xyz")

	_, ok := r.Lookup("vol-xyz")
	if ok {
		t.Error("expected key to be removed")
	}
}

func TestRegistry_OverwriteExisting(t *testing.T) {
	r, root := newRegistryAt(t)
	old := mkStep(t, root, "steps", "one", "old")
	updated := mkStep(t, root, "steps", "one", "new")

	if _, err := r.Register("vol-1", old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("vol-1", updated); err != nil {
		t.Fatal(err)
	}

	rel, ok := r.Lookup("vol-1")
	if !ok {
		t.Fatal("expected to find key")
	}
	if rel != "steps/one/new" {
		t.Errorf("expected steps/one/new, got %s", rel)
	}
}

func TestRegistry_Len(t *testing.T) {
	r, root := newRegistryAt(t)

	if r.Len() != 0 {
		t.Errorf("expected 0, got %d", r.Len())
	}

	if _, err := r.Register("a", mkStep(t, root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("b", mkStep(t, root, "b")); err != nil {
		t.Fatal(err)
	}

	if r.Len() != 2 {
		t.Errorf("expected 2, got %d", r.Len())
	}
}

func TestRegistry_ScanHostPath(t *testing.T) {
	r, storagePath := newRegistryAt(t)

	stepsDir := filepath.Join(storagePath, "steps")

	// Handle abc with two outputs
	mkStep(t, storagePath, "steps", "handle-abc", "result")
	mkStep(t, storagePath, "steps", "handle-abc", "logs")

	// Handle def with one output
	mkStep(t, storagePath, "steps", "handle-def", "dir")

	// A non-directory file in steps/ (should be skipped)
	if err := os.WriteFile(filepath.Join(stepsDir, "stale-file.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath: %v", err)
	}

	if r.Len() != 3 {
		t.Errorf("expected 3 registered artifacts, got %d (keys: %v)", r.Len(), r.Keys())
	}

	// The scan registers the relative form. It used to register the absolute
	// one, and that value went straight into the guard key.
	rel, ok := r.Lookup("handle-abc/result")
	if !ok {
		t.Error("expected handle-abc/result to be registered")
	}
	if rel != "steps/handle-abc/result" {
		t.Errorf("unexpected location: %s", rel)
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
	r1, dir := newRegistryAt(t)
	logger := lagertest.NewTestLogger("registry")

	handle, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	// Create real directory for the alias path.
	diskPath := mkStep(t, dir, "steps", "container-abc", "result")

	// Registry 1: register an alias and verify it persists.
	store := daemon.NewAliasStore(logger, dir, handle)
	r1.SetAliasStore(store)

	if _, err := r1.RegisterAlias("vol-handle-xyz", diskPath); err != nil {
		t.Fatal(err)
	}

	// Registry 2: load from the same store and verify the alias is there.
	r2 := daemon.NewRegistry(logger, dir)
	r2.SetAliasStore(store)
	if err := r2.LoadAliases(); err != nil {
		t.Fatalf("LoadAliases: %v", err)
	}

	rel, ok := r2.Lookup("vol-handle-xyz")
	if !ok {
		t.Fatal("expected alias to be loaded from disk")
	}
	if rel != "steps/container-abc/result" {
		t.Errorf("expected steps/container-abc/result, got %q", rel)
	}
	// The round trip through disk must still name the same place.
	if abs, _ := r2.LookupAmbientPath("vol-handle-xyz"); abs != diskPath {
		t.Errorf("expected %q, got %q", diskPath, abs)
	}
}

func TestRegistry_RemoveByPath(t *testing.T) {
	r, dir := newRegistryAt(t)
	logger := lagertest.NewTestLogger("registry")

	handle, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	path1 := mkStep(t, dir, "steps", "abc", "result")
	path2 := mkStep(t, dir, "steps", "abc", "logs")
	path3 := mkStep(t, dir, "steps", "def", "output")

	store := daemon.NewAliasStore(logger, dir, handle)
	r.SetAliasStore(store)

	for key, p := range map[string]string{"vol-1": path1, "vol-2": path2, "vol-3": path3} {
		if _, err := r.RegisterAlias(key, p); err != nil {
			t.Fatal(err)
		}
	}

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
	r2 := daemon.NewRegistry(logger, dir)
	r2.SetAliasStore(store)
	if err := r2.LoadAliases(); err != nil {
		t.Fatal(err)
	}
	if r2.Len() != 1 {
		t.Errorf("expected 1 persisted alias, got %d", r2.Len())
	}
}

func TestRegistry_RemoveAlsoUpdatesAliasFile(t *testing.T) {
	r, dir := newRegistryAt(t)
	logger := lagertest.NewTestLogger("registry")

	handle, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	diskPath := mkStep(t, dir, "steps", "abc", "result")

	store := daemon.NewAliasStore(logger, dir, handle)
	r.SetAliasStore(store)

	if _, err := r.RegisterAlias("vol-abc", diskPath); err != nil {
		t.Fatal(err)
	}
	r.Remove("vol-abc")

	// Load fresh and verify it's gone.
	r2 := daemon.NewRegistry(logger, dir)
	r2.SetAliasStore(store)
	if err := r2.LoadAliases(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Lookup("vol-abc"); ok {
		t.Error("expected vol-abc to be removed from persisted aliases")
	}
}

func TestRegistry_ScanHostPath_EmptyDir(t *testing.T) {
	r, storagePath := newRegistryAt(t)
	// No artifacts/steps/ directory at all.

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath on empty dir: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("expected 0, got %d", r.Len())
	}
}
