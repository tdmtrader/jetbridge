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

func TestSweeper_RemovesExpiredNestedArtifacts(t *testing.T) {
	storagePath := t.TempDir()
	nestedDir := filepath.Join(storagePath, "artifacts", "caches", "job-1")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldFile := filepath.Join(nestedDir, "build.tar")
	if err := os.WriteFile(oldFile, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(oldFile, time.Now().Add(-3*time.Hour), time.Now().Add(-3*time.Hour))

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute)

	sweeper.SweepOnce()

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("expected nested old file to be removed")
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
