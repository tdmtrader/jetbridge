package publisher_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/stretchr/testify/require"
)

type reviewRunEvidenceResolverFunc func(context.Context, int, snapshot.WorkflowRunID) (publisher.ReviewRunEvidence, bool, error)

func (function reviewRunEvidenceResolverFunc) ResolveReviewRunEvidence(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
) (publisher.ReviewRunEvidence, bool, error) {
	if function == nil {
		return publisher.ReviewRunEvidence{}, false, nil
	}
	return function(ctx, teamID, runID)
}

type reviewOutcomeReaderFunc func(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (workflowoutcomes.Outcome, bool, error)

func (function reviewOutcomeReaderFunc) Get(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
) (workflowoutcomes.Outcome, bool, error) {
	if function == nil {
		return workflowoutcomes.Outcome{}, false, nil
	}
	return function(ctx, teamID, runID, outputID)
}

func TestEvidenceVerifierReopensExactAcceptedReviewAndAuthoritativeValidation(t *testing.T) {
	fixture := newReviewEvidenceFixture(t, reviewEvidenceFixtureOptions{})

	evidence, err := fixture.verifier.Verify(context.Background(), fixture.request)
	require.NoError(t, err)
	require.Equal(t, publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review:              fixture.review,
			Candidate:           fixture.candidate,
			Validation:          fixture.validation,
			ReviewWorkflowRunID: fixture.run.WorkflowRunID,
			OutcomeRevision:     fixture.outcome.Revision,
			AcceptedBy:          fixture.outcome.Actor,
			AcceptedAt:          fixture.outcome.AuditedAt.UTC(),
		},
	}, evidence)
}

func TestEvidenceVerifierRejectsClaimsNotBoundToExactAcceptedRun(t *testing.T) {
	tests := map[string]reviewEvidenceFixtureOptions{
		"review names another candidate":        {reviewCandidateFill: '9'},
		"caller expects another candidate":      {requestedCandidateFill: '6'},
		"review verdict is not accept":          {reviewConclusion: "inconclusive"},
		"validation names another candidate":    {validationCandidateFill: '8'},
		"validation verdict is not passed":      {validationConclusion: "failed"},
		"validation is not revision three":      {validationRevision: 2},
		"outcome verdict is not accepted":       {outcomeDisposition: workflowoutcomes.DispositionRejected},
		"outcome revision is stale":             {requestedOutcomeRevision: 2},
		"resolver returns another run":          {resolvedRunID: 78},
		"resolver returns another team":         {resolvedTeamID: 10},
		"resolver returns another primary port": {candidateInput: "before"},
		"arbitrary matching primary port": {
			candidateInput: "candidate", reviewCandidateInput: "candidate",
		},
		"resolver returns another output":          {reviewOutputFill: '7'},
		"resolver returns a non-v3 workflow":       {schemaVersion: 2},
		"resolver returns another workflow":        {workflowName: "small-fix"},
		"authorized content differs from its seal": {corruptReviewContent: true},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newReviewEvidenceFixture(t, options)
			_, err := fixture.verifier.Verify(context.Background(), fixture.request)
			require.ErrorIs(t, err, publisher.ErrInvalidRequest)
		})
	}
}

func TestEvidenceVerifierUsesExactOutputOutcomeRatherThanAProjection(t *testing.T) {
	fixture := newReviewEvidenceFixture(t, reviewEvidenceFixtureOptions{})
	otherReview := reviewEvidenceRef(91, "review/v1", '9')
	fixture.run.Review = otherReview
	fixture.setRun(fixture.run)

	_, err := fixture.verifier.Verify(context.Background(), fixture.request)
	require.ErrorIs(t, err, publisher.ErrInvalidRequest)
}

type reviewEvidenceFixtureOptions struct {
	reviewCandidateFill      byte
	requestedCandidateFill   byte
	validationCandidateFill  byte
	reviewOutputFill         byte
	reviewConclusion         string
	validationConclusion     string
	validationRevision       int
	outcomeDisposition       workflowoutcomes.Disposition
	requestedOutcomeRevision int64
	resolvedRunID            snapshot.WorkflowRunID
	resolvedTeamID           int
	candidateInput           string
	reviewCandidateInput     string
	schemaVersion            int
	workflowName             string
	corruptReviewContent     bool
}

type reviewEvidenceFixture struct {
	verifier   *publisher.StoreEvidenceVerifier
	request    publisher.EvidenceRequest
	candidate  snapshot.SnapshotRef
	review     snapshot.SnapshotRef
	validation snapshot.SnapshotRef
	run        publisher.ReviewRunEvidence
	outcome    workflowoutcomes.Outcome
	metadata   *snapshotfakes.FakeMetadataStore
	content    *snapshotfakes.FakeContentStore
	runValue   *publisher.ReviewRunEvidence
}

func (fixture *reviewEvidenceFixture) setRun(run publisher.ReviewRunEvidence) {
	*fixture.runValue = run
}

func newReviewEvidenceFixture(t *testing.T, options reviewEvidenceFixtureOptions) *reviewEvidenceFixture {
	t.Helper()
	if options.reviewCandidateFill == 0 {
		options.reviewCandidateFill = 'b'
	}
	if options.requestedCandidateFill == 0 {
		options.requestedCandidateFill = 'b'
	}
	if options.validationCandidateFill == 0 {
		options.validationCandidateFill = 'b'
	}
	if options.reviewOutputFill == 0 {
		options.reviewOutputFill = '0'
	}
	if options.reviewConclusion == "" {
		options.reviewConclusion = "accept"
	}
	if options.validationConclusion == "" {
		options.validationConclusion = "passed"
	}
	if options.validationRevision == 0 {
		options.validationRevision = 3
	}
	if options.outcomeDisposition == "" {
		options.outcomeDisposition = workflowoutcomes.DispositionAccepted
	}
	if options.requestedOutcomeRevision == 0 {
		options.requestedOutcomeRevision = 3
	}
	if options.resolvedRunID == 0 {
		options.resolvedRunID = 77
	}
	if options.resolvedTeamID == 0 {
		options.resolvedTeamID = 9
	}
	if options.candidateInput == "" {
		options.candidateInput = "after"
	}
	if options.reviewCandidateInput == "" {
		options.reviewCandidateInput = "after"
	}
	if options.schemaVersion == 0 {
		options.schemaVersion = 3
	}
	if options.workflowName == "" {
		options.workflowName = "code-review"
	}

	candidate := reviewEvidenceRef(11, "repository/v1", 'b')
	requestedCandidate := reviewEvidenceRef(
		candidate.ID,
		candidate.Type,
		options.requestedCandidateFill,
	)
	base := reviewEvidenceRef(12, "repository/v1", 'a')
	reviewSubject := reviewEvidenceRef(candidate.ID, candidate.Type, options.reviewCandidateFill)
	validationSubject := reviewEvidenceRef(candidate.ID, candidate.Type, options.validationCandidateFill)

	reviewBody := contracts.ReviewBody{
		Conclusion: options.reviewConclusion,
		Summary:    "reviewed exact candidate",
		Findings:   []contracts.Finding{},
	}
	reviewRecord, err := contracts.NewRecord(
		snapshot.TypeRef("review/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput("after", contracts.SubjectRolePrimary, options.reviewCandidateInput, reviewSubject),
			contracts.SubjectFromInput("before", contracts.SubjectRoleContext, "before", base),
		},
		reviewBody,
	)
	require.NoError(t, err)
	reviewManifest, reviewArchive := evidenceRecordSnapshot(t, 21, "review/v1", reviewRecord, nil)

	validationRecord, validationFiles := validationEvidenceRecord(
		t,
		validationSubject,
		options.validationConclusion,
		options.validationRevision,
	)
	validationManifest, validationArchive := evidenceRecordSnapshot(
		t, 22, "validation/v1", validationRecord, validationFiles,
	)
	reviewRef := snapshot.SnapshotRef{ID: reviewManifest.ID, Type: reviewManifest.Type, Digest: reviewManifest.Digest}
	validationRef := snapshot.SnapshotRef{ID: validationManifest.ID, Type: validationManifest.Type, Digest: validationManifest.Digest}
	if options.reviewOutputFill != '0' {
		reviewRef = reviewEvidenceRef(reviewRef.ID, reviewRef.Type, options.reviewOutputFill)
	}

	manifests := map[snapshot.SnapshotID]snapshot.Snapshot{
		reviewManifest.ID:     reviewManifest,
		validationManifest.ID: validationManifest,
	}
	archives := map[snapshot.SnapshotID][]byte{
		reviewManifest.ID:     reviewArchive,
		validationManifest.ID: validationArchive,
	}
	if options.corruptReviewContent {
		archives[reviewManifest.ID] = validationArchive
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedCalls(func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if teamID != 9 {
			return snapshot.Snapshot{}, false, nil
		}
		manifest, found := manifests[id]
		return manifest, found, nil
	})
	content := &snapshotfakes.FakeContentStore{}
	content.OpenCalls(func(_ context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archives[manifest.ID])), nil
	})

	run := publisher.ReviewRunEvidence{
		TeamID:                options.resolvedTeamID,
		WorkflowRunID:         options.resolvedRunID,
		WorkflowDefinitionID:  31,
		WorkflowName:          options.workflowName,
		WorkflowVersion:       4,
		SchemaVersion:         options.schemaVersion,
		DefinitionContentHash: strings.Repeat("d", 64),
		CandidateInput:        options.candidateInput,
		Candidate:             candidate,
		ReviewOutput:          "review",
		Review:                reviewRef,
	}
	runValue := run
	resolver := reviewRunEvidenceResolverFunc(func(
		_ context.Context,
		teamID int,
		runID snapshot.WorkflowRunID,
	) (publisher.ReviewRunEvidence, bool, error) {
		if teamID != 9 || runID != 77 {
			return publisher.ReviewRunEvidence{}, false, nil
		}
		return runValue, true, nil
	})
	outcome := workflowoutcomes.Outcome{
		WorkflowRunID:     77,
		OutputSnapshotID:  reviewManifest.ID,
		Disposition:       options.outcomeDisposition,
		PublicationState:  workflowoutcomes.PublicationNotRequested,
		HumanModified:     false,
		InterventionCount: 0,
		Labels:            []string{},
		Actor:             "alice",
		Revision:          3,
		AuditedAt:         time.Date(2026, 7, 29, 8, 0, 0, 0, time.FixedZone("offset", -7*60*60)),
	}
	fixture := &reviewEvidenceFixture{
		request: publisher.EvidenceRequest{
			TeamID: 9,
			AcceptedReview: &publisher.AcceptedReviewEvidenceRequest{
				Review:              snapshot.SnapshotRef{ID: reviewManifest.ID, Type: reviewManifest.Type, Digest: reviewManifest.Digest},
				Candidate:           requestedCandidate,
				Validation:          validationRef,
				ReviewWorkflowRunID: 77,
				OutcomeRevision:     options.requestedOutcomeRevision,
			},
		},
		candidate:  candidate,
		review:     snapshot.SnapshotRef{ID: reviewManifest.ID, Type: reviewManifest.Type, Digest: reviewManifest.Digest},
		validation: validationRef,
		run:        run,
		outcome:    outcome,
		metadata:   metadata,
		content:    content,
		runValue:   &runValue,
	}
	outcomes := reviewOutcomeReaderFunc(func(
		_ context.Context,
		teamID int,
		runID snapshot.WorkflowRunID,
		outputID snapshot.SnapshotID,
	) (workflowoutcomes.Outcome, bool, error) {
		if teamID != 9 || runID != 77 || outputID != reviewManifest.ID {
			return workflowoutcomes.Outcome{}, false, nil
		}
		return outcome, true, nil
	})
	verifier, err := publisher.NewEvidenceVerifier(
		resolver,
		outcomes,
		metadata,
		content,
		exactApprovalVerifierFunc(func(
			context.Context,
			publisher.DurableApprovalRequest,
		) (publisher.ApprovalEvidence, error) {
			return publisher.ApprovalEvidence{}, publisher.ErrInvalidRequest
		}),
		snapshot.Canonicalizer{TempDir: t.TempDir()},
	)
	require.NoError(t, err)
	fixture.verifier = verifier
	return fixture
}

func validationEvidenceRecord(
	t *testing.T,
	candidate snapshot.SnapshotRef,
	conclusion string,
	revision int,
) (contracts.Record[contracts.ValidationBody], map[string][]byte) {
	t.Helper()
	body := contracts.ValidationBody{
		Conclusion: conclusion,
		Summary:    "authoritative validation",
		Attestation: contracts.ValidationAttestation{
			CandidateDigest:       candidate.Digest,
			BaseInputs:            []contracts.ValidationBaseInput{},
			ProfileDigest:         reviewEvidenceRef(1, "opaque/v1", '1').Digest,
			ProtectedConfigDigest: reviewEvidenceRef(1, "opaque/v1", '2').Digest,
			CapabilityImage:       "example.invalid/validator@sha256:" + strings.Repeat("3", 64),
			CapabilityImageDigest: reviewEvidenceRef(1, "opaque/v1", '3').Digest,
			WorkflowDefinitionID:  51,
			WorkflowVersion:       6,
			Toolchain:             "dev-capability/v1",
		},
	}
	files := map[string][]byte{}
	switch conclusion {
	case "passed":
		body.Checks = []contracts.ValidationCheck{{
			ID: "tests", Kind: "test", Name: "tests", Status: "passed",
			Attempts: []contracts.ValidationAttempt{{
				Number: 1, Status: "passed", Duration: "1s",
				Log: contracts.ValidationLog{
					Path:      "content/logs/tests.log",
					Digest:    snapshot.Digest("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
					Size:      0,
					MediaType: "text/plain",
				},
				Evidence: []contracts.Anchor{},
			}},
		}}
		files["content/logs/tests.log"] = []byte{}
	case "failed":
		body.Checks = []contracts.ValidationCheck{{
			ID: "tests", Kind: "test", Name: "tests", Status: "failed",
			Attempts: []contracts.ValidationAttempt{{
				Number: 1, Status: "failed", Duration: "1s",
				Log: contracts.ValidationLog{
					Path:      "content/logs/tests.log",
					Digest:    snapshot.Digest("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
					Size:      0,
					MediaType: "text/plain",
				},
				Evidence: []contracts.Anchor{},
			}},
		}}
		files["content/logs/tests.log"] = []byte{}
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("validation/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"candidate", contracts.SubjectRolePrimary, "candidate", candidate,
		)},
		body,
	)
	require.NoError(t, err)
	if revision == 2 {
		schema, found := contracts.SchemaDigestForRevision("validation/v1", 2)
		require.True(t, found)
		record.Schema = schema
		record.Body = contracts.ValidationBody{
			Conclusion: "incomplete",
			Summary:    "legacy validation",
			Checks:     []contracts.ValidationCheck{},
		}
		files = map[string][]byte{}
	}
	return record, files
}

func evidenceRecordSnapshot[T any](
	t *testing.T,
	id snapshot.SnapshotID,
	typeRef snapshot.TypeRef,
	record contracts.Record[T],
	files map[string][]byte,
) (snapshot.Snapshot, []byte) {
	t.Helper()
	recordBytes, err := json.Marshal(record)
	require.NoError(t, err)
	all := map[string][]byte{"record.json": recordBytes}
	for name, content := range files {
		all[name] = content
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, name := range names {
		content := all[name]
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}))
		_, err := writer.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	tree, err := (snapshot.Canonicalizer{TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(raw.Bytes()))
	require.NoError(t, err)
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	require.NoError(t, err)
	return snapshot.Snapshot{
		ID:             id,
		Type:           typeRef,
		Digest:         tree.Digest,
		ByteSize:       tree.ByteSize,
		FileCount:      tree.FileCount,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Now().UTC(),
	}, archive
}

var _ publisher.ReviewRunEvidenceResolver = reviewRunEvidenceResolverFunc(nil)
var _ publisher.ReviewOutcomeReader = reviewOutcomeReaderFunc(nil)
