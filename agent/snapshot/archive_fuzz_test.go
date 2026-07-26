package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"testing"
)

// FuzzCanonicalCapture points the fuzzer at the parser this platform's security
// model rests on: arbitrary bytes arriving from a step's output mount, handed
// straight to the canonicalizer. TestExtractRejectsHostileArchives enumerates
// 29 hostile shapes and is excellent; its one structural weakness is that a
// human chose all 29.
//
// Two properties:
//
//  1. Capture never panics, and never returns a tree together with an error.
//     The second half matters because a tree returned alongside an error is a
//     leaked temporary directory and an unowned handle.
//
//  2. Whatever Capture accepts, it accepts as a FIXED POINT: re-capturing its
//     own canonical output yields byte-identical bytes and the same identity.
//     Without this, a stored digest could not be recomputed from stored bytes,
//     which is the one thing content addressing has to guarantee.
//
// Limits are deliberately small so a single iteration is cheap; the limit
// arithmetic itself has its own table tests and is not what is being fuzzed.
func FuzzCanonicalCapture(f *testing.F) {
	f.Add(makeTar(f, []tarEntry{{name: "a.txt", typeflag: tar.TypeReg, content: "hello\n"}}))
	f.Add(makeTar(f, []tarEntry{
		{name: "dir", typeflag: tar.TypeDir},
		{name: "dir/x", typeflag: tar.TypeReg, content: "x"},
	}))
	f.Add(makeTar(f, []tarEntry{
		{name: "a.txt", typeflag: tar.TypeReg, content: "y"},
		{name: "link", typeflag: tar.TypeSymlink, linkname: "a.txt"},
	}))
	f.Add(makeTar(f, []tarEntry{{name: "données/café.txt", typeflag: tar.TypeReg, content: "bonjour\n"}}))
	f.Add(makeTar(f, []tarEntry{{name: "run", typeflag: tar.TypeReg, mode: 0755, content: "#!/bin/sh\n"}}))
	f.Add([]byte(nil))
	f.Add([]byte("not a tar at all"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		canonicalizer := Canonicalizer{MaxEntries: 64, MaxContentBytes: 1 << 16, TempDir: t.TempDir()}

		tree, err := canonicalizer.Capture(context.Background(), bytes.NewReader(raw))
		if err != nil {
			if tree != nil {
				t.Fatalf("Capture() returned a tree together with error %v", err)
			}
			return
		}
		defer tree.Close()

		canonical, err := os.ReadFile(tree.ArchivePath)
		if err != nil {
			t.Fatalf("read canonical archive: %v", err)
		}
		second, err := canonicalizer.Capture(context.Background(), bytes.NewReader(canonical))
		if err != nil {
			t.Fatalf("canonical output was rejected on re-capture: %v", err)
		}
		defer second.Close()

		again, err := os.ReadFile(second.ArchivePath)
		if err != nil {
			t.Fatalf("read re-captured canonical archive: %v", err)
		}
		if !bytes.Equal(canonical, again) {
			t.Fatal("canonicalization is not a fixed point: re-capture changed the emitted bytes")
		}
		if second.Digest != tree.Digest || second.ByteSize != tree.ByteSize || second.FileCount != tree.FileCount {
			t.Fatalf("re-capture identity = %s / %d bytes / %d entries, want %s / %d / %d",
				second.Digest, second.ByteSize, second.FileCount,
				tree.Digest, tree.ByteSize, tree.FileCount)
		}
	})
}
