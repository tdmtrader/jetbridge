package main

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestOsRootBlocksSymlinkEscape is the evidence the symlink policy rests on.
//
// Targets are no longer judged by their text, so the guarantee that nothing
// escapes has to come from somewhere else: os.Root refuses to resolve any path
// that leaves the root, whatever a symlink in the way points at. If this ever
// stops holding, relaxing the target checks stops being safe — so it is
// asserted here rather than assumed from the documentation.
func TestOsRootBlocksSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	inside := filepath.Join(base, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}

	if err := os.Symlink("/etc", filepath.Join(inside, "abs")); err != nil {
		t.Fatalf("symlink abs: %v", err)
	}
	if err := os.Symlink("../outside", filepath.Join(inside, "rel")); err != nil {
		t.Fatalf("symlink rel: %v", err)
	}

	root, err := os.OpenRoot(inside)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	for _, escape := range []string{"abs/hosts", "rel/planted.txt"} {
		if _, err := root.OpenFile(escape, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			t.Errorf("os.Root allowed a write through %q; the symlink policy depends on this failing", escape)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a file was created outside the root")
	}
}

// TestExtractTarAnchored_SymlinkCannotEscape is the adversarial half: an
// archive that plants a symlink and then writes through it, which is the
// classic tar traversal. Extraction must not place anything outside the root,
// whether the symlink target is absolute or relative.
func TestExtractTarAnchored_SymlinkCannotEscape(t *testing.T) {
	for _, tc := range []struct{ name, target string }{
		{"absolute", "/tmp"},
		{"relative", "../../escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "escape")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			dest := filepath.Join(base, "root")
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: tc.target, Mode: 0o777})
			tw.WriteHeader(&tar.Header{Name: "link/owned.txt", Typeflag: tar.TypeReg, Size: 5, Mode: 0o644})
			tw.Write([]byte("owned"))
			tw.Close()

			root, err := os.OpenRoot(dest)
			if err != nil {
				t.Fatalf("open root: %v", err)
			}
			defer root.Close()

			// The archive may be rejected or the write contained; either is
			// acceptable. What must never happen is a file landing outside.
			_ = extractTarAnchored(context.Background(), root, &buf)

			if _, err := os.Stat(filepath.Join(outside, "owned.txt")); err == nil {
				t.Fatal("extraction wrote through a symlink and escaped the root")
			}
			if _, err := os.Stat(filepath.Join("/tmp", "owned.txt")); err == nil {
				os.Remove(filepath.Join("/tmp", "owned.txt"))
				t.Fatal("extraction wrote through an absolute symlink into /tmp")
			}
		})
	}
}

// TestCopyOpenedTree_PreservesAbsoluteSymlink covers the symlinks every
// Debian-derived container rootfs carries — /var/spool/mail -> /var/mail is the
// one that surfaced. Rejecting them meant the daemon could not serve a rootfs
// artifact at all: the tree was accepted into its store and then failed every
// resolve, reaching the caller only as an opaque 500 from /resolve-batch.
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

	link := filepath.Join(dst, "rootfs", "var", "spool", "mail")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink copied symlink: %v", err)
	}
	if got != "/var/mail" {
		t.Errorf("symlink target = %q, want %q (copied verbatim)", got, "/var/mail")
	}
	info, err := os.Lstat(link)
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

// TestTarOpenedDirectory_EmitsAbsoluteSymlink covers the mirror and serve
// paths. These are what replicate an artifact to a peer node and stream it to
// a client, and they were still refusing absolute targets after the copy path
// stopped — so a rootfs could be resolved locally but never replicated. The
// chart defaults agentSnapshots.replicationFactor to 2 while the behavioral
// suite runs it at 1, so no test exercised the difference.
func TestTarOpenedDirectory_EmitsAbsoluteSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink("/bin/busybox", filepath.Join(src, "bin", "ls")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dir, err := os.Open(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer dir.Close()

	var buf bytes.Buffer
	if err := tarOpenedDirectory(&buf, dir); err != nil {
		t.Fatalf("tarring a tree with an absolute symlink must succeed, got: %v", err)
	}

	tr := tar.NewReader(&buf)
	var found bool
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasSuffix(hdr.Name, "bin/ls") {
			found = true
			if hdr.Typeflag != tar.TypeSymlink {
				t.Errorf("bin/ls typeflag = %v, want symlink", hdr.Typeflag)
			}
			if hdr.Linkname != "/bin/busybox" {
				t.Errorf("bin/ls linkname = %q, want %q", hdr.Linkname, "/bin/busybox")
			}
		}
	}
	if !found {
		t.Error("bin/ls was not emitted into the archive")
	}
}

// TestArchiveSymlinkRoundTrip pins the property that motivated one shared rule:
// anything the daemon will emit, it will also ingest. Divergent rules left it
// unable to re-ingest its own mirror stream.
func TestArchiveSymlinkRoundTrip(t *testing.T) {
	for _, target := range []string{"/var/mail", "/bin/busybox", "sibling", "../peer", "../../far"} {
		if err := validateArchiveSymlink("some/entry", target); err != nil {
			t.Errorf("extraction rejects %q which tarring emits: %v", target, err)
		}
		if err := validateReproducibleSymlink("some/entry", target); err != nil {
			t.Errorf("tarring rejects %q: %v", target, err)
		}
	}
	for _, target := range []string{"", "bad\x00target"} {
		if err := validateReproducibleSymlink("some/entry", target); err == nil {
			t.Errorf("unrepresentable target %q should be rejected", target)
		}
	}
}
