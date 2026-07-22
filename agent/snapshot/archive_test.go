package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestCanonicalCaptureNormalizesMetadataAndInputOrder(t *testing.T) {
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

func TestCanonicalCaptureRoundTrip(t *testing.T) {
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

func TestCanonicalCaptureNormalizesImplicitDirectories(t *testing.T) {
	t.Parallel()

	tree := capture(t, Canonicalizer{}, bytes.NewReader(makeTar(t, []tarEntry{
		{name: "implicit/file", typeflag: tar.TypeReg, content: "content"},
	})))
	defer tree.Close()

	assertMode(t, filepath.Join(tree.Root, "implicit"), os.ModeDir|0755)
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

func TestExtractRejectsUnknownAndSocketTypes(t *testing.T) {
	t.Parallel()

	for _, typeflag := range []byte{'s', 'Z'} {
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

func TestCanonicalCaptureHonorsContextCancellation(t *testing.T) {
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
}

func TestCanonicalCaptureCleansUpAndCloseIsIdempotent(t *testing.T) {
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
	return append(result, make([]byte, 1024)...)
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
