package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// openTreePair opens src and dst as *os.File handles for copyOpenedTree.
func openTreePair(t *testing.T, src, dst string) (*os.File, *os.File) {
	t.Helper()
	srcFile, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	t.Cleanup(func() { srcFile.Close() })
	dstFile, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	t.Cleanup(func() { dstFile.Close() })
	return srcFile, dstFile
}

// TestCopyOpenedTree_PreservesAbsoluteSymlink covers the symlinks every
// Debian-derived container rootfs carries — /var/spool/mail -> /var/mail is
// the one that surfaced. Rejecting them meant the daemon could not serve a
// rootfs artifact at all: the tree was accepted into its store and then failed
// every resolve with "unsafe artifact symlink", which reached the caller only
// as an opaque 500 from /resolve-batch.
//
// Copying is safe because the entry is recreated with Symlinkat and never
// dereferenced; the target is inert data at copy time. The walk descends only
// into real source directories, so it cannot be steered through a symlink it
// just created.
func TestCopyOpenedTree_PreservesAbsoluteSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "rootfs", "var", "spool"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink("/var/mail", filepath.Join(src, "rootfs", "var", "spool", "mail")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "rootfs", "var", "regular.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	srcFile, dstFile := openTreePair(t, src, dst)
	if err := copyOpenedTree(context.Background(), srcFile, dstFile, ""); err != nil {
		t.Fatalf("copying a rootfs with an absolute symlink must succeed, got: %v", err)
	}

	got, err := os.Readlink(filepath.Join(dst, "rootfs", "var", "spool", "mail"))
	if err != nil {
		t.Fatalf("readlink copied symlink: %v", err)
	}
	if got != "/var/mail" {
		t.Errorf("symlink target = %q, want %q (copied verbatim)", got, "/var/mail")
	}

	// The symlink must be reproduced as a symlink, not followed and flattened.
	info, err := os.Lstat(filepath.Join(dst, "rootfs", "var", "spool", "mail"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("copied entry is not a symlink — the target was dereferenced")
	}

	body, err := os.ReadFile(filepath.Join(dst, "rootfs", "var", "regular.txt"))
	if err != nil || string(body) != "data" {
		t.Errorf("regular file alongside the symlink: %q, %v", body, err)
	}
}

// TestCopyOpenedTree_StillRejectsEscapingRelativeSymlink pins that relaxing the
// absolute case does not relax the relative one. Allowing absolute targets is
// the approved, minimal change; the existing guard on relative targets that
// climb out of the tree stays exactly as it was.
func TestCopyOpenedTree_StillRejectsEscapingRelativeSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink("../../../etc/passwd", filepath.Join(src, "nested", "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	srcFile, dstFile := openTreePair(t, src, dst)
	if err := copyOpenedTree(context.Background(), srcFile, dstFile, ""); err == nil {
		t.Fatal("a relative symlink climbing out of the tree must still be rejected")
	}
}

// TestValidateCopyableSymlink covers the predicate directly, including the
// targets that remain unrepresentable regardless of the absolute-path change.
func TestValidateCopyableSymlink(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		wantErr      bool
	}{
		{"rootfs/var/spool/mail", "/var/mail", false},
		{"rootfs/bin/sh", "/bin/bash", false},
		{"nested/sibling", "other", false},
		{"nested/parent", "../peer", false},
		{"nested/escape", "../../etc/passwd", true},
		{"top", "..", true},
		{"empty", "", true},
		{"nul", "bad\x00target", true},
	} {
		t.Run(tc.name+"->"+tc.target, func(t *testing.T) {
			err := validateCopyableSymlink(tc.name, tc.target)
			if tc.wantErr && err == nil {
				t.Errorf("target %q should be rejected", tc.target)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("target %q should be allowed, got: %v", tc.target, err)
			}
		})
	}
}
