package projection_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type reviewProjectionStore struct {
	input      projection.ReviewInput
	found      bool
	findErr    error
	candidates []snapshot.SnapshotRef
	upsertErr  error
	upserts    []*reviews.StoredReview
}

func (s *reviewProjectionStore) FindReviewInput(context.Context, snapshot.SnapshotID) (projection.ReviewInput, bool, error) {
	return s.input, s.found, s.findErr
}

func (s *reviewProjectionStore) ListUnprojectedReviews(context.Context, int) ([]snapshot.SnapshotRef, error) {
	return append([]snapshot.SnapshotRef(nil), s.candidates...), nil
}

func (s *reviewProjectionStore) UpsertReviewProjection(_ context.Context, review *reviews.StoredReview) error {
	cp := *review
	cp.Review = append(json.RawMessage(nil), review.Review...)
	s.upserts = append(s.upserts, &cp)
	return s.upsertErr
}

type reviewContent struct {
	data []byte
	err  error
}

func (c reviewContent) Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
	if c.err != nil {
		return nil, c.err
	}
	return io.NopCloser(bytes.NewReader(c.data)), nil
}

type interruptedReviewContent struct {
	data      []byte
	failAfter int
}

func (c interruptedReviewContent) Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
	return &interruptedReviewReader{reader: bytes.NewReader(c.data), remaining: c.failAfter}, nil
}

type interruptedReviewReader struct {
	reader    *bytes.Reader
	remaining int
}

func (reader *interruptedReviewReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, errors.New("replica stream interrupted")
	}
	if len(buffer) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	n, err := reader.reader.Read(buffer)
	reader.remaining -= n
	return n, err
}

func (*interruptedReviewReader) Close() error { return nil }

func TestReviewProjectorRevalidatesIdentityAndDerivesProjection(t *testing.T) {
	reviewJSON := validProjectedReview(t)
	archive := reviewArchive(t, reviewJSON)
	manifest := projectedManifest(41, archive)
	runID := snapshot.WorkflowRunID(9)
	store := &reviewProjectionStore{
		found: true,
		input: projection.ReviewInput{
			Snapshot: manifest, ProductionID: 77, WorkflowRunID: &runID,
			BuildName: "3", TeamName: "main",
			PipelineName: "agent", JobName: "review",
			SubmittedBy: "review-agent",
		},
	}
	projector, err := projection.NewReviewProjector(store, reviewContent{data: archive})
	if err != nil {
		t.Fatal(err)
	}

	if err := projector.Project(context.Background(), refFor(manifest)); err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.SnapshotID != manifest.ID || got.ProductionID == nil || *got.ProductionID != 77 {
		t.Fatalf("snapshot/production linkage = %#v/%#v", got.SnapshotID, got.ProductionID)
	}
	if got.WorkflowRunID == nil || *got.WorkflowRunID != runID {
		t.Fatalf("workflow_run_id = %#v", got.WorkflowRunID)
	}
	// The record's judgment is projected verbatim. A numeric score derived from
	// the conclusion would be a second description of the same verdict.
	if got.Conclusion != "accept" || got.Summary != "reviewed" {
		t.Fatalf("projected judgment = %#v", got)
	}
	if len(got.SeverityCounts) != 2 || got.SeverityCounts["low"] != 1 || got.SeverityCounts["observation"] != 1 {
		t.Fatalf("severity counts = %v", got.SeverityCounts)
	}
	if !bytes.Equal(got.Review, reviewJSON) {
		t.Fatalf("stored review differs from canonical record.json\ngot:  %s\nwant: %s", got.Review, reviewJSON)
	}
}

// review/v1 names its subjects by snapshot identity. A subject's type, digest
// and entity ID are not a repository URL, a commit SHA and a branch, so the
// projection has no field to put them in and invents none.
func TestReviewProjectionCarriesNoRepositoryCoordinates(t *testing.T) {
	projected := reflect.TypeOf(reviews.StoredReview{})
	for _, gone := range []string{"Repo", "CommitSha", "Branch", "Score", "MaxScore", "Pass", "AgentModel", "DurationSeconds", "TicketID", "PipelineRunID"} {
		if _, found := projected.FieldByName(gone); found {
			t.Errorf("StoredReview.%s has no writer and must not exist", gone)
		}
	}
}

func TestReviewProjectorRejectsMismatchedTypeAndDigestBeforeUpsert(t *testing.T) {
	archive := reviewArchive(t, validProjectedReview(t))
	manifest := projectedManifest(42, archive)
	store := &reviewProjectionStore{found: true, input: projection.ReviewInput{Snapshot: manifest, ProductionID: 1, TeamName: "main"}}
	projector, err := projection.NewReviewProjector(store, reviewContent{data: archive})
	if err != nil {
		t.Fatal(err)
	}

	wrongType := refFor(manifest)
	wrongType.Type = "review/v2"
	if err := projector.Project(context.Background(), wrongType); err == nil {
		t.Fatal("mismatched type projected")
	}
	wrongDigest := refFor(manifest)
	wrongDigest.Digest = testDigest('f')
	if err := projector.Project(context.Background(), wrongDigest); err == nil {
		t.Fatal("mismatched digest projected")
	}
	if len(store.upserts) != 0 {
		t.Fatalf("invalid identities upserted %d rows", len(store.upserts))
	}
}

func TestReviewProjectorRejectsUnavailableAndCorruptCanonicalContent(t *testing.T) {
	archive := reviewArchive(t, validProjectedReview(t))
	manifest := projectedManifest(43, archive)

	t.Run("unavailable", func(t *testing.T) {
		store := &reviewProjectionStore{found: true, input: projection.ReviewInput{Snapshot: manifest, ProductionID: 1, TeamName: "main"}}
		projector, err := projection.NewReviewProjector(store, reviewContent{err: errors.New("object store offline")})
		if err != nil {
			t.Fatal(err)
		}
		if err := projector.Project(context.Background(), refFor(manifest)); !errors.Is(err, snapshot.ErrContentUnavailable) {
			t.Fatalf("error = %v, want ErrContentUnavailable", err)
		}
		if len(store.upserts) != 0 {
			t.Fatal("unavailable content was projected")
		}
	})

	t.Run("stream interrupted", func(t *testing.T) {
		store := &reviewProjectionStore{found: true, input: projection.ReviewInput{Snapshot: manifest, ProductionID: 1, TeamName: "main"}}
		projector, err := projection.NewReviewProjector(store, interruptedReviewContent{data: archive, failAfter: len(archive) / 2})
		if err != nil {
			t.Fatal(err)
		}
		if err := projector.Project(context.Background(), refFor(manifest)); !errors.Is(err, snapshot.ErrContentUnavailable) {
			t.Fatalf("error = %v, want ErrContentUnavailable", err)
		}
		if len(store.upserts) != 0 {
			t.Fatal("interrupted content was projected")
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		corrupt := append([]byte(nil), archive...)
		corrupt[0] ^= 0xff
		store := &reviewProjectionStore{found: true, input: projection.ReviewInput{Snapshot: manifest, ProductionID: 1, TeamName: "main"}}
		projector, err := projection.NewReviewProjector(store, reviewContent{data: corrupt})
		if err != nil {
			t.Fatal(err)
		}
		if err := projector.Project(context.Background(), refFor(manifest)); !errors.Is(err, projection.ErrCorruptSnapshot) {
			t.Fatalf("error = %v, want ErrCorruptSnapshot", err)
		}
		if len(store.upserts) != 0 {
			t.Fatal("corrupt content was projected")
		}
	})
}

func refFor(manifest snapshot.Snapshot) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
}

func projectedManifest(id snapshot.SnapshotID, archive []byte) snapshot.Snapshot {
	digest := sha256.Sum256(archive)
	return snapshot.Snapshot{
		ID: id, Type: "review/v1", Digest: snapshot.Digest("sha256:" + hex.EncodeToString(digest[:])),
		ByteSize: int64(len(archive)), FileCount: 1, Representation: "filesystem-tree-v1",
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
}

func reviewArchive(t *testing.T, document []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	if err := w.WriteHeader(&tar.Header{Name: "record.json", Mode: 0600, Size: int64(len(document)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(document); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func validProjectedReview(t *testing.T) []byte {
	t.Helper()
	line := 1
	subject := snapshot.SnapshotRef{
		ID: 1, Type: "repository-change/v1", Digest: testDigest('a'),
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", subject,
		)},
		contracts.ReviewBody{
			Conclusion: "accept", Summary: "reviewed",
			Findings: []contracts.Finding{
				{
					ID: "issue-1", Severity: "low", Category: "correctness",
					Title: "issue", Description: "issue detail",
					Evidence: []contracts.Anchor{{
						Subject: "primary",
						Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: &line, End: &line},
					}},
				},
				{
					ID: "observation-1", Severity: "observation", Category: "maintainability",
					Title: "observation", Description: "observation detail",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
