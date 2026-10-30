package main_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// The sweep takes step directories and nothing else. A file under the root --
// however old -- is not the sweeper's to remove: nothing the artifact daemon serves is
// a file, so one that is there was planted or is the store's own record.
func TestSweeper_RemovesExpiredStepDirectoriesAndLeavesFilesAlone(t *testing.T) {
	storagePath := t.TempDir()
	expired := expiredStepDir(t, storagePath, "expired-handle")

	fresh := filepath.Join(storagePath, "steps", "fresh-handle")
	if err := os.MkdirAll(fresh, 0755); err != nil {
		t.Fatal(err)
	}

	artifactsDir := filepath.Join(storagePath, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(artifactsDir, "old.tar")
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(oldFile, old, old); err != nil {
		t.Fatal(err)
	}

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, nil)

	sweeper.SweepOnce()

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Errorf("the expired step directory was not removed (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the fresh step directory was removed: %v", err)
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Errorf("a file under the root was swept; the sweep takes step directories only: %v", err)
	}
}

func TestSweeper_NoArtifactsDir(t *testing.T) {
	storagePath := t.TempDir()
	// Don't create artifacts dir — sweep should handle gracefully

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, nil)

	// Should not panic
	sweeper.SweepOnce()
}

func TestSweeper_SkipsCachesDirectory(t *testing.T) {
	storagePath := t.TempDir()

	// Create an old cache directory
	cacheDir := filepath.Join(storagePath, "artifacts", "caches", "job-1-build-abc")
	os.MkdirAll(cacheDir, 0755)
	os.WriteFile(filepath.Join(cacheDir, "data"), []byte("cached"), 0644)
	os.Chtimes(cacheDir, time.Now().Add(-5*time.Hour), time.Now().Add(-5*time.Hour))

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, nil)

	sweeper.SweepOnce()

	// Cache should NOT be removed (only steps/ is swept)
	if _, err := os.Stat(filepath.Join(cacheDir, "data")); err != nil {
		t.Error("expected cache directory to survive sweep")
	}
}

func TestSweeper_RemovesExpiredStepDirectories(t *testing.T) {
	storagePath := t.TempDir()

	// Create an old step directory (under steps/, not artifacts/steps/)
	oldStep := filepath.Join(storagePath, "steps", "old-handle")
	os.MkdirAll(filepath.Join(oldStep, "output"), 0755)
	os.WriteFile(filepath.Join(oldStep, "output", "file.txt"), []byte("x"), 0644)
	os.Chtimes(oldStep, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	// Create a fresh step directory
	freshStep := filepath.Join(storagePath, "steps", "fresh-handle")
	os.MkdirAll(filepath.Join(freshStep, "output"), 0755)
	os.WriteFile(filepath.Join(freshStep, "output", "file.txt"), []byte("y"), 0644)

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, nil)

	sweeper.SweepOnce()

	// Old step directory should be removed
	if _, err := os.Stat(oldStep); !os.IsNotExist(err) {
		t.Error("expected old step directory to be removed")
	}

	// Fresh step directory should remain
	if _, err := os.Stat(freshStep); err != nil {
		t.Error("expected fresh step directory to remain")
	}
}

func TestSweeper_CleansUpAliasesOnRemove(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("sweeper")

	// Create a step directory that will expire.
	oldStep := filepath.Join(storagePath, "steps", "container-abc")
	resultDir := filepath.Join(oldStep, "result")
	os.MkdirAll(resultDir, 0755)
	os.WriteFile(filepath.Join(resultDir, "data.txt"), []byte("hello"), 0644)
	os.Chtimes(oldStep, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	// Set up registry with alias store and register an alias pointing into that step dir.
	registry := daemon.NewRegistry(logger, storagePath)
	rootHandle, err := os.OpenRoot(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	aliasStore := daemon.NewAliasStore(logger, storagePath, rootHandle)
	registry.SetAliasStore(aliasStore)
	if _, err := registry.RegisterAlias("vol-cached-xyz", resultDir); err != nil {
		t.Fatal(err)
	}

	// Verify alias is there.
	if _, ok := registry.Lookup("vol-cached-xyz"); !ok {
		t.Fatal("alias should exist before sweep")
	}

	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, registry)
	sweeper.SweepOnce()

	// Step directory should be removed.
	if _, err := os.Stat(oldStep); !os.IsNotExist(err) {
		t.Error("expected old step to be removed")
	}

	// Alias should be cleaned up from registry.
	if _, ok := registry.Lookup("vol-cached-xyz"); ok {
		t.Error("expected alias to be removed after sweep")
	}

	// Verify the alias file is also updated (load into fresh registry).
	r2 := daemon.NewRegistry(logger, storagePath)
	r2.SetAliasStore(aliasStore)
	r2.LoadAliases()
	if _, ok := r2.Lookup("vol-cached-xyz"); ok {
		t.Error("expected alias to be gone from persisted file after sweep")
	}
}
