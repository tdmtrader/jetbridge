package projection

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const maxReviewDocumentBytes int64 = 1 << 20

// ReviewInput is immutable snapshot identity plus the selected production
// occurrence used only for legacy/build presentation linkage. Snapshot bytes,
// not these denormalized fields, remain the review's canonical truth.
type ReviewInput struct {
	Snapshot      snapshot.Snapshot
	ProductionID  snapshot.DatabaseID
	WorkflowRunID *snapshot.WorkflowRunID
	BuildID       *int
	BuildName     string
	TeamName      string
	PipelineName  string
	JobName       string
	PipelineRunID *int
	SubmittedBy   string
}

type ReviewStore interface {
	FindReviewInput(context.Context, snapshot.SnapshotID) (ReviewInput, bool, error)
	ListUnprojectedReviews(context.Context, int) ([]snapshot.SnapshotRef, error)
	UpsertReviewProjection(context.Context, *reviews.StoredReview) error
}

type ReviewContent interface {
	Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
}

type ReviewProjector struct {
	store   ReviewStore
	content ReviewContent
}

func NewReviewProjector(store ReviewStore, content ReviewContent) (*ReviewProjector, error) {
	if store == nil {
		return nil, fmt.Errorf("projection: review store is required")
	}
	if content == nil {
		return nil, fmt.Errorf("projection: review content store is required")
	}
	return &ReviewProjector{store: store, content: content}, nil
}

func (projector *ReviewProjector) SnapshotType() snapshot.TypeRef { return "review/v1" }

func (projector *ReviewProjector) Candidates(ctx context.Context, limit int) ([]snapshot.SnapshotRef, error) {
	return projector.store.ListUnprojectedReviews(ctx, limit)
}

func (projector *ReviewProjector) Project(ctx context.Context, ref snapshot.SnapshotRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Type != projector.SnapshotType() {
		return fmt.Errorf("%w: review projector requires exact type %q, got %q", snapshot.ErrUnsupportedType, projector.SnapshotType(), ref.Type)
	}
	input, found, err := projector.store.FindReviewInput(ctx, ref.ID)
	if err != nil {
		return fmt.Errorf("load review projection input: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: snapshot %s", snapshot.ErrNotFound, ref.ID)
	}
	manifest := input.Snapshot
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("%w: invalid persisted manifest: %v", ErrCorruptSnapshot, err)
	}
	if manifest.ID != ref.ID || manifest.Type != ref.Type || manifest.Digest != ref.Digest {
		return fmt.Errorf("%w: persisted snapshot identity does not match sealed reference", ErrCorruptSnapshot)
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return fmt.Errorf("%w: snapshot %s is %s", snapshot.ErrContentUnavailable, manifest.ID, manifest.ContentState)
	}
	if input.ProductionID <= 0 || strings.TrimSpace(input.TeamName) == "" {
		return fmt.Errorf("projection: review snapshot %s has no valid production context", manifest.ID)
	}

	reader, err := projector.content.Open(ctx, manifest)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return fmt.Errorf("%w: open review snapshot %s: %v", snapshot.ErrContentUnavailable, manifest.ID, err)
	}
	if reader == nil {
		return fmt.Errorf("%w: review snapshot %s returned no content", snapshot.ErrContentUnavailable, manifest.ID)
	}
	recordJSON, readErr := readCanonicalReview(ctx, reader, manifest)
	closeErr := reader.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("%w: close review snapshot %s: %v", snapshot.ErrContentUnavailable, manifest.ID, closeErr)
	}
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}

	// The composed READ-TIME gate, not its two halves re-assembled here. Decoding
	// plus ReviewBody.Validate skipped the declared-schema layer entirely, so this
	// production read path judged a stored review by a weaker contract than the
	// one the platform sealed it under — and would have kept doing so silently.
	sealed, err := contracts.DecodeSealedReviewRecord(recordJSON)
	if err != nil {
		return fmt.Errorf("%w: invalid review/v1 record: %v", ErrCorruptSnapshot, err)
	}
	primary := primaryReviewSubject(sealed.Subjects)
	score, pass := reviewCompatibilityScore(sealed.Body.Conclusion)
	proven, observations := reviewFindingCounts(sealed.Body.Findings)

	snapshotID := manifest.ID
	productionID := input.ProductionID
	buildID := 0
	if input.BuildID != nil {
		buildID = *input.BuildID
	}
	record := &reviews.StoredReview{
		BuildID: buildID, BuildName: input.BuildName, TeamName: input.TeamName,
		PipelineName: input.PipelineName, JobName: input.JobName,
		Repo: primary.Type.String(), CommitSha: primary.Digest.String(), Branch: primary.ID,
		Score: score, MaxScore: 1, Pass: pass,
		ProvenCount: proven, ObservationCount: observations,
		Summary:         sealed.Body.Summary,
		DurationSeconds: 0, Review: append(json.RawMessage(nil), recordJSON...),
		PipelineRunID: input.PipelineRunID, SubmittedBy: input.SubmittedBy,
		SnapshotID: &snapshotID, WorkflowRunID: input.WorkflowRunID, ProductionID: &productionID,
	}
	if err := projector.store.UpsertReviewProjection(ctx, record); err != nil {
		return fmt.Errorf("upsert review projection: %w", err)
	}
	return nil
}

func primaryReviewSubject(subjects []contracts.Subject) contracts.Subject {
	for _, subject := range subjects {
		if subject.Role == contracts.SubjectRolePrimary {
			return subject
		}
	}
	return contracts.Subject{}
}

func reviewCompatibilityScore(conclusion string) (float64, bool) {
	switch conclusion {
	case "accept":
		return 1, true
	case "inconclusive":
		return 0.5, false
	default:
		return 0, false
	}
}

func reviewFindingCounts(findings []contracts.Finding) (int, int) {
	var substantive, observations int
	for _, finding := range findings {
		if finding.Severity == "observation" {
			observations++
		} else {
			substantive++
		}
	}
	return substantive, observations
}

func readCanonicalReview(ctx context.Context, reader io.Reader, manifest snapshot.Snapshot) ([]byte, error) {
	if manifest.ByteSize < 0 {
		return nil, fmt.Errorf("%w: negative archive size", ErrCorruptSnapshot)
	}
	hasher := sha256.New()
	limited := io.LimitReader(reader, manifest.ByteSize+1)
	counted := &countingReader{reader: io.TeeReader(limited, hasher)}
	archive := tar.NewReader(counted)
	var reviewJSON []byte
	var parseErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			parseErr = fmt.Errorf("read tar: %w", err)
			break
		}
		if header.Name != "record.json" {
			continue
		}
		if reviewJSON != nil {
			parseErr = fmt.Errorf("duplicate record.json")
			break
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			parseErr = fmt.Errorf("record.json is not a regular file")
			break
		}
		if header.Size < 0 || header.Size > maxReviewDocumentBytes {
			parseErr = fmt.Errorf("record.json exceeds %d bytes", maxReviewDocumentBytes)
			break
		}
		reviewJSON = make([]byte, header.Size)
		if _, err := io.ReadFull(archive, reviewJSON); err != nil {
			parseErr = fmt.Errorf("read record.json: %w", err)
			break
		}
	}
	_, drainErr := io.Copy(io.Discard, counted)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if drainErr != nil {
		return nil, fmt.Errorf("%w: read review archive: %v", snapshot.ErrContentUnavailable, drainErr)
	}
	if counted.count != manifest.ByteSize {
		return nil, fmt.Errorf("%w: archive size is %d, manifest declares %d", ErrCorruptSnapshot, counted.count, manifest.ByteSize)
	}
	actualDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actualDigest != manifest.Digest.String() {
		return nil, fmt.Errorf("%w: archive digest does not match manifest", ErrCorruptSnapshot)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptSnapshot, parseErr)
	}
	if reviewJSON == nil {
		return nil, fmt.Errorf("%w: record.json is missing", ErrCorruptSnapshot)
	}
	return reviewJSON, nil
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.count += int64(n)
	return n, err
}

var _ Handler = (*ReviewProjector)(nil)
