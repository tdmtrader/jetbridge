package main

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeTar builds an archive from (name, typeflag, body) triples in order.
func writeTar(t *testing.T, entries [][3]any) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		name := entry[0].(string)
		flag := entry[1].(byte)
		body := entry[2].(string)
		hdr := &tar.Header{Name: name, Typeflag: flag, Mode: 0o644}
		if flag == tar.TypeReg {
			hdr.Size = int64(len(body))
		} else {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("write body %q: %v", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return &buf
}

// TestExtractTarAnchored_AcceptsDotSlashPrefixedArchive covers the archive shape
// `tar -C <dir> .` produces, which is what fly execute uploads (getFiles returns
// ["."] for --include-ignored) and what volume streaming emits: a "./" root
// entry followed by "./"-prefixed members.
//
// The daemon rejected the very first entry, so stream-in answered 400 with
// `unsafe tar path "./": key contains an unsafe path segment`, the artifact was
// never stored, and every later resolve of that key failed.
func TestExtractTarAnchored_AcceptsDotSlashPrefixedArchive(t *testing.T) {
	archive := writeTar(t, [][3]any{
		{"./", byte(tar.TypeDir), ""},
		{"./file.txt", byte(tar.TypeReg), "hello"},
		{"./sub/", byte(tar.TypeDir), ""},
		{"./sub/nested.txt", byte(tar.TypeReg), "nested"},
	})

	dest := t.TempDir()
	root, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := extractTarAnchored(context.Background(), root, archive); err != nil {
		t.Fatalf("extracting a `tar -C dir .` archive must succeed, got: %v", err)
	}

	for name, want := range map[string]string{
		"file.txt":       "hello",
		"sub/nested.txt": "nested",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%q = %q, want %q", name, got, want)
		}
	}
}

// TestExtractTarAnchored_StillRejectsEscapingPaths pins the security property
// that accepting "./" must not relax: nothing may resolve outside the root,
// including paths that only escape after their interior ".." is collapsed.
func TestExtractTarAnchored_StillRejectsEscapingPaths(t *testing.T) {
	for _, name := range []string{
		"../escape.txt",
		"./../escape.txt",
		"sub/../../escape.txt",
		"/absolute.txt",
		"..",
	} {
		t.Run(name, func(t *testing.T) {
			archive := writeTar(t, [][3]any{{name, byte(tar.TypeReg), "owned"}})

			dest := t.TempDir()
			root, err := os.OpenRoot(dest)
			if err != nil {
				t.Fatalf("open root: %v", err)
			}
			defer root.Close()

			if err := extractTarAnchored(context.Background(), root, archive); err == nil {
				t.Fatalf("tar path %q escapes the extraction root and must be rejected", name)
			}
		})
	}
}
