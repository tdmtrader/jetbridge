package snapshot

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCanonicalCaptureRoundTripsThroughMaterializedDisk is deliberately NOT
// TestCanonicalCaptureRoundTrip, and the difference is the whole point.
//
// That test re-feeds the canonical ARCHIVE BYTES to Capture. It proves the
// serializer has a fixed point and it never touches a filesystem. The property
// an agent workspace actually rests on is this one: a snapshot is materialized
// to disk, a step re-tars that directory the way any producer would, the result
// is sealed, and the digest comes back unchanged. Everything between the two
// digests here is real: real extraction into a real directory, a real
// filepath.WalkDir, real lstat modes and real readlink targets.
//
// The negative half is the same property from the other side, and it is a
// different property from the hostile-archive tests: those assert that Capture
// returns an ERROR. This asserts that content the canonicalizer happily accepts
// still moves the digest. The flipped byte keeps the file the same length, so
// the tree's byte size is identical and only content can be doing the work.
func TestCanonicalCaptureRoundTripsThroughMaterializedDisk(t *testing.T) {
	t.Parallel()

	raw := makeTar(t, []tarEntry{
		{name: "empty", typeflag: tar.TypeDir, mode: 0700},
		{name: "bin", typeflag: tar.TypeDir, mode: 0755},
		{name: "bin/run", typeflag: tar.TypeReg, mode: 0100, content: "#!/bin/sh\n"},
		{name: "données", typeflag: tar.TypeDir, mode: 0755},
		{name: "données/café.txt", typeflag: tar.TypeReg, mode: 0640, content: "bonjour\n"},
		{name: "données/latest", typeflag: tar.TypeSymlink, linkname: "./café.txt"},
		{name: "bin/data", typeflag: tar.TypeSymlink, linkname: "../données/café.txt"},
	})

	original := capture(t, Canonicalizer{}, bytes.NewReader(raw))
	defer original.Close()

	// The real extraction path. Capture materializes the canonical archive into
	// its own private root using the same code a step mount is fed from; there is
	// no second, test-only extractor.
	materialized := capture(t, Canonicalizer{}, bytes.NewReader(readFile(t, original.ArchivePath)))
	defer materialized.Close()

	fromDisk := capture(t, Canonicalizer{}, bytes.NewReader(tarMaterializedDirectory(t, materialized.Root)))
	defer fromDisk.Close()

	if fromDisk.Digest != original.Digest {
		t.Fatalf("re-seal of the materialized tree = %s, want %s", fromDisk.Digest, original.Digest)
	}
	if !bytes.Equal(readFile(t, fromDisk.ArchivePath), readFile(t, original.ArchivePath)) {
		t.Fatal("re-seal of the materialized tree produced different canonical bytes")
	}
	if fromDisk.ByteSize != original.ByteSize || fromDisk.FileCount != original.FileCount {
		t.Fatalf("re-seal identity = %d bytes / %d entries, want %d / %d",
			fromDisk.ByteSize, fromDisk.FileCount, original.ByteSize, original.FileCount)
	}

	// One byte, same length, inside one file, on disk.
	target := filepath.Join(materialized.Root, "données", "café.txt")
	if err := os.WriteFile(target, []byte("bonjouR\n"), 0644); err != nil {
		t.Fatalf("flip one byte on disk: %v", err)
	}
	tampered := capture(t, Canonicalizer{}, bytes.NewReader(tarMaterializedDirectory(t, materialized.Root)))
	defer tampered.Close()

	if tampered.Digest == original.Digest {
		t.Fatalf("a one-byte change on disk left the digest at %s", tampered.Digest)
	}
	if tampered.ByteSize != original.ByteSize || tampered.FileCount != original.FileCount {
		t.Fatalf("the tampered tree changed size (%d bytes / %d entries vs %d / %d); the digest must move on content alone",
			tampered.ByteSize, tampered.FileCount, original.ByteSize, original.FileCount)
	}
}

// tarMaterializedDirectory re-tars an extracted tree the way an ordinary
// producer would: walk it, sort by POSIX path, and emit whatever the filesystem
// reports. It deliberately normalizes nothing of its own — no mode rewriting, no
// time zeroing, no ownership. The round trip is only meaningful if Capture, and
// not this helper, owns identity.
//
// FormatGNU is pinned for the same reason the canonical writer pins it: the
// tree carries a non-ASCII path, and letting archive/tar pick a format would
// make this helper's output depend on the entry set rather than on the tree.
func tarMaterializedDirectory(t *testing.T, root string) []byte {
	t.Helper()

	var names []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk materialized tree %q: %v", root, walkErr)
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("stat %q: %v", full, err)
		}
		header := &tar.Header{
			Name:   name,
			Mode:   int64(info.Mode().Perm()),
			Format: tar.FormatGNU,
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				t.Fatalf("read symlink %q: %v", full, err)
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = link
		case info.IsDir():
			header.Typeflag = tar.TypeDir
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
		default:
			t.Fatalf("unsupported file mode %v for %q", info.Mode(), name)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		file, err := os.Open(full)
		if err != nil {
			t.Fatalf("open %q: %v", full, err)
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("copy %q: %v", name, copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close %q: %v", name, closeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close re-tar writer: %v", err)
	}
	return buffer.Bytes()
}
