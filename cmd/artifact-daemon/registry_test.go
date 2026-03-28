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
