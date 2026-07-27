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

// ReviewInput is immutable snapshot identity plus the production occurrence
// that authorizes the projection. It carries no build/pipeline/job/publisher
// names: those describe WHERE one occurrence happened, a review snapshot can
// have several, and every read resolves them from the production row it
// selected. TeamName is here as the ownership check — a production that
// resolved no team is not an authorization the projector may act on.
type ReviewInput struct {
	Snapshot      snapshot.Snapshot
	ProductionID  snapshot.DatabaseID
	WorkflowRunID *snapshot.WorkflowRunID
	TeamName      string
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

	productionID := input.ProductionID
	record := &reviews.StoredReview{
		// The record's judgment, verbatim. review/v1 states a conclusion; it
		// does not state a score, and projecting one onto a 0..1 pass scale
		// invented a second, weaker description of the same verdict — one the
		// build page then rendered as a green/red badge that could disagree
		// with the findings underneath it.
		Conclusion:     sealed.Body.Conclusion,
		Summary:        sealed.Body.Summary,
		SeverityCounts: reviewSeverityCounts(sealed.Body.Findings),

		// Repository coordinates are absent on purpose. review/v1 carries
		// subjects, which name what was reviewed by snapshot type and digest.
		// A subject's type is not a clone URL, its content digest is not a
		// commit SHA, and its entity ID ("primary", "change") is not a branch;
		// neither the record body nor the production occurrence carries
		// repository coordinates, so there is nothing faithful to write.
		Review:     append(json.RawMessage(nil), recordJSON...),
		SnapshotID: manifest.ID, WorkflowRunID: input.WorkflowRunID, ProductionID: &productionID,
	}
	if err := projector.store.UpsertReviewProjection(ctx, record); err != nil {
		return fmt.Errorf("upsert review projection: %w", err)
	}
	return nil
}

// reviewSeverityCounts tallies findings by review/v1 severity. A severity with
// no findings is absent rather than zero: the map says what the record
// contains, and an explicit 0 would read as a claim the reviewer looked and
// found none.
func reviewSeverityCounts(findings []contracts.Finding) map[string]int {
	counts := map[string]int{}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	return counts
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
