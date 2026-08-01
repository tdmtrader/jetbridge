package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// validateArchivePath's rejections are the highest-value mark in the whole
// plan: they are the exact shape a first user hits running `tar -cf x.tar .`
// (a "./" entry), and until this mark existed classifyArchiveFailure joined
// the rejection with ErrInvalidArchive and nothing else — every archive-shape
// mistake came back as a bare 400 with no reason. This proves each rejection
// is individually marked and names the actual problem.
func TestValidateArchivePathMarksEveryRejectionWithClientDetail(t *testing.T) {
	tests := map[string]struct {
		name string
		want string
	}{
		"empty":                {name: "", want: "archive path is empty"},
		"too long":             {name: strings.Repeat("a", int(MaxSnapshotPathBytes)+1), want: "archive path exceeds"},
		"backslash":            {name: `a\b`, want: "contains a backslash"},
		"absolute":             {name: "/a", want: "is absolute"},
		"drive-like":           {name: "C:/a", want: "is drive-like"},
		"trailing separator":   {name: "a/", want: "has a trailing separator"},
		"dot segment":          {name: "./a", want: "contains an empty, dot, or traversal segment"},
		"dotdot segment":       {name: "a/../b", want: "contains an empty, dot, or traversal segment"},
		"empty middle segment": {name: "a//b", want: "contains an empty, dot, or traversal segment"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateArchivePath(test.name)
			if err == nil {
				t.Fatal("expected validateArchivePath to reject the name")
			}
			detail, ok := ClientDetail(err)
			if !ok {
				t.Fatalf("no client detail on %v", err)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.want)
			}
		})
	}
}

// validateHeader's own rejections — as opposed to the validateArchivePath
// call it delegates to first, covered above — are built purely from hdr's
// caller-submitted fields and fixed strings. This exercises each one.
func TestValidateHeaderMarksEveryRejectionWithClientDetail(t *testing.T) {
	base := func() *tar.Header {
		return &tar.Header{Name: "file.txt", Typeflag: tar.TypeReg, Size: 2}
	}
	tests := map[string]struct {
		header func() *tar.Header
		want   string
	}{
		"xattrs not supported": {
			header: func() *tar.Header {
				h := base()
				h.Xattrs = map[string]string{"user.x": "y"}
				return h
			},
			want: "PAX and extended metadata are not supported",
		},
		"pax path mismatch": {
			header: func() *tar.Header {
				h := base()
				h.PAXRecords = map[string]string{"path": "other"}
				return h
			},
			want: "inconsistent PAX path metadata",
		},
		"pax linkpath on non-symlink": {
			header: func() *tar.Header {
				h := base()
				h.PAXRecords = map[string]string{"linkpath": "x"}
				return h
			},
			want: "PAX linkpath is only permitted for symlink entries",
		},
		"pax linkpath mismatch": {
			header: func() *tar.Header {
				h := base()
				h.Typeflag = tar.TypeSymlink
				h.Size = 0
				h.Linkname = "target"
				h.PAXRecords = map[string]string{"linkpath": "other"}
				return h
			},
			want: "inconsistent PAX link metadata",
		},
		"pax unsupported key": {
			header: func() *tar.Header {
				h := base()
				h.PAXRecords = map[string]string{"mtime": "1"}
				return h
			},
			want: `unsupported PAX metadata "mtime"`,
		},
		"setuid or setgid": {
			header: func() *tar.Header {
				h := base()
				h.Mode |= 04000
				return h
			},
			want: "setuid or setgid bits are not permitted",
		},
		"negative size": {
			header: func() *tar.Header {
				h := base()
				h.Size = -1
				return h
			},
			want: "has negative size",
		},
		"non-regular declares content": {
			header: func() *tar.Header {
				h := base()
				h.Typeflag = tar.TypeDir
				h.Size = 1
				return h
			},
			want: "declares content",
		},
		"unsupported entry type": {
			header: func() *tar.Header {
				h := base()
				h.Typeflag = tar.TypeBlock
				h.Size = 0
				return h
			},
			want: "unsupported tar entry type",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateHeader(test.header())
			if err == nil {
				t.Fatal("expected validateHeader to reject the header")
			}
			detail, ok := ClientDetail(err)
			if !ok {
				t.Fatalf("no client detail on %v", err)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.want)
			}
		})
	}
}

// This is the end-to-end regression test: it proves the mark on
// validateArchivePath survives the whole BatchSealer.Upload path, including
// classifyArchiveFailure's errors.Join(ErrInvalidArchive, err) — the exact
// site that used to erase the reason and leave every archive-shape mistake as
// a bare 400. The entry name reproduces `tar -cf x.tar .`'s "./" shape.
func TestBatchSealerUploadArchivePathRejectionSurvivesClassification(t *testing.T) {
	metadata := &sealerMetadataStore{}
	content := &sealerContentStore{events: &metadata.events, exists: map[Location]bool{}}
	locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
	registry := sealerRegistry{TypeRef("opaque/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		t.Fatal("validator must not run: archive capture should fail first")
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
	if err != nil {
		t.Fatal(err)
	}

	body := tarBytes(t, "./file.txt", "hi")
	reader := &sealerReadCloser{Reader: bytes.NewReader(body)}
	_, err = sealer.Upload(context.Background(), UploadRequest{
		TeamID: 1, TeamName: "main", UploadedBy: "Alice", Actor: "github:subject-1",
		IdempotencyKey: "upload-key", Type: TypeRef("opaque/v1"),
		OpenTar:        func(context.Context) (io.ReadCloser, error) { return reader, nil },
		SourceMetadata: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected Upload to reject the malformed archive")
	}
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("error = %v, want it classified as ErrInvalidArchive", err)
	}
	detail, ok := ClientDetail(err)
	if !ok {
		t.Fatalf("no client detail on %v — classifyArchiveFailure must not strip the mark", err)
	}
	if !strings.Contains(detail, "contains an empty, dot, or traversal segment") {
		t.Fatalf("detail = %q", detail)
	}
}

// cleanSymlinkTarget is validateArchivePath's near-twin applied to a
// symlink's target instead of its name, reached from the same capture loop
// (extractTar and ValidateArchiveLimits both call it at the point a symlink
// header is seen). An out-of-tree symlink in `tar -cf` output is the same
// F5/F6 class as a "./" entry.
func TestCleanSymlinkTargetMarksEveryRejectionWithClientDetail(t *testing.T) {
	tests := map[string]struct {
		name   string
		target string
		want   string
	}{
		"empty target":  {name: "link", target: "", want: "has an empty target"},
		"too long":      {name: "link", target: strings.Repeat("a", int(MaxSnapshotSymlinkTargetBytes)+1), want: "target exceeds"},
		"backslash":     {name: "link", target: `b\c`, want: "contains a backslash"},
		"absolute":      {name: "link", target: "/etc/passwd", want: "target is absolute"},
		"drive-like":    {name: "link", target: "C:/b", want: "target is drive-like"},
		"escapes root":  {name: "link", target: "../../etc/passwd", want: "target escapes the archive root"},
		"nested escape": {name: "a/b/link", target: "../../../escape", want: "target escapes the archive root"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := cleanSymlinkTarget(test.name, test.target)
			if err == nil {
				t.Fatal("expected cleanSymlinkTarget to reject the target")
			}
			detail, ok := ClientDetail(err)
			if !ok {
				t.Fatalf("no client detail on %v", err)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.want)
			}
		})
	}
}

// planMaterialization's "conflicts with an already materialized path"
// rejection is reached from the same extraction loop as validateArchivePath
// and cleanSymlinkTarget, and is built purely from the caller's own entry
// name.
func TestPlanMaterializationConflictMarksClientDetail(t *testing.T) {
	materialized := map[string]capturedEntry{
		"a": {name: "a", kind: extractedRegular},
	}
	_, err := planMaterialization("a", extractedSymlink, materialized)
	if err == nil {
		t.Fatal("expected planMaterialization to reject the kind conflict")
	}
	detail, ok := ClientDetail(err)
	if !ok {
		t.Fatalf("no client detail on %v", err)
	}
	if !strings.Contains(detail, `path "a" conflicts with an already materialized path`) {
		t.Fatalf("detail = %q", detail)
	}
}

// ValidateArchiveLimits is the second, independent validation pass run over
// the canonical spooled bytes before content storage (see
// atc/worker/jetbridge/snapshot_content_store.go's SnapshotContentStore.Put),
// so its rejections are just as reachable from a real request as
// Canonicalizer.Capture's. This covers its two entry-shape rejections that
// were left unmarked in the original pass.
func TestValidateArchiveLimitsMarksEntryShapeRejectionsWithClientDetail(t *testing.T) {
	limits := ArchiveLimits{MaxContentBytes: 1 << 20, MaxEntries: 100}

	t.Run("duplicate canonical path", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i := 0; i < 2; i++ {
			if err := tw.WriteHeader(&tar.Header{Name: "dup.txt", Mode: 0o600, Size: 2, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("hi")); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}

		err := ValidateArchiveLimits(context.Background(), &buf, limits)
		if err == nil {
			t.Fatal("expected ValidateArchiveLimits to reject the duplicate entry")
		}
		detail, ok := ClientDetail(err)
		if !ok {
			t.Fatalf("no client detail on %v", err)
		}
		if !strings.Contains(detail, `duplicate canonical path "dup.txt"`) {
			t.Fatalf("detail = %q", detail)
		}
	})

	t.Run("trailing data after terminator", func(t *testing.T) {
		body := tarBytes(t, "file.txt", "hi")
		body = append(body, 'x') // one stray byte after the two-zero-block trailer

		err := ValidateArchiveLimits(context.Background(), bytes.NewReader(body), limits)
		if err == nil {
			t.Fatal("expected ValidateArchiveLimits to reject the trailing byte")
		}
		detail, ok := ClientDetail(err)
		if !ok {
			t.Fatalf("no client detail on %v", err)
		}
		if !strings.Contains(detail, "contains trailing data after the tar terminator") {
			t.Fatalf("detail = %q", detail)
		}
	})
}
