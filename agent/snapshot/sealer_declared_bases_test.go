package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestBatchSealerUploadSealsAgainstAnAuthorizedDeclaredBase proves the direct-
// create path can now do what only a node with a declared input could do
// before: hand a seal-time validator a real, authorized, reopenable base.
// repository-change/v1 is exactly this shape in production — its gate reopens
// the base subject's input to re-canonicalize it and verify lineage — so the
// fake validator here reproduces that same "look the base up, open it, read
// it" behavior rather than accepting an empty ValidationContext.
func TestBatchSealerUploadSealsAgainstAnAuthorizedDeclaredBase(t *testing.T) {
	now := time.Date(2026, time.August, 4, 9, 0, 0, 0, time.UTC)
	baseBody := tarBytes(t, "base.txt", "base repository content")
	baseDigest := canonicalDigest(t, baseBody)
	baseManifest := Snapshot{
		ID: 42, Type: TypeRef("repository/v1"), Digest: baseDigest,
		ByteSize: int64(len(canonicalBody(t, baseBody))), FileCount: 1,
		Representation: "application/x-tar", ContentState: ContentStateAvailable, CreatedAt: now,
	}

	candidateBody := tarBytes(t, "record.json", `{"repository_id":"r"}`)
	candidateDigest := canonicalDigest(t, candidateBody)
	candidateManifest := Snapshot{
		ID: 43, Type: TypeRef("repository-change/v1"), Digest: candidateDigest,
		ByteSize: int64(len(canonicalBody(t, candidateBody))), FileCount: 1,
		Representation: "application/x-tar", IntrinsicMetadata: json.RawMessage(`{"verified_base":true}`),
		ContentState: ContentStateAvailable, CreatedAt: now,
	}

	metadata := &sealerMetadataStore{
		authorized: map[SnapshotID]Snapshot{
			baseManifest.ID:      baseManifest,
			candidateManifest.ID: candidateManifest,
		},
		commitResult: map[string]SealedOutput{
			"upload": {
				Port:     Port{Name: "snapshot", Type: candidateManifest.Type},
				Snapshot: SnapshotRef{ID: candidateManifest.ID, Type: candidateManifest.Type, Digest: candidateManifest.Digest},
			},
		},
	}
	events := &metadata.events
	content := &sealerContentStore{
		events: events, exists: map[Location]bool{},
		openContent: map[SnapshotID][]byte{baseManifest.ID: canonicalBody(t, baseBody)},
	}
	locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}

	openedBase := 0
	registry := sealerRegistry{TypeRef("repository-change/v1"): sealerValidatorFunc(func(ctx context.Context, _ *os.Root, declarations ValidationContext) (ValidationResult, error) {
		ref, found := declarations.Input("base")
		if !found {
			t.Fatal("declared base \"base\" was not exposed to the validator")
		}
		if ref != (SnapshotRef{ID: baseManifest.ID, Type: baseManifest.Type, Digest: baseManifest.Digest}) {
			t.Fatalf("exposed base ref = %#v, want the authorized base manifest", ref)
		}
		reader, err := declarations.OpenInput(ctx, "base")
		if err != nil {
			t.Fatalf("OpenInput(base) error = %v", err)
		}
		openedBase++
		opened, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read declared base: read=%v close=%v", err, closeErr)
		}
		if !bytes.Equal(opened, canonicalBody(t, baseBody)) {
			t.Fatalf("declared base content did not match the authorized base's canonical bytes")
		}
		return ValidationResult{IntrinsicMetadata: json.RawMessage(`{"verified_base":true}`)}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks,
		WithBatchSealerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}

	reader := &sealerReadCloser{Reader: bytes.NewReader(candidateBody)}
	got, err := sealer.Upload(context.Background(), UploadRequest{
		TeamID: 1, TeamName: "main", UploadedBy: "Alice", Actor: "github:subject-1",
		IdempotencyKey: "upload-with-base", Type: TypeRef("repository-change/v1"),
		OpenTar: func(context.Context) (io.ReadCloser, error) { return reader, nil },
		Bases:   map[string]SnapshotID{"base": baseManifest.ID},
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if got.ID != candidateManifest.ID || got.Digest != candidateManifest.Digest {
		t.Fatalf("Upload() = %#v, want the sealed candidate manifest", got)
	}
	if openedBase != 1 {
		t.Fatalf("declared base was opened %d times, want exactly 1", openedBase)
	}
}

// TestBatchSealerUploadRejectsDeclaredBaseTheTeamCannotRead proves
// authorization happens up front: a base the caller's team does not own is
// rejected before the validator ever runs and before the candidate archive is
// even opened, not discovered lazily inside AdmitForSeal.
func TestBatchSealerUploadRejectsDeclaredBaseTheTeamCannotRead(t *testing.T) {
	metadata := &sealerMetadataStore{authorized: map[SnapshotID]Snapshot{}}
	events := &metadata.events
	content := &sealerContentStore{events: events, exists: map[Location]bool{}}
	locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}

	validatorCalled := false
	registry := sealerRegistry{TypeRef("repository-change/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		validatorCalled = true
		return ValidationResult{}, nil
	})}
	sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
	if err != nil {
		t.Fatal(err)
	}

	opened := 0
	_, err = sealer.Upload(context.Background(), UploadRequest{
		TeamID: 1, TeamName: "main", UploadedBy: "Alice", Actor: "github:subject-1",
		IdempotencyKey: "upload-unauthorized-base", Type: TypeRef("repository-change/v1"),
		OpenTar: func(context.Context) (io.ReadCloser, error) {
			opened++
			return io.NopCloser(bytes.NewReader(tarBytes(t, "record.json", "{}"))), nil
		},
		Bases: map[string]SnapshotID{"base": 999},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Upload() error = %v, want ErrNotFound", err)
	}
	if validatorCalled {
		t.Fatal("validator ran before the declared base was authorized")
	}
	if opened != 0 {
		t.Fatalf("candidate archive was opened %d times before authorization failed, want 0", opened)
	}
	if len(metadata.stages) != 0 || content.putCalls != 0 {
		t.Fatalf("unauthorized base performed durable work: stages=%d puts=%d", len(metadata.stages), content.putCalls)
	}
}

// TestBatchSealerUploadRejectsMalformedDeclaredBase covers the two ways a
// declared base can be malformed rather than merely unauthorized: an empty
// port name, and a non-positive snapshot ID. Both must fail before any I/O.
func TestBatchSealerUploadRejectsMalformedDeclaredBase(t *testing.T) {
	cases := map[string]map[string]SnapshotID{
		"empty base name":       {"": 42},
		"non-positive snapshot": {"base": 0},
	}
	for name, bases := range cases {
		t.Run(name, func(t *testing.T) {
			metadata := &sealerMetadataStore{authorized: map[SnapshotID]Snapshot{42: {
				ID: 42, Type: TypeRef("repository/v1"), Digest: mustOtherDigest(t),
				ByteSize: 1, FileCount: 1, Representation: "application/x-tar",
				ContentState: ContentStateAvailable, CreatedAt: time.Now(),
			}}}
			events := &metadata.events
			content := &sealerContentStore{events: events, exists: map[Location]bool{}}
			locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
			registry := sealerRegistry{TypeRef("repository-change/v1"): sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				t.Fatal("validator ran with a malformed declared base")
				return ValidationResult{}, nil
			})}
			sealer, err := NewBatchSealer(Canonicalizer{TempDir: t.TempDir()}, registry, metadata, content, locks)
			if err != nil {
				t.Fatal(err)
			}
			opened := 0
			_, err = sealer.Upload(context.Background(), UploadRequest{
				TeamID: 1, TeamName: "main", UploadedBy: "Alice", Actor: "github:subject-1",
				IdempotencyKey: "upload-malformed-base-" + name, Type: TypeRef("repository-change/v1"),
				OpenTar: func(context.Context) (io.ReadCloser, error) {
					opened++
					return io.NopCloser(bytes.NewReader(tarBytes(t, "record.json", "{}"))), nil
				},
				Bases: bases,
			})
			if err == nil {
				t.Fatal("Upload() accepted a malformed declared base")
			}
			if opened != 0 {
				t.Fatalf("candidate archive was opened %d times before validation failed, want 0", opened)
			}
		})
	}
}

// TestUploadRequestClonesDeclaredBasesWithoutAliasing matches the aliasing
// discipline every other UploadRequest map/slice field already has: mutating
// a clone's Bases must never mutate the original request's.
func TestUploadRequestClonesDeclaredBasesWithoutAliasing(t *testing.T) {
	request := UploadRequest{
		TeamID: 1, TeamName: "main", UploadedBy: "Alice", Actor: "github:subject-1",
		IdempotencyKey: "clone-check", Type: TypeRef("repository-change/v1"),
		OpenTar: func(context.Context) (io.ReadCloser, error) { return nil, nil },
		Bases:   map[string]SnapshotID{"base": 42},
	}
	clone := request.Clone()
	clone.Bases["base"] = 7
	clone.Bases["extra"] = 8
	if request.Bases["base"] != 42 || len(request.Bases) != 1 {
		t.Fatalf("Clone() aliased Bases: original = %#v", request.Bases)
	}
}
