package main_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func TestSweeper_RemovesExpiredArtifacts(t *testing.T) {
	storagePath := t.TempDir()
	artifactsDir := filepath.Join(storagePath, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an old file (mtime set to 3 hours ago)
	oldFile := filepath.Join(artifactsDir, "old.tar")
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(oldFile, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	// Create a fresh file
	freshFile := filepath.Join(artifactsDir, "fresh.tar")
	if err := os.WriteFile(freshFile, []byte("fresh"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

	sweeper.SweepOnce()

	// Old file should be gone
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("expected old file to be removed")
	}

	// Fresh file should remain
	if _, err := os.Stat(freshFile); err != nil {
		t.Error("expected fresh file to still exist")
	}
}

func TestSweeper_RemovesExpiredLegacyFlatFiles(t *testing.T) {
	storagePath := t.TempDir()
	artifactsDir := filepath.Join(storagePath, "artifacts")
	os.MkdirAll(artifactsDir, 0755)

	// Old legacy tar file at the top level of /artifacts/
	oldFile := filepath.Join(artifactsDir, "legacy.tar")
	os.WriteFile(oldFile, []byte("data"), 0644)
	os.Chtimes(oldFile, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

	sweeper.SweepOnce()

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("expected expired legacy flat file to be removed")
	}
}

func TestSweeper_NoArtifactsDir(t *testing.T) {
	storagePath := t.TempDir()
	// Don't create artifacts dir — sweep should handle gracefully

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

	// Should not panic
	sweeper.SweepOnce()
}

// Phase 6: Build-aware sweeper tests

func TestSweeper_SkipsCachesDirectory(t *testing.T) {
	storagePath := t.TempDir()

	// Create an old cache directory
	cacheDir := filepath.Join(storagePath, "artifacts", "caches", "job-1-build-abc")
	os.MkdirAll(cacheDir, 0755)
	os.WriteFile(filepath.Join(cacheDir, "data"), []byte("cached"), 0644)
	os.Chtimes(cacheDir, time.Now().Add(-5*time.Hour), time.Now().Add(-5*time.Hour))

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

	sweeper.SweepOnce()

	// Cache should NOT be removed (only steps/ is swept)
	if _, err := os.Stat(filepath.Join(cacheDir, "data")); err != nil {
		t.Error("expected cache directory to survive sweep")
	}
}

func TestSweeper_RemovesExpiredStepDirectories(t *testing.T) {
	storagePath := t.TempDir()

	// Create an old step directory
	oldStep := filepath.Join(storagePath, "artifacts", "steps", "old-handle")
	os.MkdirAll(filepath.Join(oldStep, "output"), 0755)
	os.WriteFile(filepath.Join(oldStep, "output", "file.txt"), []byte("x"), 0644)
	os.Chtimes(oldStep, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	// Create a fresh step directory
	freshStep := filepath.Join(storagePath, "artifacts", "steps", "fresh-handle")
	os.MkdirAll(filepath.Join(freshStep, "output"), 0755)
	os.WriteFile(filepath.Join(freshStep, "output", "file.txt"), []byte("y"), 0644)

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

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
