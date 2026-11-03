package outputplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A Run result is usually a tree, not a flat directory: the landing queue's
// compose publishes a whole repository checkout. The canonicalizer refuses an
// archive path with a trailing separator, so the daemon's own tar of a sealed
// directory must name each directory without one, or every capture holding a
// subdirectory stays pending forever. concourse.home's first compose Run found it.
func TestASealedDirectoryWithSubdirectoriesCanonicalizes(t *testing.T) {
	fixture := newCaptureLedger(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(root, "atc", "db", "build.go"), "package db")
	writeFile(t, filepath.Join(root, "README.md"), "jetbridge")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	captured, err := fixture.daemon.CanonicalizeDirectory(context.Background(), root)
	if err != nil {
		t.Fatalf("canonicalizing a tree with subdirectories: %v", err)
	}
	defer captured.Close()

	body, err := os.ReadFile(filepath.Join(captured.Root, "atc", "db", "build.go"))
	if err != nil || string(body) != "package db" {
		t.Errorf("the captured tree's atc/db/build.go reads %q, %v", body, err)
	}
	if info, err := os.Stat(filepath.Join(captured.Root, "empty")); err != nil || !info.IsDir() {
		t.Errorf("the captured tree lost the empty directory: %v", err)
	}
}
