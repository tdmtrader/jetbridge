package hangar

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type tarEntry struct {
	name       string
	typeflag   byte
	regularA   bool
	mode       int64
	uid        int
	gid        int
	uname      string
	gname      string
	linkname   string
	content    string
	modTime    time.Time
	accessTime time.Time
	changeTime time.Time
	paxRecords map[string]string
}

func TestCanonicalArchiveByteLimitIncludesBoundedTransportOverhead(t *testing.T) {
	limit, err := CanonicalArchiveByteLimit(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tree := capture(t, Canonicalizer{MaxEntries: 1, MaxContentBytes: 1}, bytes.NewReader(makeTar(t, []tarEntry{{
		name: "file", typeflag: tar.TypeReg, mode: 0644, content: "x",
	}})))
	defer tree.Close()
	if tree.ByteSize <= 1 {
		t.Fatalf("canonical archive size = %d, want physical tar overhead", tree.ByteSize)
	}
	if tree.ByteSize > limit {
		t.Fatalf("canonical archive size = %d exceeds derived limit %d", tree.ByteSize, limit)
	}

	defaultLimit, err := CanonicalArchiveByteLimit(DefaultMaxTreeContentBytes, DefaultMaxTreeEntries)
	if err != nil || defaultLimit <= DefaultMaxTreeContentBytes {
		t.Fatalf("default transport limit = %d, %v", defaultLimit, err)
	}
	if _, err := CanonicalArchiveByteLimit(math.MaxInt64, math.MaxInt64); err == nil {
		t.Fatal("expected overflow to fail closed")
	}
}

func TestValidateArchiveLimitsEnforcesLogicalContentAndImplicitEntries(t *testing.T) {
	t.Parallel()

	t.Run("content bytes independent of tar overhead", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: "xx"}})
		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(raw), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      1,
		})
		if err == nil || !strings.Contains(err.Error(), "content limit") {
			t.Fatalf("ValidateArchiveLimits() error = %v, want content limit", err)
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("ValidateArchiveLimits() error = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("implicit parents count as entries", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "parent/file", typeflag: tar.TypeReg, content: "x"}})
		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(raw), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      1,
		})
		if err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("ValidateArchiveLimits() error = %v, want entry limit", err)
		}
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("ValidateArchiveLimits() error = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("exact limits pass", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "parent/file", typeflag: tar.TypeReg, content: "x"}})
		if err := ValidateArchiveLimits(context.Background(), bytes.NewReader(raw), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      2,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("duplicate explicit directory is rejected", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{
			{name: "parent", typeflag: tar.TypeDir},
			{name: "parent", typeflag: tar.TypeDir},
		})
		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(raw), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      1,
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate canonical path") {
			t.Fatalf("ValidateArchiveLimits() error = %v, want duplicate-path rejection", err)
		}
	})

	t.Run("data after the tar terminator is rejected", func(t *testing.T) {
		raw := append(makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: "x"}}), byte('x'))
		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(raw), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      1,
		})
		if err == nil || !strings.Contains(err.Error(), "trailing data") {
			t.Fatalf("ValidateArchiveLimits() error = %v, want trailing-data rejection", err)
		}
	})
}

func TestCanonicalizerCaptureEnforcesPhysicalArchiveAdmission(t *testing.T) {
	t.Parallel()

	limit, err := CanonicalArchiveByteLimit(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	exact := rawRepeatedPAXTar(10)
	if int64(len(exact)) != limit {
		t.Fatalf("exact archive fixture size = %d, want physical limit %d", len(exact), limit)
	}

	t.Run("accepts the exact physical bound", func(t *testing.T) {
		tree := capture(t, Canonicalizer{MaxEntries: 1, MaxContentBytes: 1}, bytes.NewReader(exact))
		defer tree.Close()
	})

	t.Run("rejects repeated extension metadata beyond the bound", func(t *testing.T) {
		raw := rawRepeatedPAXTar(11)
		_, err := (Canonicalizer{MaxEntries: 1, MaxContentBytes: 1}).Capture(context.Background(), bytes.NewReader(raw))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Capture() error = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("rejects bytes after the tar terminator", func(t *testing.T) {
		raw := append(makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: "x"}}), byte('x'))
		_, err := (Canonicalizer{MaxEntries: 1, MaxContentBytes: 1}).Capture(context.Background(), bytes.NewReader(raw))
		if err == nil || !strings.Contains(err.Error(), "trailing data") {
			t.Fatalf("Capture() error = %v, want trailing-data rejection", err)
		}
	})

	t.Run("validator uses the same physical bound", func(t *testing.T) {
		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(rawRepeatedPAXTar(11)), TreeLimits{
			MaxContentBytes: 1,
			MaxEntries:      1,
		})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("ValidateArchiveLimits() error = %v, want ErrLimitExceeded", err)
		}
	})
}

func TestCanonicalizerCaptureNormalizesMetadataAndInputOrder(t *testing.T) {
	t.Parallel()

	first := makeTar(t, []tarEntry{
		{name: "z.txt", typeflag: tar.TypeReg, mode: 0600, uid: 12, gid: 34, uname: "alice", gname: "staff", content: "z", modTime: time.Unix(123, 0)},
		{name: "bin", typeflag: tar.TypeDir, mode: 0700, uid: 12, gid: 34, modTime: time.Unix(456, 0)},
		{name: "bin/run", typeflag: tar.TypeReg, mode: 0711, uid: 12, gid: 34, content: "run", modTime: time.Unix(789, 0)},
	})
	second := makeTar(t, []tarEntry{
		{name: "bin/run", regularA: true, mode: 0777, uid: 99, gid: 98, uname: "bob", gname: "wheel", content: "run", modTime: time.Unix(900, 0), accessTime: time.Unix(901, 0), changeTime: time.Unix(902, 0)},
		{name: "z.txt", regularA: true, mode: 0666, uid: 99, gid: 98, content: "z", modTime: time.Unix(903, 0)},
		{name: "bin", typeflag: tar.TypeDir, mode: 0777, uid: 99, gid: 98, modTime: time.Unix(904, 0)},
	})

	treeA := capture(t, Canonicalizer{}, bytes.NewReader(first))
	defer treeA.Close()
	treeB := capture(t, Canonicalizer{}, bytes.NewReader(second))
	defer treeB.Close()

	archiveA := readFile(t, treeA.ArchivePath)
	archiveB := readFile(t, treeB.ArchivePath)
	if !bytes.Equal(archiveA, archiveB) {
		t.Fatal("canonical archive changed with source metadata or input order")
	}
	if treeA.Digest != treeB.Digest {
		t.Fatalf("canonical digest differs: %q != %q", treeA.Digest, treeB.Digest)
	}
	if treeA.ByteSize != int64(len(archiveA)) {
		t.Fatalf("ByteSize = %d, archive size = %d", treeA.ByteSize, len(archiveA))
	}
	wantDigest := Digest(fmt.Sprintf("sha256:%x", sha256.Sum256(archiveA)))
	if treeA.Digest != wantDigest {
		t.Fatalf("Digest = %q, want %q", treeA.Digest, wantDigest)
	}
	if treeA.FileCount != 3 {
		t.Fatalf("FileCount = %d, want 3", treeA.FileCount)
	}

	headers := readTar(t, archiveA)
	if got := headerNames(headers); !reflect.DeepEqual(got, []string{"bin", "bin/run", "z.txt"}) {
		t.Fatalf("canonical order = %q", got)
	}
	for _, hdr := range headers {
		if hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("ownership not normalized for %q: %#v", hdr.Name, hdr)
		}
		if !hdr.ModTime.Equal(time.Unix(0, 0)) || !hdr.AccessTime.Equal(time.Unix(0, 0)) || !hdr.ChangeTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("times not normalized for %q: %#v", hdr.Name, hdr)
		}
		if len(hdr.PAXRecords) != 0 || len(hdr.Xattrs) != 0 {
			t.Fatalf("host metadata survived for %q", hdr.Name)
		}
	}
	if headers[0].Mode != 0755 || headers[1].Mode != 0755 || headers[2].Mode != 0644 {
		t.Fatalf("canonical modes = %#o, %#o, %#o", headers[0].Mode, headers[1].Mode, headers[2].Mode)
	}
}

func TestCanonicalizerCaptureRoundTrip(t *testing.T) {
	t.Parallel()

	raw := makeTar(t, []tarEntry{
		{name: "empty", typeflag: tar.TypeDir, mode: 0700},
		{name: "bin", typeflag: tar.TypeDir, mode: 0755},
		{name: "bin/run", typeflag: tar.TypeReg, mode: 0100, content: "#!/bin/sh\n"},
		{name: "données", typeflag: tar.TypeDir, mode: 0755},
		{name: "données/café.txt", typeflag: tar.TypeReg, mode: 0640, content: "bonjour\n"},
		{name: "données/latest", typeflag: tar.TypeSymlink, mode: 0700, linkname: "./café.txt"},
		{name: "bin/data", typeflag: tar.TypeSymlink, linkname: "../données/café.txt"},
	})

	tree := capture(t, Canonicalizer{}, bytes.NewReader(raw))
	defer tree.Close()

	assertMode(t, filepath.Join(tree.Root, "empty"), os.ModeDir|0755)
	assertMode(t, filepath.Join(tree.Root, "bin", "run"), 0755)
	assertMode(t, filepath.Join(tree.Root, "données", "café.txt"), 0644)
	if got := readFile(t, filepath.Join(tree.Root, "données", "café.txt")); string(got) != "bonjour\n" {
		t.Fatalf("UTF-8 file content = %q", got)
	}
	if got, err := os.Readlink(filepath.Join(tree.Root, "données", "latest")); err != nil || got != "café.txt" {
		t.Fatalf("normalized symlink = %q, %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(tree.Root, "bin", "data")); err != nil || got != "../données/café.txt" {
		t.Fatalf("relative symlink = %q, %v", got, err)
	}

	canonical := readFile(t, tree.ArchivePath)
	roundTrip := capture(t, Canonicalizer{}, bytes.NewReader(canonical))
	defer roundTrip.Close()
	if got := readFile(t, roundTrip.ArchivePath); !bytes.Equal(got, canonical) {
		t.Fatal("canonical archive was not stable across a round trip")
	}
	if roundTrip.Digest != tree.Digest || roundTrip.ByteSize != tree.ByteSize || roundTrip.FileCount != tree.FileCount {
		t.Fatalf("round-trip identity changed: %#v != %#v", roundTrip, tree)
	}

	headers := readTar(t, canonical)
	for _, hdr := range headers {
		if hdr.Name == "données/latest" && (hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "café.txt" || hdr.Mode != 0777) {
			t.Fatalf("canonical symlink header = %#v", hdr)
		}
	}
}

func TestCanonicalizerCaptureNormalizesImplicitDirectories(t *testing.T) {
	t.Parallel()

	tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "implicit/file", typeflag: tar.TypeReg, content: "content"},
	})))
	defer tree.Close()

	assertMode(t, filepath.Join(tree.Root, "implicit"), os.ModeDir|0755)
}

func TestCanonicalGNUArchiveGoldenVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entries    []tarEntry
		wantDigest Digest
		wantSize   int64
	}{
		{
			name:       "short tree",
			entries:    []tarEntry{{name: "a.txt", typeflag: tar.TypeReg, content: "hello\n"}},
			wantDigest: "sha256:f4ca69b2b52dcdd85b285c63a633018f00c4b226ae9dc2f3f9748f79c711ac3e",
			wantSize:   2048,
		},
		{
			name:       "UTF-8 path",
			entries:    []tarEntry{{name: "données/café.txt", typeflag: tar.TypeReg, content: "bonjour\n"}},
			wantDigest: "sha256:2fb1992d05b03ee6916fe807dc40aa8a5112e11f3a83a50957b3e85708c5369b",
			wantSize:   2560,
		},
		{
			name:       "GNU long path",
			entries:    []tarEntry{{name: strings.Repeat("p", 101), typeflag: tar.TypeReg, content: "long path\n"}},
			wantDigest: "sha256:fffa6e74a0fdee63dba675aa6762c97fb1d3e43cbe9efc6d70c8602932b83d50",
			wantSize:   3072,
		},
		{
			name:       "GNU long symlink target",
			entries:    []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: strings.Repeat("t", 101)}},
			wantDigest: "sha256:faeb97c76dc34b1b56b8f028b1ddfe8775197dd8ad2c3c9324b0c40efbfd5f0b",
			wantSize:   2560,
		},
	}

	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, tt.entries)))
			defer tree.Close()
			if tree.Digest != tt.wantDigest || tree.ByteSize != tt.wantSize {
				t.Errorf("golden identity = %q, %d; want %q, %d", tree.Digest, tree.ByteSize, tt.wantDigest, tt.wantSize)
			}
		})
	}
}

func TestExtractRejectsHostileArchives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{name: "empty name", entries: []tarEntry{{name: "", typeflag: tar.TypeReg}}, want: "empty"},
		{name: "absolute", entries: []tarEntry{{name: "/escape", typeflag: tar.TypeReg}}, want: "absolute"},
		{name: "traversal", entries: []tarEntry{{name: "a/../escape", typeflag: tar.TypeReg}}, want: "segment"},
		{name: "leading dot slash", entries: []tarEntry{{name: "./file", typeflag: tar.TypeReg}}, want: "segment"},
		{name: "dot segment", entries: []tarEntry{{name: "a/./file", typeflag: tar.TypeReg}}, want: "segment"},
		{name: "repeated separator", entries: []tarEntry{{name: "a//file", typeflag: tar.TypeReg}}, want: "segment"},
		{name: "trailing slash", entries: []tarEntry{{name: "dir/", typeflag: tar.TypeDir}}, want: "trailing"},
		{name: "backslash", entries: []tarEntry{{name: `a\b`, typeflag: tar.TypeReg}}, want: "backslash"},
		{name: "drive path", entries: []tarEntry{{name: `C:escape`, typeflag: tar.TypeReg}}, want: "drive"},
		{name: "nested drive path", entries: []tarEntry{{name: `safe/C:escape`, typeflag: tar.TypeReg}}, want: "drive"},
		{name: "duplicate", entries: []tarEntry{{name: "same", typeflag: tar.TypeReg}, {name: "same", typeflag: tar.TypeReg}}, want: "duplicate"},
		{name: "normalized duplicate rejected at source", entries: []tarEntry{{name: "a/b", typeflag: tar.TypeReg}, {name: "a/./b", typeflag: tar.TypeReg}}, want: "segment"},
		{name: "hard link", entries: []tarEntry{{name: "hard", typeflag: tar.TypeLink, linkname: "target"}}, want: "type"},
		{name: "character device", entries: []tarEntry{{name: "char", typeflag: tar.TypeChar}}, want: "type"},
		{name: "block device", entries: []tarEntry{{name: "block", typeflag: tar.TypeBlock}}, want: "type"},
		{name: "fifo", entries: []tarEntry{{name: "fifo", typeflag: tar.TypeFifo}}, want: "type"},
		{name: "GNU sparse", entries: []tarEntry{{name: "sparse", typeflag: tar.TypeGNUSparse}}, want: "invalid tar header"},
		{name: "setuid", entries: []tarEntry{{name: "setuid", typeflag: tar.TypeReg, mode: 04755}}, want: "setuid"},
		{name: "setgid", entries: []tarEntry{{name: "setgid", typeflag: tar.TypeReg, mode: 02755}}, want: "setgid"},
		{name: "PAX metadata", entries: []tarEntry{{name: "pax", typeflag: tar.TypeReg, mode: 0644, paxRecords: map[string]string{"comment": "surprise"}}}, want: "PAX"},
		{name: "escaping symlink", entries: []tarEntry{{name: "a/link", typeflag: tar.TypeSymlink, linkname: "../../escape"}}, want: "escapes"},
		{name: "absolute symlink", entries: []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: "/escape"}}, want: "absolute"},
		{name: "empty symlink", entries: []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: ""}}, want: "empty"},
		{name: "backslash symlink", entries: []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: `..\escape`}}, want: "backslash"},
		{name: "drive symlink", entries: []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: `C:escape`}}, want: "drive"},
		{name: "nested drive symlink", entries: []tarEntry{{name: "safe/link", typeflag: tar.TypeSymlink, linkname: `../C:escape`}}, want: "drive"},
		{name: "write through symlink parent", entries: []tarEntry{{name: "parent", typeflag: tar.TypeSymlink, linkname: "safe"}, {name: "parent/file", typeflag: tar.TypeReg, content: "owned"}}, want: "symlink parent"},
		{name: "replace implicit parent with symlink", entries: []tarEntry{{name: "parent/file", typeflag: tar.TypeReg, content: "first"}, {name: "parent", typeflag: tar.TypeSymlink, linkname: "safe"}}, want: "conflicts"},
		{name: "child below regular file", entries: []tarEntry{{name: "parent", typeflag: tar.TypeReg}, {name: "parent/file", typeflag: tar.TypeReg}}, want: "parent"},
	}

	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := makeTar(t, tt.entries)
			_, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Capture() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestExtractRejectsUnsafePAXEffectivePathsAndLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry tarEntry
		want  string
	}{
		{
			name:  "absolute effective path",
			entry: tarEntry{name: "safe", typeflag: tar.TypeReg, paxRecords: map[string]string{"path": "/escape"}},
			want:  "absolute",
		},
		{
			name:  "traversing effective path",
			entry: tarEntry{name: "safe", typeflag: tar.TypeReg, paxRecords: map[string]string{"path": "../escape"}},
			want:  "segment",
		},
		{
			name:  "linkpath on regular file",
			entry: tarEntry{name: "regular", typeflag: tar.TypeReg, linkname: "ignored", paxRecords: map[string]string{"linkpath": "ignored"}},
			want:  "linkpath",
		},
		{
			name:  "absolute effective link",
			entry: tarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "safe", paxRecords: map[string]string{"linkpath": "/escape"}},
			want:  "absolute",
		},
		{
			name:  "traversing effective link",
			entry: tarEntry{name: "nested/link", typeflag: tar.TypeSymlink, linkname: "safe", paxRecords: map[string]string{"linkpath": "../../escape"}},
			want:  "escapes",
		},
	}

	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(rawPAXTar(t, tt.entry)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Capture() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestExtractRejectsUnknownAndSocketTypes(t *testing.T) {
	t.Parallel()

	typeflags := []byte{'s', 'Z'}
	if len(typeflags) == 0 {
		t.Fatal("unknown/socket type guard table is empty")
	}
	for _, typeflag := range typeflags {
		t.Run(fmt.Sprintf("type-%c", typeflag), func(t *testing.T) {
			raw := rawTarHeader("hostile", typeflag, 0, nil)
			_, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
			if err == nil || !strings.Contains(err.Error(), "type") {
				t.Fatalf("Capture() error = %v, want unsupported type", err)
			}
		})
	}
}

func TestExtractEnforcesConfiguredLimits(t *testing.T) {
	t.Parallel()

	t.Run("entry count", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{
			{name: "one", typeflag: tar.TypeReg},
			{name: "two", typeflag: tar.TypeReg},
		})
		_, err := (Canonicalizer{MaxEntries: 1}).Capture(context.Background(), bytes.NewReader(raw))
		if err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("Capture() error = %v, want entry limit", err)
		}
	})

	t.Run("streamed regular content", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "large", typeflag: tar.TypeReg, content: "12345"}})
		_, err := (Canonicalizer{MaxContentBytes: 4}).Capture(context.Background(), bytes.NewReader(raw))
		if err == nil || !strings.Contains(err.Error(), "content limit") {
			t.Fatalf("Capture() error = %v, want content limit", err)
		}
	})

	t.Run("limits must be positive", func(t *testing.T) {
		raw := makeTar(t, nil)
		for _, c := range []Canonicalizer{{MaxEntries: -1}, {MaxContentBytes: -1}} {
			if _, err := c.Capture(context.Background(), bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "positive") {
				t.Fatalf("Capture() error = %v, want positive limit", err)
			}
		}
	})

	t.Run("maximum content limit does not wrap", func(t *testing.T) {
		tree := capture(t, Canonicalizer{MaxContentBytes: math.MaxInt64}, bytes.NewReader(makeTar(t, []tarEntry{
			{name: "small", typeflag: tar.TypeReg, content: "safe"},
		})))
		defer tree.Close()
	})
}

func TestExtractCountsImplicitParentsBeforeMaterializingEntry(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	var observed []string
	canonicalizer := Canonicalizer{
		TempDir:    parent,
		MaxEntries: 1,
		beforeAnchoredCleanup: func(root *os.Root) error {
			return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				observed = append(observed, name)
				return nil
			})
		},
	}
	raw := makeTar(t, []tarEntry{{name: "a/b", typeflag: tar.TypeReg, content: "must-not-be-created"}})
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("Capture() error = %v, want entry limit", err)
	}
	if !reflect.DeepEqual(observed, []string{".", "content.spool", "root"}) {
		t.Fatalf("paths present before failed-capture cleanup = %q, want only private extraction root", observed)
	}

	tree := capture(t, Canonicalizer{MaxEntries: 2}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "a/b", typeflag: tar.TypeReg, content: "ok"},
		{name: "a", typeflag: tar.TypeDir},
	})))
	defer tree.Close()
	if tree.FileCount != 2 {
		t.Fatalf("FileCount = %d, want implicit directory plus file", tree.FileCount)
	}
}

func TestExtractContentAccountingDoesNotOverflow(t *testing.T) {
	t.Parallel()

	raw := makeTar(t, []tarEntry{{name: "one-more", typeflag: tar.TypeReg, content: "x"}})
	tr := tar.NewReader(bytes.NewReader(raw))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	spool, err := os.CreateTemp(t.TempDir(), "tree-spool-")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	_, err = extractRegular(context.Background(), tr, root, spool, hdr, math.MaxInt64, math.MaxInt64, nil)
	if err == nil || !strings.Contains(err.Error(), "content limit") {
		t.Fatalf("extractRegular() error = %v, want content limit", err)
	}
}

func TestExtractExplicitDirectoryModeIgnoresUmask(t *testing.T) {
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)

	tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "explicit", typeflag: tar.TypeDir, mode: 0700},
	})))
	defer tree.Close()
	assertMode(t, filepath.Join(tree.Root, "explicit"), os.ModeDir|0755)
}

func TestExtractUsesRootConfinementAgainstParentReplacement(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	canonicalizer := Canonicalizer{
		beforeMaterialize: func(root *os.Root, name string) error {
			if name != "parent/file" {
				return nil
			}
			if err := root.Remove("parent"); err != nil {
				return err
			}
			return root.Symlink(outside, "parent")
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "parent/file", typeflag: tar.TypeReg, content: "escape"},
	})))
	if err == nil {
		t.Fatal("Capture unexpectedly wrote through replaced symlink parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file exists or could not be checked: %v", err)
	}
}

func TestExtractRootRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	rootPath := t.TempDir()
	outside := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Symlink(outside, "escape"); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("escape/file", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("os.Root unexpectedly followed an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file exists or could not be checked: %v", err)
	}
}

func TestExtractRejectsUnexpectedHostEquivalentCollision(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeMaterialize: func(root *os.Root, name string) error {
			if name == "collision" {
				return root.WriteFile(name, []byte("host alias"), 0600)
			}
			return nil
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "collision", typeflag: tar.TypeReg, content: "archive"},
	})))
	if err == nil || !strings.Contains(err.Error(), "host-equivalent collision") {
		t.Fatalf("Capture() error = %v, want host-equivalent collision", err)
	}
}

func TestCanonicalNamespacePreservesPOSIXNamesWhenHostSupportsThem(t *testing.T) {
	t.Parallel()

	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, "Case"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, aliasErr := os.Lstat(filepath.Join(probe, "case"))
	caseSensitive := errors.Is(aliasErr, os.ErrNotExist)

	raw := makeTar(t, []tarEntry{
		{name: "Case", typeflag: tar.TypeReg, content: "upper"},
		{name: "case", typeflag: tar.TypeReg, content: "lower"},
		{name: "report:final", typeflag: tar.TypeReg, content: "colon"},
	})
	tree, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
	if caseSensitive {
		if err != nil {
			t.Fatalf("bytewise-distinct POSIX names rejected on case-sensitive host: %v", err)
		}
		defer tree.Close()
		if got := headerNames(readTar(t, readFile(t, tree.ArchivePath))); !reflect.DeepEqual(got, []string{"Case", "case", "report:final"}) {
			t.Fatalf("canonical names = %q", got)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "host-equivalent collision") {
		if tree != nil {
			defer tree.Close()
		}
		t.Fatalf("Capture() error = %v, want fail-closed host alias collision", err)
	}
}

func TestCanonicalNamespacePreservesPOSIXColonNames(t *testing.T) {
	t.Parallel()

	tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "report:final", typeflag: tar.TypeReg, content: "colon"},
	})))
	defer tree.Close()
	if got := headerNames(readTar(t, readFile(t, tree.ArchivePath))); !reflect.DeepEqual(got, []string{"report:final"}) {
		t.Fatalf("canonical names = %q", got)
	}
}

func TestCanonicalizerCaptureRevalidatesOpenedRegularFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		replace func(*os.Root) error
	}{
		{
			name: "type",
			replace: func(root *os.Root) error {
				if err := root.Remove("file"); err != nil {
					return err
				}
				return root.Mkdir("file", 0755)
			},
		},
		{
			name: "size",
			replace: func(root *os.Root) error {
				return root.WriteFile("file", []byte("changed size"), 0644)
			},
		},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			canonicalizer := Canonicalizer{
				beforeCanonicalOpen: func(root *os.Root, name string) error {
					if name != "file" || called {
						return nil
					}
					called = true
					return tt.replace(root)
				},
			}
			_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
				{name: "file", typeflag: tar.TypeReg, content: "original"},
			})))
			if err == nil || !strings.Contains(err.Error(), "changed during capture") {
				t.Fatalf("Capture() error = %v, want descriptor revalidation failure", err)
			}
		})
	}
}

func TestCanonicalizerCaptureRejectsDirectoryReplacement(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforePreEmitVerify: func(root *os.Root) error {
			// The replacement must occupy a different inode, since that is the
			// only thing distinguishing it from the captured directory. Allocate
			// it while the original still exists, then swap: removing first lets
			// filesystems that recycle inode numbers (ext4, overlayfs) hand the
			// same one straight back, which would make the swap undetectable and
			// this assertion pass or fail by filesystem rather than by behaviour.
			if err := root.Mkdir("empty.replacement", 0755); err != nil {
				return err
			}
			if err := root.Remove("empty"); err != nil {
				return err
			}
			return os.Rename(
				filepath.Join(root.Name(), "empty.replacement"),
				filepath.Join(root.Name(), "empty"),
			)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "empty", typeflag: tar.TypeDir},
	})))
	if err == nil || !strings.Contains(err.Error(), "verify captured tree before emission") {
		t.Fatalf("Capture() error = %v, want pre-emission directory identity failure", err)
	}
}

func TestCanonicalizerCaptureRejectsReplacedSymlinkTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
	}{
		{name: "absolute", target: "/escape"},
		{name: "traversal", target: "../../escape"},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonicalizer := Canonicalizer{
				beforePreEmitVerify: func(root *os.Root) error {
					// Same inode-recycling hazard as the directory case above:
					// allocate the replacement link before removing the original
					// so the swap is detectable on every filesystem.
					if err := root.Symlink(tt.target, "nested/link.replacement"); err != nil {
						return err
					}
					if err := root.Remove("nested/link"); err != nil {
						return err
					}
					return os.Rename(
						filepath.Join(root.Name(), "nested/link.replacement"),
						filepath.Join(root.Name(), "nested/link"),
					)
				},
			}
			_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
				{name: "nested/link", typeflag: tar.TypeSymlink, linkname: "safe"},
			})))
			if err == nil || !strings.Contains(err.Error(), "verify captured tree before emission") {
				t.Fatalf("Capture() error = %v, want pre-emission symlink target failure", err)
			}
		})
	}
}

func TestCanonicalizerCaptureRejectsRegularRenameAndSymlinkReplacement(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforePreEmitVerify: func(root *os.Root) error {
			if err := root.Rename("file", "moved"); err != nil {
				return err
			}
			return root.Symlink("moved", "file")
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "file", typeflag: tar.TypeReg, content: "sealed"},
	})))
	if err == nil || !strings.Contains(err.Error(), "verify captured tree before emission") {
		t.Fatalf("Capture() error = %v, want renamed regular and extra-path failure", err)
	}
}

func TestCanonicalizerCaptureRejectsEntriesAddedBeforeOrAfterEmission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		canonicalizer Canonicalizer
		wantPhase     string
	}{
		{
			name: "before",
			canonicalizer: Canonicalizer{beforePreEmitVerify: func(root *os.Root) error {
				return root.WriteFile("extra", []byte("extra"), 0644)
			}},
			wantPhase: "before emission",
		},
		{
			name: "after",
			canonicalizer: Canonicalizer{beforePostEmitVerify: func(root *os.Root) error {
				return root.WriteFile("extra", []byte("extra"), 0644)
			}},
			wantPhase: "after emission",
		},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
				{name: "file", typeflag: tar.TypeReg, content: "sealed"},
			})))
			if err == nil || !strings.Contains(err.Error(), "verify captured tree "+tt.wantPhase) {
				t.Fatalf("Capture() error = %v, want %s integrity failure", err, tt.wantPhase)
			}
		})
	}
}

func TestCanonicalizerCaptureRejectsSameLengthRegularContentMutation(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeCanonicalOpen: func(root *os.Root, name string) error {
			if name != "file" {
				return nil
			}
			return root.WriteFile(name, []byte("MUTATED!"), 0644)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "file", typeflag: tar.TypeReg, content: "ORIGINAL"},
	})))
	if err == nil || !strings.Contains(err.Error(), "regular content differs") {
		t.Fatalf("Capture() error = %v, want regular content integrity failure", err)
	}
}

func TestCanonicalizerCaptureRejectsTamperedContentSpool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tamper func(*os.Root, *os.File) error
		want   string
	}{
		{
			name: "same-length content",
			tamper: func(_ *os.Root, spool *os.File) error {
				_, err := spool.WriteAt([]byte("MUTATED!"), 0)
				return err
			},
			want: "spool content differs",
		},
		{
			name: "truncated",
			tamper: func(_ *os.Root, spool *os.File) error {
				return spool.Truncate(3)
			},
			want: "spool size",
		},
		{
			name: "appended",
			tamper: func(_ *os.Root, spool *os.File) error {
				_, err := spool.WriteAt([]byte("extra"), int64(len("ORIGINAL")))
				return err
			},
			want: "spool size",
		},
		{
			name: "path replacement",
			tamper: func(root *os.Root, _ *os.File) error {
				if err := root.Rename("content.spool", "moved.spool"); err != nil {
					return err
				}
				return root.WriteFile("content.spool", []byte("ORIGINAL"), 0600)
			},
			want: "spool path identity",
		},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonicalizer := Canonicalizer{beforeSpoolSeal: tt.tamper}
			_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
				{name: "file", typeflag: tar.TypeReg, content: "ORIGINAL"},
			})))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Capture() error = %v, want %q integrity failure", err, tt.want)
			}
		})
	}
}

func TestCanonicalizerCaptureSealsSpoolReadOnlyAndUnlinksPath(t *testing.T) {
	t.Parallel()

	var writableInfo fs.FileInfo
	canonicalizer := Canonicalizer{
		beforeSpoolSeal: func(_ *os.Root, spool *os.File) error {
			var err error
			writableInfo, err = spool.Stat()
			return err
		},
		afterSpoolSeal: func(root *os.Root, spool *os.File) error {
			sealedInfo, err := spool.Stat()
			if err != nil {
				return err
			}
			if writableInfo == nil || !os.SameFile(writableInfo, sealedInfo) {
				return fmt.Errorf("sealed descriptor identity differs")
			}
			if _, err := root.Lstat("content.spool"); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("sealed spool pathname still exists: %w", err)
			}
			if _, err := spool.WriteAt([]byte("x"), 0); err == nil {
				return fmt.Errorf("sealed spool descriptor remains writable")
			}
			if err := spool.Truncate(0); err == nil {
				return fmt.Errorf("sealed spool descriptor remains truncatable")
			}
			return nil
		},
	}
	tree := capture(t, canonicalizer, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "file", typeflag: tar.TypeReg, content: "ORIGINAL"},
	})))
	defer tree.Close()
}

func TestValidateSpoolLayoutRejectsInvalidSections(t *testing.T) {
	t.Parallel()

	valid := func() *captureIndex {
		return &captureIndex{
			entries: map[string]capturedEntry{
				"first": {name: "first", kind: extractedRegular, spoolOffset: 0, size: 3},
				"empty": {name: "empty", kind: extractedRegular, spoolOffset: 3, size: 0},
				"last":  {name: "last", kind: extractedRegular, spoolOffset: 3, size: 2},
				"dir":   {name: "dir", kind: extractedDirectory},
			},
			spoolSize: 5,
		}
	}
	if err := validateSpoolLayout(valid(), 5); err != nil {
		t.Fatalf("valid spool layout rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*captureIndex)
		actual int64
		want   string
	}{
		{
			name: "declared total differs",
			mutate: func(index *captureIndex) {
				index.spoolSize = 4
			},
			actual: 5,
			want:   "spool size",
		},
		{
			name: "gap",
			mutate: func(index *captureIndex) {
				entry := index.entries["last"]
				entry.spoolOffset = 4
				index.entries["last"] = entry
			},
			actual: 5,
			want:   "gap or overlap",
		},
		{
			name: "overlap",
			mutate: func(index *captureIndex) {
				entry := index.entries["last"]
				entry.spoolOffset = 2
				index.entries["last"] = entry
			},
			actual: 5,
			want:   "gap or overlap",
		},
		{
			name: "out of bounds",
			mutate: func(index *captureIndex) {
				entry := index.entries["last"]
				entry.size = 3
				index.entries["last"] = entry
			},
			actual: 5,
			want:   "bounds",
		},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := valid()
			tt.mutate(index)
			err := validateSpoolLayout(index, tt.actual)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateSpoolLayout() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCanonicalEmissionRejectsSpoolBytesThatDoNotMatchCapturedHash(t *testing.T) {
	t.Parallel()

	spool, err := os.CreateTemp(t.TempDir(), "tree-spool-")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if _, err := spool.Write([]byte("MUTATED!")); err != nil {
		t.Fatal(err)
	}
	entries := []capturedEntry{{
		name:        "file",
		kind:        extractedRegular,
		mode:        0644,
		size:        int64(len("ORIGINAL")),
		spoolOffset: 0,
		contentHash: sha256.Sum256([]byte("ORIGINAL")),
	}}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	err = writeCanonicalEntries(context.Background(), tw, spool, nil, entries, nil)
	closeErr := tw.Close()
	if err == nil || !strings.Contains(err.Error(), "spool content differs") {
		t.Fatalf("writeCanonicalEntries() error = %v, want spool content integrity failure (close: %v)", err, closeErr)
	}
}

func TestCanonicalizerCaptureRejectsPrivateRootPathReplacement(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	moved := filepath.Join(parent, "moved-capture")
	var privateRoot string
	canonicalizer := Canonicalizer{
		TempDir: parent,
		beforeCaptureBoundary: func(_ *os.Root, name string) error {
			privateRoot = name
			if err := os.Rename(name, moved); err != nil {
				return err
			}
			return os.Mkdir(name, 0700)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{
		{name: "secret", typeflag: tar.TypeReg, content: "sensitive"},
	})))
	if err == nil || !strings.Contains(err.Error(), "capture path identity") {
		t.Fatalf("Capture() error = %v, want private-root boundary failure", err)
	}
	if privateRoot == "" {
		t.Fatal("private-root boundary hook did not run")
	}
	assertDirectoryEmpty(t, moved)
	if _, err := os.Stat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement private root remains after cleanup: %v", err)
	}
}

func TestCanonicalizerCaptureRejectsArchivePathReplacementAtBoundary(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeCaptureBoundary: func(root *os.Root, _ string) error {
			if err := root.Rename("canonical.tar", "moved.tar"); err != nil {
				return err
			}
			return root.WriteFile("canonical.tar", []byte("replacement"), 0600)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
	if err == nil || !strings.Contains(err.Error(), "archive path identity") {
		t.Fatalf("Capture() error = %v, want archive boundary failure", err)
	}
}

func TestCanonicalizerCaptureRejectsArchiveContentRewriteAtBoundary(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeCaptureBoundary: func(root *os.Root, _ string) error {
			archive, err := root.OpenFile("canonical.tar", os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			info, statErr := archive.Stat()
			if statErr == nil {
				_, statErr = archive.WriteAt(bytes.Repeat([]byte{0xa5}, int(info.Size())), 0)
			}
			return errors.Join(statErr, archive.Close())
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
	if err == nil || !strings.Contains(err.Error(), "archive digest") {
		t.Fatalf("Capture() error = %v, want archive digest integrity failure", err)
	}
}

func TestCanonicalizerCaptureRejectsArchiveModeChangeAtBoundary(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeCaptureBoundary: func(root *os.Root, _ string) error {
			return root.Chmod("canonical.tar", 0644)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
	if err == nil || !strings.Contains(err.Error(), "archive mode") {
		t.Fatalf("Capture() error = %v, want archive mode integrity failure", err)
	}
}

func TestCanonicalizerCaptureRejectsArchiveSizeChangeAtBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*os.File) error
	}{
		{
			name: "truncate",
			mutate: func(archive *os.File) error {
				return archive.Truncate(1)
			},
		},
		{
			name: "append",
			mutate: func(archive *os.File) error {
				_, err := archive.Write([]byte("appended"))
				return err
			},
		},
	}
	if len(tests) == 0 {
		t.Fatal("test table contains no cases")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonicalizer := Canonicalizer{
				beforeCaptureBoundary: func(root *os.Root, _ string) error {
					flags := os.O_WRONLY
					if tt.name == "append" {
						flags |= os.O_APPEND
					}
					archive, err := root.OpenFile("canonical.tar", flags, 0)
					if err != nil {
						return err
					}
					return errors.Join(tt.mutate(archive), archive.Close())
				},
			}
			_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
			if err == nil || !strings.Contains(err.Error(), "archive byte size") {
				t.Fatalf("Capture() error = %v, want archive byte-size integrity failure", err)
			}
		})
	}
}

func TestCanonicalizerCapturePerformsFinalTreeVerificationAtSuccessBoundary(t *testing.T) {
	t.Parallel()

	canonicalizer := Canonicalizer{
		beforeCaptureBoundary: func(root *os.Root, _ string) error {
			return root.WriteFile("root/late-extra", []byte("late"), 0600)
		},
	}
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
	if err == nil || !strings.Contains(err.Error(), "verify captured tree at success boundary") {
		t.Fatalf("Capture() error = %v, want final exact-tree verification failure", err)
	}
}

func TestCanonicalizerCaptureRejectsUntrustedConfiguredTempDir(t *testing.T) {
	t.Parallel()

	t.Run("writable without sticky bit", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(parent, 0700)
		_, err := (Canonicalizer{TempDir: parent}).Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
		if err == nil || !strings.Contains(err.Error(), "trusted temporary parent") {
			t.Fatalf("Capture() error = %v, want untrusted temporary parent", err)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(parent, nil, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := (Canonicalizer{TempDir: parent}).Capture(context.Background(), bytes.NewReader(makeTar(t, nil)))
		if err == nil || !strings.Contains(err.Error(), "trusted temporary parent") {
			t.Fatalf("Capture() error = %v, want temporary parent directory error", err)
		}
	})

	t.Run("sticky shared directory", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, os.ModeSticky|0777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(parent, 0700)
		tree := capture(t, Canonicalizer{TempDir: parent}, bytes.NewReader(makeTar(t, nil)))
		defer tree.Close()
	})
}

func TestCanonicalizerCaptureStabilizesRelativeTempDir(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	captureWorkingDirectory := filepath.Join(workspace, "capture-cwd")
	otherWorkingDirectory := filepath.Join(workspace, "other-cwd")
	for _, directory := range []string{captureWorkingDirectory, otherWorkingDirectory, filepath.Join(captureWorkingDirectory, "scratch")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chdir(captureWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalWorkingDirectory)

	tree := capture(t, Canonicalizer{TempDir: "scratch"}, bytes.NewReader(makeTar(t, []tarEntry{{
		name: "file", typeflag: tar.TypeReg, content: "stable",
	}})))
	privateRoot := filepath.Dir(tree.Root)
	cleanupRoot := privateRoot
	if !filepath.IsAbs(cleanupRoot) {
		cleanupRoot = filepath.Join(captureWorkingDirectory, cleanupRoot)
	}
	defer os.RemoveAll(cleanupRoot)

	if !filepath.IsAbs(tree.Root) || !filepath.IsAbs(tree.ArchivePath) || !filepath.IsAbs(tree.privateRoot) {
		t.Fatalf("capture paths are not absolute: Root=%q ArchivePath=%q privateRoot=%q", tree.Root, tree.ArchivePath, tree.privateRoot)
	}
	if err := os.Chdir(otherWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(tree.Root, "file"))); got != "stable" {
		t.Fatalf("captured content after cwd change = %q, want stable", got)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("Close() after cwd change = %v", err)
	}
	if _, err := os.Stat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private root remains after cwd change and Close: %v", err)
	}
}

func TestCapturedTreeCloseWipesThroughAnchoredRootAfterRename(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	tree := capture(t, Canonicalizer{TempDir: parent}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "secret", typeflag: tar.TypeReg, content: "sensitive"},
	})))
	privateRoot := filepath.Dir(tree.Root)
	moved := filepath.Join(parent, "moved-capture")
	if err := os.Rename(privateRoot, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(privateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("pathname cleanup failed")
	attempts := 0
	tree.removeAll = func(name string) error {
		attempts++
		if attempts == 1 {
			return removeErr
		}
		return os.RemoveAll(name)
	}
	if err := tree.Close(); !errors.Is(err, removeErr) {
		t.Fatalf("first Close() = %v, want pathname cleanup failure", err)
	}
	assertDirectoryEmpty(t, moved)
	if err := tree.Close(); err != nil {
		t.Fatalf("retry Close() = %v", err)
	}
	if _, err := os.Stat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement private root remains after Close retry: %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("idempotent Close() = %v", err)
	}
}

func TestExtractRejectsTruncatedTarAndPropagatesReaderErrors(t *testing.T) {
	t.Parallel()

	t.Run("truncated", func(t *testing.T) {
		raw := makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: strings.Repeat("x", 800)}})
		_, err := (Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw[:700]))
		if err == nil || (!errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "unexpected EOF")) {
			t.Fatalf("Capture() error = %v, want unexpected EOF", err)
		}
	})

	t.Run("source error", func(t *testing.T) {
		sentinel := errors.New("source failed")
		_, err := (Canonicalizer{}).Capture(context.Background(), &errorReader{err: sentinel})
		if !errors.Is(err, sentinel) {
			t.Fatalf("Capture() error = %v, want %v", err, sentinel)
		}
	})
}

func TestCanonicalizerCaptureHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("before first header", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (Canonicalizer{}).Capture(ctx, bytes.NewReader(makeTar(t, nil)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Capture() error = %v, want context cancellation", err)
		}
	})

	t.Run("during file copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		raw := makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: strings.Repeat("x", 4096)}})
		reader := &cancelingReader{reader: bytes.NewReader(raw), cancel: cancel, remaining: 700}
		_, err := (Canonicalizer{}).Capture(ctx, reader)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Capture() error = %v, want context cancellation", err)
		}
	})

	t.Run("closes blocked read closer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := newBlockingReadCloser()
		defer reader.Close()
		result := make(chan error, 1)
		go func() {
			_, err := (Canonicalizer{}).Capture(ctx, reader)
			result <- err
		}()
		select {
		case <-reader.started:
		case <-time.After(2 * time.Second):
			t.Fatal("Capture did not start source Read")
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Capture() error = %v, want context cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Capture did not close the blocked source reader")
		}
		if reader.closeCalls() != 1 {
			t.Fatalf("Close calls = %d, want 1", reader.closeCalls())
		}
	})

	t.Run("does not close reader on ordinary success", func(t *testing.T) {
		reader := &trackingReadCloser{Reader: bytes.NewReader(makeTar(t, nil))}
		tree := capture(t, Canonicalizer{}, reader)
		defer tree.Close()
		if reader.closeCalls != 0 {
			t.Fatalf("source Close calls = %d, want 0", reader.closeCalls)
		}
	})
}

func TestCanonicalizerCaptureCleansUpAndCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	canonicalizer := Canonicalizer{TempDir: parent}
	tree := capture(t, canonicalizer, bytes.NewReader(makeTar(t, []tarEntry{{name: "file", typeflag: tar.TypeReg, content: "ok"}})))
	privateRoot := filepath.Dir(tree.Root)
	if filepath.Dir(tree.ArchivePath) != privateRoot {
		t.Fatalf("root and archive do not share private ownership tree: %q, %q", tree.Root, tree.ArchivePath)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("first Close() = %v", err)
	}
	if _, err := os.Stat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private root remains after Close(): %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}

	before := directoryNames(t, parent)
	_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{{name: "../bad", typeflag: tar.TypeReg}})))
	if err == nil {
		t.Fatal("invalid archive unexpectedly succeeded")
	}
	after := directoryNames(t, parent)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed capture leaked private data: before %q, after %q", before, after)
	}
}

func TestCanonicalizerCaptureJoinsCleanupErrorsAndCloseRetries(t *testing.T) {
	t.Parallel()

	t.Run("failed capture joins cleanup error", func(t *testing.T) {
		cleanupErr := errors.New("cleanup failed")
		canonicalizer := Canonicalizer{
			TempDir: t.TempDir(),
			removeAll: func(privateRoot string) error {
				return errors.Join(os.RemoveAll(privateRoot), cleanupErr)
			},
		}
		_, err := canonicalizer.Capture(context.Background(), bytes.NewReader(makeTar(t, []tarEntry{{name: "../bad"}})))
		if err == nil || !strings.Contains(err.Error(), "segment") || !errors.Is(err, cleanupErr) {
			t.Fatalf("Capture() error = %v, want validation and cleanup errors", err)
		}
	})

	t.Run("Close retries after removal error", func(t *testing.T) {
		tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, nil)))
		privateRoot := filepath.Dir(tree.Root)
		removeErr := errors.New("temporary removal failure")
		attempts := 0
		tree.removeAll = func(name string) error {
			attempts++
			if attempts == 1 {
				return removeErr
			}
			return os.RemoveAll(name)
		}
		if err := tree.Close(); !errors.Is(err, removeErr) {
			t.Fatalf("first Close() = %v, want temporary removal failure", err)
		}
		if _, err := os.Stat(privateRoot); err != nil {
			t.Fatalf("private root removed after failed Close: %v", err)
		}
		if err := tree.Close(); err != nil {
			t.Fatalf("retry Close() = %v", err)
		}
		if attempts != 2 {
			t.Fatalf("removal attempts = %d, want 2", attempts)
		}
		if err := tree.Close(); err != nil {
			t.Fatalf("idempotent Close() = %v", err)
		}
		if attempts != 2 {
			t.Fatalf("successful Close retried removal: attempts = %d", attempts)
		}
	})

	t.Run("Close continues pathname cleanup after root close error", func(t *testing.T) {
		tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, nil)))
		privateRoot := filepath.Dir(tree.Root)
		closeErr := errors.New("root close failed")
		closeCalls := 0
		tree.closeRoot = func(root *os.Root) error {
			closeCalls++
			return errors.Join(root.Close(), closeErr)
		}

		if err := tree.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("Close() = %v, want root close error", err)
		}
		if _, err := os.Stat(privateRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("private root remains after root close error: %v", err)
		}
		if err := tree.Close(); err != nil {
			t.Fatalf("retry Close() = %v", err)
		}
		if closeCalls != 1 {
			t.Fatalf("root close calls = %d, want 1", closeCalls)
		}
	})
}

func makeTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if entry.regularA {
			typeflag = tar.TypeRegA
		} else if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 && typeflag != tar.TypeSymlink {
			mode = 0644
		}
		hdr := &tar.Header{
			Name:       entry.name,
			Typeflag:   typeflag,
			Mode:       mode,
			Uid:        entry.uid,
			Gid:        entry.gid,
			Uname:      entry.uname,
			Gname:      entry.gname,
			Linkname:   entry.linkname,
			Size:       int64(len(entry.content)),
			ModTime:    entry.modTime,
			AccessTime: entry.accessTime,
			ChangeTime: entry.changeTime,
			PAXRecords: entry.paxRecords,
		}
		if typeflag != tar.TypeReg && typeflag != tar.TypeRegA {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			if entry.name == "" {
				// archive/tar rightly refuses an empty name; construct that malformed
				// header below so Capture still owns the security decision.
				return rawTarHeader("", typeflag, mode, []byte(entry.content))
			}
			t.Fatalf("write tar header %q: %v", entry.name, err)
		}
		if hdr.Size > 0 {
			if _, err := io.WriteString(tw, entry.content); err != nil {
				t.Fatalf("write tar content %q: %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func rawTarHeader(name string, typeflag byte, mode int64, content []byte) []byte {
	return append(rawTarEntry(name, typeflag, mode, "", content), make([]byte, 1024)...)
}

func rawPAXTar(t *testing.T, entry tarEntry) []byte {
	t.Helper()
	keys := make([]string, 0, len(entry.paxRecords))
	for key := range entry.paxRecords {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var records strings.Builder
	for _, key := range keys {
		records.WriteString(formatRawPAXRecord(key, entry.paxRecords[key]))
	}
	typeflag := entry.typeflag
	if typeflag == 0 {
		typeflag = tar.TypeReg
	}
	mode := entry.mode
	if mode == 0 {
		mode = 0644
	}
	result := rawTarEntry("PaxHeaders.0/entry", tar.TypeXHeader, 0644, "", []byte(records.String()))
	result = append(result, rawTarEntry(entry.name, typeflag, mode, entry.linkname, []byte(entry.content))...)
	return append(result, make([]byte, 1024)...)
}

func rawRepeatedPAXTar(extensionCount int) []byte {
	result := make([]byte, 0, extensionCount*1024+2048)
	record := []byte(formatRawPAXRecord("path", "file"))
	for range extensionCount {
		result = append(result, rawTarEntry("PaxHeaders.0/file", tar.TypeXHeader, 0644, "", record)...)
	}
	result = append(result, rawTarEntry("file", tar.TypeReg, 0644, "", []byte("x"))...)
	return append(result, make([]byte, 1024)...)
}

func formatRawPAXRecord(key, value string) string {
	body := key + "=" + value + "\n"
	size := len(body) + 2
	for {
		record := strconv.Itoa(size) + " " + body
		if len(record) == size {
			return record
		}
		size = len(record)
	}
}

func rawTarEntry(name string, typeflag byte, mode int64, linkname string, content []byte) []byte {
	block := make([]byte, 512)
	copy(block[0:100], name)
	writeOctal(block[100:108], mode)
	writeOctal(block[108:116], 0)
	writeOctal(block[116:124], 0)
	writeOctal(block[124:136], int64(len(content)))
	writeOctal(block[136:148], 0)
	for i := 148; i < 156; i++ {
		block[i] = ' '
	}
	block[156] = typeflag
	copy(block[157:257], linkname)
	copy(block[257:263], "ustar\x00")
	copy(block[263:265], "00")
	var sum int64
	for _, b := range block {
		sum += int64(b)
	}
	writeOctal(block[148:156], sum)
	result := append(block, content...)
	if padding := (512 - len(content)%512) % 512; padding != 0 {
		result = append(result, make([]byte, padding)...)
	}
	return result
}

func writeOctal(field []byte, value int64) {
	raw := fmt.Sprintf("%0*o", len(field)-1, value)
	copy(field, raw)
	field[len(field)-1] = 0
}

func capture(t *testing.T, canonicalizer Canonicalizer, reader io.Reader) *CapturedTree {
	t.Helper()
	tree, err := canonicalizer.Capture(context.Background(), reader)
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	return tree
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return data
}

func readTar(t *testing.T, data []byte) []*tar.Header {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(data))
	var headers []*tar.Header
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return headers
		}
		if err != nil {
			t.Fatalf("read canonical tar: %v", err)
		}
		clone := *hdr
		headers = append(headers, &clone)
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatalf("read canonical content: %v", err)
		}
	}
}

func headerNames(headers []*tar.Header) []string {
	names := make([]string, len(headers))
	for i, hdr := range headers {
		names[i] = hdr.Name
	}
	return names
}

func assertMode(t *testing.T, name string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatalf("stat %q: %v", name, err)
	}
	got := info.Mode() & (os.ModeType | os.ModePerm)
	if got != want {
		t.Fatalf("mode %q = %v, want %v", name, got, want)
	}
}

func directoryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read directory %q: %v", root, err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)
	return names
}

func assertDirectoryEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read directory %q: %v", root, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %q contains sensitive capture paths: %v", root, entries)
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }

type cancelingReader struct {
	reader    io.Reader
	cancel    context.CancelFunc
	remaining int
	canceled  bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if !r.canceled && r.remaining <= 0 {
		r.cancel()
		r.canceled = true
	}
	if !r.canceled && len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= n
	return n, err
}

type blockingReadCloser struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	mu        sync.Mutex
	closes    int
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	return 0, os.ErrClosed
}

func (r *blockingReadCloser) Close() error {
	r.mu.Lock()
	r.closes++
	r.mu.Unlock()
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func (r *blockingReadCloser) closeCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

type trackingReadCloser struct {
	io.Reader
	closeCalls int
}

func (r *trackingReadCloser) Close() error {
	r.closeCalls++
	return nil
}

func TestCloseReadCloserOnCancelStopIsIdempotent(t *testing.T) {
	reader := newBlockingReadCloser()
	stop := closeReadCloserOnCancel(context.Background(), reader)
	done := make(chan struct{})
	go func() {
		stop()
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("calling the cancellation stopper twice blocked forever")
	}
	if calls := reader.closeCalls(); calls != 0 {
		t.Fatalf("stopping a live cancellation hook closed the reader %d times", calls)
	}
}

func TestCloseReadCloserOnCancelConcurrentStopAndCancellationClosesAtMostOnce(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		reader := newBlockingReadCloser()
		stop := closeReadCloserOnCancel(ctx, reader)
		start := make(chan struct{})
		var callers sync.WaitGroup
		for caller := 0; caller < 8; caller++ {
			callers.Add(1)
			go func() {
				defer callers.Done()
				<-start
				stop()
			}()
		}
		close(start)
		cancel()
		callers.Wait()
		if calls := reader.closeCalls(); calls > 1 {
			t.Fatalf("iteration %d closed the reader %d times", iteration, calls)
		}
	}
}
