package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func contentDigest(body string) Digest {
	return Digest(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))))
}

const exposedRecordBody = `{"schema_version":"1.0.0"}`
const exposedNoteBody = "a note\n"

// exposedTree is a canonical archive holding one record, one note, one
// directory and one symlink — enough to exercise every refusal below.
func exposedTree(t *testing.T) []byte {
	t.Helper()
	return canonicalBody(t, makeTar(t, []tarEntry{
		{name: "docs", typeflag: tar.TypeDir},
		{name: "docs/note.txt", typeflag: tar.TypeReg, content: exposedNoteBody},
		{name: "link", typeflag: tar.TypeSymlink, linkname: "record.json"},
		{name: "record.json", typeflag: tar.TypeReg, content: exposedRecordBody},
	}))
}

func TestVerifyExposedPathsAcceptsTruthAndRefusesEveryDisagreement(t *testing.T) {
	t.Parallel()

	archive := exposedTree(t)
	tree := mustTestDigest(t)

	tests := []struct {
		name  string
		paths []ExposedPath
		want  string
	}{
		{
			name: "every claimed digest matches",
			paths: []ExposedPath{
				{Path: "docs/note.txt", Digest: contentDigest(exposedNoteBody)},
				{Path: "record.json", Digest: contentDigest(exposedRecordBody)},
			},
		},
		{
			name:  "one claimed digest is wrong",
			paths: []ExposedPath{{Path: "record.json", Digest: contentDigest(exposedNoteBody)}},
			want:  `exposed path "record.json" hashes to`,
		},
		{
			name:  "claimed path is absent",
			paths: []ExposedPath{{Path: "missing.json", Digest: contentDigest(exposedRecordBody)}},
			want:  `exposed path "missing.json" is absent from the exposed tree`,
		},
		{
			name:  "claimed path sorts after every entry",
			paths: []ExposedPath{{Path: "zzz.json", Digest: contentDigest(exposedRecordBody)}},
			want:  `exposed path "zzz.json" is absent from the exposed tree`,
		},
		{
			name:  "claimed path is a directory",
			paths: []ExposedPath{{Path: "docs", Digest: contentDigest("")}},
			want:  `exposed path "docs" is not a regular file`,
		},
		{
			name:  "claimed path is a symlink",
			paths: []ExposedPath{{Path: "link", Digest: contentDigest("record.json")}},
			want:  `exposed path "link" is not a regular file`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exposure, err := NewStaticSelectorExposure("/tmp/build/plan/base", tree, tt.paths...)
			if err != nil {
				t.Fatalf("NewStaticSelectorExposure() error = %v", err)
			}
			err = VerifyExposedPaths(context.Background(), bytes.NewReader(archive), exposure)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("VerifyExposedPaths() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("VerifyExposedPaths() error = %v, want substring %q", err, tt.want)
			}
			if !errors.Is(err, ErrExposureMismatch) {
				t.Fatalf("VerifyExposedPaths() error = %v, want ErrExposureMismatch", err)
			}
		})
	}
}

func TestVerifyExposedPathsIgnoresFullMaterialization(t *testing.T) {
	t.Parallel()

	// A full-tree exposure enumerates nothing, so there is nothing to recompute
	// and the reader must never be consumed as if there were.
	exposure := FullTreeExposure("/tmp/build/plan/base", mustTestDigest(t))
	if err := VerifyExposedPaths(context.Background(), bytes.NewReader(nil), exposure); err != nil {
		t.Fatalf("VerifyExposedPaths(full) error = %v, want nil", err)
	}
}

// TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent is the
// corrupted-store case: metadata authorizes the exact input reference, and the
// content store hands back a tree whose bytes do not match the per-path digest
// the exposure claims. The seal must refuse, and it must refuse BEFORE it stages,
// uploads or commits anything.
func TestSealRefusesAStaticSelectorExposureThatDisagreesWithTheStoredContent(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	archive := exposedTree(t)
	inputDigest := mustTestDigest(t)
	inputRef := SnapshotRef{ID: 41, Type: TypeRef("repository/v1"), Digest: inputDigest}

	newSealer := func() (*BatchSealer, *sealerMetadataStore, *sealerContentStore, *sealerLocks) {
		metadata := &sealerMetadataStore{authorized: map[SnapshotID]Snapshot{41: {
			ID: 41, Type: inputRef.Type, Digest: inputRef.Digest,
			ByteSize: int64(len(archive)), FileCount: 4, Representation: "application/x-tar",
			ContentState: ContentStateAvailable, CreatedAt: now,
		}}}
		content := &sealerContentStore{
			events:      &metadata.events,
			exists:      map[Location]bool{},
			openContent: map[SnapshotID][]byte{41: archive},
		}
		locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
		sealer := mustNewSealer(t, t.TempDir(), metadata, content, locks,
			sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{}, nil
			}),
			WithBatchSealerClock(func() time.Time { return now }),
		)
		return sealer, metadata, content, locks
	}

	request := func(paths ...ExposedPath) SealRequest {
		t.Helper()
		exposure, err := NewStaticSelectorExposure("/tmp/build/plan/base", inputDigest, paths...)
		if err != nil {
			t.Fatalf("NewStaticSelectorExposure() error = %v", err)
		}
		value := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", tarBytes(t, "value", "output"))})
		value.InputOrder = []string{"base"}
		value.Inputs = map[string]SnapshotRef{"base": inputRef}
		value.InputExposures = map[string]InputExposure{"base": exposure}
		return value
	}

	t.Run("a truthful exposure seals", func(t *testing.T) {
		sealer, metadata, _, _ := newSealer()
		if _, err := sealer.Seal(context.Background(), request(
			ExposedPath{Path: "record.json", Digest: contentDigest(exposedRecordBody)},
		)); err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		if metadata.commit == nil {
			t.Fatal("a truthful exposure did not commit")
		}
	})

	t.Run("a corrupted store refuses before any storage", func(t *testing.T) {
		sealer, metadata, content, locks := newSealer()
		_, err := sealer.Seal(context.Background(), request(
			ExposedPath{Path: "record.json", Digest: mustOtherDigest(t)},
		))
		if err == nil || !errors.Is(err, ErrExposureMismatch) {
			t.Fatalf("Seal() error = %v, want ErrExposureMismatch", err)
		}
		if !strings.Contains(err.Error(), `exposed path "record.json" hashes to`) {
			t.Fatalf("Seal() error = %v, want the operator-actionable path and digests", err)
		}
		if len(locks.acquired) != 0 {
			t.Fatalf("a refused exposure acquired %d digest leases", len(locks.acquired))
		}
		if len(metadata.stages) != 0 || metadata.commit != nil || content.putCalls != 0 {
			t.Fatalf("a refused exposure reached storage: stages=%d commit=%v puts=%d",
				len(metadata.stages), metadata.commit != nil, content.putCalls)
		}
	})
}
