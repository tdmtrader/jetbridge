package publisher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var exactDefinitionHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReviewRunEvidence is one atomic, server-owned resolution of the workflow
// identity and the exact bindings publication relies on. Implementations must
// resolve it in one consistent read; composing independent binding lookups
// would admit a mutable/TOCTOU claim at this authority boundary.
type ReviewRunEvidence struct {
	TeamID                int
	WorkflowRunID         snapshot.WorkflowRunID
	WorkflowDefinitionID  int
	WorkflowName          string
	WorkflowVersion       int
	SchemaVersion         int
	DefinitionContentHash string
	CandidateInput        string
	Candidate             snapshot.SnapshotRef
	ReviewOutput          string
	Review                snapshot.SnapshotRef
}

func (evidence ReviewRunEvidence) validate(teamID int, runID snapshot.WorkflowRunID) error {
	if evidence.TeamID != teamID || evidence.WorkflowRunID != runID ||
		evidence.WorkflowDefinitionID <= 0 || evidence.WorkflowVersion <= 0 ||
		evidence.WorkflowName != "code-review" || evidence.SchemaVersion != 3 ||
		!exactDefinitionHashPattern.MatchString(evidence.DefinitionContentHash) ||
		evidence.CandidateInput != "after" ||
		evidence.Candidate.Validate() != nil || evidence.Candidate.Type != snapshot.TypeRef("repository/v1") ||
		evidence.ReviewOutput != "review" ||
		evidence.Review.Validate() != nil || evidence.Review.Type != snapshot.TypeRef("review/v1") {
		return fmt.Errorf("%w: code-review-v3 run evidence is invalid", ErrInvalidRequest)
	}
	return nil
}

type ReviewRunEvidenceResolver interface {
	ResolveReviewRunEvidence(
		context.Context,
		int,
		snapshot.WorkflowRunID,
	) (ReviewRunEvidence, bool, error)
}

// ReviewOutcomeReader is the exact authoritative workflow outcome lookup.
// agent/api/workflowoutcomes.Store and the DB factory already satisfy it.
type ReviewOutcomeReader interface {
	Get(
		context.Context,
		int,
		snapshot.WorkflowRunID,
		snapshot.SnapshotID,
	) (workflowoutcomes.Outcome, bool, error)
}

func (verifier *StoreEvidenceVerifier) verifyAcceptedReview(
	ctx context.Context,
	teamID int,
	request AcceptedReviewEvidenceRequest,
) (PublicationEvidence, error) {
	run, found, err := verifier.runs.ResolveReviewRunEvidence(
		ctx, teamID, request.ReviewWorkflowRunID,
	)
	if err != nil {
		return PublicationEvidence{}, fmt.Errorf("publisher: resolve exact review run evidence: %w", err)
	}
	if !found || run.validate(teamID, request.ReviewWorkflowRunID) != nil ||
		run.Review != request.Review || run.Candidate != request.Candidate {
		return PublicationEvidence{}, fmt.Errorf("%w: exact code-review-v3 run evidence was not found", ErrInvalidRequest)
	}

	review, err := verifier.inspectReview(ctx, teamID, request.Review)
	if err != nil {
		return PublicationEvidence{}, err
	}
	primary, found := exactPrimarySubject(review.Subjects)
	if !found || primary.Input != run.CandidateInput ||
		primary.Type != run.Candidate.Type || primary.Digest != run.Candidate.Digest ||
		review.Body.Conclusion != "accept" {
		return PublicationEvidence{}, fmt.Errorf("%w: review does not accept the exact primary candidate binding", ErrInvalidRequest)
	}

	validation, revision, err := verifier.inspectValidation(ctx, teamID, request.Validation)
	if err != nil {
		return PublicationEvidence{}, err
	}
	validationPrimary, found := exactPrimarySubject(validation.Subjects)
	if !found || revision != 3 || validation.Body.Conclusion != "passed" ||
		validationPrimary.Type != run.Candidate.Type ||
		validationPrimary.Digest != run.Candidate.Digest ||
		validation.Body.Attestation.CandidateDigest != run.Candidate.Digest {
		return PublicationEvidence{}, fmt.Errorf("%w: validation does not authoritatively pass the exact candidate", ErrInvalidRequest)
	}

	outcome, found, err := verifier.outcomes.Get(
		ctx, teamID, request.ReviewWorkflowRunID, request.Review.ID,
	)
	if err != nil {
		return PublicationEvidence{}, fmt.Errorf("publisher: read exact review outcome: %w", err)
	}
	if !found || outcome.Validate() != nil ||
		outcome.WorkflowRunID != request.ReviewWorkflowRunID ||
		outcome.OutputSnapshotID != request.Review.ID ||
		outcome.Disposition != workflowoutcomes.DispositionAccepted ||
		outcome.Revision != request.OutcomeRevision ||
		!boundedText(outcome.Actor, 256, false) ||
		!boundedEvidenceTime(outcome.AuditedAt) {
		return PublicationEvidence{}, fmt.Errorf("%w: exact accepted review outcome revision was not found", ErrInvalidRequest)
	}

	accepted := AcceptedReviewEvidence{
		Review:              request.Review,
		Candidate:           request.Candidate,
		Validation:          request.Validation,
		ReviewWorkflowRunID: request.ReviewWorkflowRunID,
		OutcomeRevision:     outcome.Revision,
		AcceptedBy:          outcome.Actor,
		AcceptedAt:          outcome.AuditedAt.UTC(),
	}
	evidence := PublicationEvidence{Kind: EvidenceAcceptedReview, AcceptedReview: &accepted}
	if err := evidence.Validate(); err != nil {
		return PublicationEvidence{}, err
	}
	return evidence, nil
}

func exactPrimarySubject(subjects []contracts.Subject) (contracts.Subject, bool) {
	var primary contracts.Subject
	found := false
	for _, subject := range subjects {
		if subject.Role != contracts.SubjectRolePrimary {
			continue
		}
		if found {
			return contracts.Subject{}, false
		}
		primary = subject
		found = true
	}
	return primary, found
}

func (verifier *StoreEvidenceVerifier) inspectReview(
	ctx context.Context,
	teamID int,
	ref snapshot.SnapshotRef,
) (contracts.Record[contracts.ReviewBody], error) {
	var record contracts.Record[contracts.ReviewBody]
	err := verifier.withExactEvidenceRoot(ctx, teamID, ref, func(root *os.Root) error {
		payload, err := readEvidenceRecord(root)
		if err != nil {
			return err
		}
		decoded, err := contracts.DecodeSealedReviewRecord(payload)
		if err != nil {
			return err
		}
		record = decoded
		return nil
	})
	if err != nil {
		return contracts.Record[contracts.ReviewBody]{}, fmt.Errorf("%w: exact sealed review is invalid: %w", ErrInvalidRequest, err)
	}
	return record, nil
}

func (verifier *StoreEvidenceVerifier) inspectValidation(
	ctx context.Context,
	teamID int,
	ref snapshot.SnapshotRef,
) (contracts.Record[contracts.ValidationBody], int, error) {
	var record contracts.Record[contracts.ValidationBody]
	err := verifier.withExactEvidenceRoot(ctx, teamID, ref, func(root *os.Root) error {
		payload, err := readEvidenceRecord(root)
		if err != nil {
			return err
		}
		return contracts.DecodeSealedRecord(payload, snapshot.TypeRef("validation/v1"), &record)
	})
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, 0, fmt.Errorf("%w: exact sealed validation is invalid: %w", ErrInvalidRequest, err)
	}
	revision, found := contracts.SchemaRevisionFor(snapshot.TypeRef("validation/v1"), record.Schema)
	if !found {
		return contracts.Record[contracts.ValidationBody]{}, 0, fmt.Errorf("%w: validation schema revision is unknown", ErrInvalidRequest)
	}
	return record, revision, nil
}

func (verifier *StoreEvidenceVerifier) withExactEvidenceRoot(
	ctx context.Context,
	teamID int,
	ref snapshot.SnapshotRef,
	inspect func(*os.Root) error,
) error {
	manifest, found, err := verifier.metadata.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil {
		return fmt.Errorf("authorize evidence snapshot: %w", err)
	}
	if !found || manifest.Validate() != nil ||
		manifest.ID != ref.ID || manifest.Type != ref.Type || manifest.Digest != ref.Digest ||
		manifest.ContentState != snapshot.ContentStateAvailable {
		return fmt.Errorf("authorized evidence snapshot does not match the exact available reference")
	}
	reader, err := verifier.content.Open(ctx, manifest)
	if err != nil || reader == nil {
		return fmt.Errorf("open evidence snapshot")
	}
	tree, captureErr := verifier.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return fmt.Errorf("capture evidence snapshot: %w", err)
	}
	if tree == nil {
		return fmt.Errorf("evidence canonicalizer returned no tree")
	}
	defer tree.Close()
	if tree.Digest != manifest.Digest ||
		tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		return fmt.Errorf("evidence content does not match its sealed manifest")
	}
	root, err := tree.OpenRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	validator, err := verifier.registry.Lookup(ref.Type)
	if err != nil {
		return err
	}
	if _, err := validator.RevalidateSealed(ctx, root, snapshot.ValidationContext{}); err != nil {
		return err
	}
	return inspect(root)
}

func readEvidenceRecord(root *os.Root) ([]byte, error) {
	const maximumRecordBytes int64 = 1 << 20
	info, err := root.Lstat("record.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumRecordBytes {
		return nil, fmt.Errorf("record.json must be a bounded regular file")
	}
	file, err := root.Open("record.json")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var payload bytes.Buffer
	copied, err := io.Copy(&payload, io.LimitReader(file, maximumRecordBytes+1))
	if err != nil || copied > maximumRecordBytes {
		return nil, fmt.Errorf("record.json exceeds the evidence read limit")
	}
	return payload.Bytes(), nil
}
