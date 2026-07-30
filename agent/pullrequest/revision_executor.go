package pullrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var ErrPRRevisionAuthority = errors.New(
	"pullrequest: exact PR revision authority is unavailable",
)

// PRRevisionSnapshotRefs is the exact non-repository-change evidence a
// revision executor must reopen immediately before provider mutation.
type PRRevisionSnapshotRefs struct {
	Validation snapshot.SnapshotRef
	Impact     snapshot.SnapshotRef
	Response   snapshot.SnapshotRef
}

// PRRevisionSnapshots is a narrow, semantically verified projection of the
// exact sealed records. Implementations must team-authorize and re-hash every
// reference in PRRevisionSnapshotRefs, run the registered read-time validator,
// and return only facts from those exact bytes.
type PRRevisionSnapshots struct {
	ValidationCandidate  contracts.StableSnapshotRef
	ValidationConclusion string
	Impact               contracts.PublishImpactBody
	ResponseObservation  contracts.StableSnapshotRef
	Response             contracts.PullRequestResponseBody
}

type PRRevisionSnapshotInspector interface {
	InspectPRRevisionSnapshots(
		context.Context,
		int,
		PRRevisionSnapshotRefs,
	) (PRRevisionSnapshots, error)
}

// PRRevisionCandidateInspector is satisfied by
// publisher.SnapshotChangeInspector. The result commit is read from the exact
// team-authorized repository-change/v1 rather than accepted from workflow
// parameters.
type PRRevisionCandidateInspector interface {
	InspectExactPRCandidate(
		context.Context,
		int,
		snapshot.SnapshotRef,
	) (publisher.RepositoryChange, error)
}

type snapshotPRRevisionInspector struct {
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
	registry      snapshot.ValidatorRegistry
}

// NewSnapshotPRRevisionInspector builds the store-backed read boundary used by
// the executor. It authorizes each reference for the exact team, canonicalizes
// the bytes again, and runs the registered sealed-record validator before
// projecting any publication fact.
func NewSnapshotPRRevisionInspector(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
) (PRRevisionSnapshotInspector, error) {
	if nilPRRevisionDependency(metadata) ||
		nilPRRevisionDependency(content) {
		return nil, fmt.Errorf(
			"pullrequest: PR revision snapshot stores are required",
		)
	}
	registry, err := contracts.NewRegistry(
		contracts.WithCanonicalizer(canonicalizer),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"pullrequest: PR revision validator registry: %w", err,
		)
	}
	return &snapshotPRRevisionInspector{
		metadata: metadata, content: content,
		canonicalizer: canonicalizer, registry: registry,
	}, nil
}

func (inspector *snapshotPRRevisionInspector) InspectPRRevisionSnapshots(
	ctx context.Context,
	teamID int,
	references PRRevisionSnapshotRefs,
) (PRRevisionSnapshots, error) {
	if inspector == nil || ctx == nil || teamID <= 0 {
		return PRRevisionSnapshots{}, fmt.Errorf(
			"pullrequest: PR revision snapshot inspection requires context and team",
		)
	}
	if err := ctx.Err(); err != nil {
		return PRRevisionSnapshots{}, err
	}
	if references.Validation.Validate() != nil ||
		references.Validation.Type !=
			snapshot.TypeRef("validation/v1") ||
		references.Impact.Validate() != nil ||
		references.Impact.Type !=
			snapshot.TypeRef("publish-impact/v1") ||
		references.Response.Validate() != nil ||
		references.Response.Type !=
			snapshot.TypeRef("pull-request-response/v1") {
		return PRRevisionSnapshots{}, fmt.Errorf(
			"pullrequest: PR revision snapshot references are invalid",
		)
	}

	var result PRRevisionSnapshots
	err := inspector.withExactPRRevisionRoot(
		ctx, teamID, references.Validation,
		func(root *os.Root) error {
			payload, err := readPRRevisionRecord(root)
			if err != nil {
				return err
			}
			var record contracts.Record[contracts.ValidationBody]
			if err := contracts.DecodeSealedRecord(
				payload,
				snapshot.TypeRef("validation/v1"),
				&record,
			); err != nil {
				return err
			}
			revision, found := contracts.SchemaRevisionFor(
				snapshot.TypeRef("validation/v1"), record.Schema,
			)
			if !found || revision != 3 {
				return fmt.Errorf(
					"validation/v1 revision 3 is required",
				)
			}
			primary, found := exactPRRevisionPrimary(record.Subjects)
			if !found {
				return fmt.Errorf(
					"validation/v1 requires one exact primary subject",
				)
			}
			result.ValidationCandidate = primary.StableRef()
			result.ValidationConclusion = record.Body.Conclusion
			return nil
		},
	)
	if err != nil {
		return PRRevisionSnapshots{}, fmt.Errorf(
			"pullrequest: inspect exact validation: %w", err,
		)
	}
	err = inspector.withExactPRRevisionRoot(
		ctx, teamID, references.Impact,
		func(root *os.Root) error {
			record, err := contracts.ReadSealedPublishImpactRecord(
				ctx, root,
			)
			if err != nil {
				return err
			}
			result.Impact = clonePRRevisionImpact(record.Body)
			return nil
		},
	)
	if err != nil {
		return PRRevisionSnapshots{}, fmt.Errorf(
			"pullrequest: inspect exact impact: %w", err,
		)
	}
	err = inspector.withExactPRRevisionRoot(
		ctx, teamID, references.Response,
		func(root *os.Root) error {
			record, err :=
				contracts.ReadSealedPullRequestResponseRecord(
					ctx, root,
				)
			if err != nil {
				return err
			}
			primary, found := exactPRRevisionPrimary(record.Subjects)
			if !found {
				return fmt.Errorf(
					"pull-request-response/v1 requires one exact primary subject",
				)
			}
			result.ResponseObservation = primary.StableRef()
			result.Response = clonePRRevisionResponse(record.Body)
			return nil
		},
	)
	if err != nil {
		return PRRevisionSnapshots{}, fmt.Errorf(
			"pullrequest: inspect exact response: %w", err,
		)
	}
	return result, nil
}

func (inspector *snapshotPRRevisionInspector) withExactPRRevisionRoot(
	ctx context.Context,
	teamID int,
	reference snapshot.SnapshotRef,
	inspect func(*os.Root) error,
) error {
	manifest, found, err := inspector.metadata.GetAuthorized(
		ctx, teamID, reference.ID,
	)
	if err != nil {
		return fmt.Errorf("authorize snapshot: %w", err)
	}
	if !found || manifest.Validate() != nil ||
		manifest.ID != reference.ID ||
		manifest.Type != reference.Type ||
		manifest.Digest != reference.Digest {
		return fmt.Errorf(
			"authorized snapshot does not match the exact reference",
		)
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return fmt.Errorf(
			"%w: snapshot is %s",
			snapshot.ErrContentUnavailable, manifest.ContentState,
		)
	}
	reader, err := inspector.content.Open(ctx, manifest)
	if err != nil {
		var closeErr error
		if reader != nil {
			closeErr = reader.Close()
		}
		return errors.Join(err, closeErr)
	}
	if reader == nil {
		return snapshot.ErrContentUnavailable
	}
	tree, captureErr := inspector.canonicalizer.Capture(ctx, reader)
	readerCloseErr := reader.Close()
	if err := errors.Join(captureErr, readerCloseErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return err
	}
	if tree == nil {
		return fmt.Errorf("snapshot canonicalizer returned no value")
	}
	if tree.Digest != manifest.Digest ||
		tree.ByteSize != manifest.ByteSize ||
		tree.FileCount != manifest.FileCount {
		_ = tree.Close()
		return fmt.Errorf(
			"snapshot bytes do not match the sealed manifest",
		)
	}
	root, err := tree.OpenRoot()
	if err != nil {
		_ = tree.Close()
		return err
	}
	validator, err := inspector.registry.Lookup(reference.Type)
	if err == nil {
		_, err = validator.RevalidateSealed(
			ctx, root, snapshot.ValidationContext{},
		)
	}
	if err == nil {
		err = inspect(root)
	}
	rootCloseErr := root.Close()
	treeCloseErr := tree.Close()
	return errors.Join(err, rootCloseErr, treeCloseErr)
}

func readPRRevisionRecord(root *os.Root) ([]byte, error) {
	const maximumRecordBytes int64 = 1 << 20
	info, err := root.Lstat("record.json")
	if err != nil || !info.Mode().IsRegular() ||
		info.Size() > maximumRecordBytes {
		return nil, fmt.Errorf(
			"record.json must be a bounded regular file",
		)
	}
	file, err := root.Open("record.json")
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	copied, readErr := io.Copy(
		&payload, io.LimitReader(file, maximumRecordBytes+1),
	)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if copied > maximumRecordBytes {
		return nil, fmt.Errorf("record.json exceeds the read limit")
	}
	return payload.Bytes(), nil
}

func exactPRRevisionPrimary(
	subjects []contracts.Subject,
) (contracts.Subject, bool) {
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

func clonePRRevisionImpact(
	body contracts.PublishImpactBody,
) contracts.PublishImpactBody {
	body.ChangedFiles = append(
		[]contracts.PublishChangedFile(nil), body.ChangedFiles...,
	)
	body.ValidationChanges = append(
		[]string(nil), body.ValidationChanges...,
	)
	body.RuleResults = append(
		[]contracts.PublishImpactRule(nil), body.RuleResults...,
	)
	body.Reasons = append([]string(nil), body.Reasons...)
	if body.AgentAssessment != nil {
		assessment := *body.AgentAssessment
		body.AgentAssessment = &assessment
	}
	return body
}

func clonePRRevisionResponse(
	body contracts.PullRequestResponseBody,
) contracts.PullRequestResponseBody {
	body.Replies = append(
		[]contracts.PullRequestThreadResponse(nil), body.Replies...,
	)
	return body
}

type prRevisionExecutor struct {
	bindings          BindingStore
	acceptedReviews   AcceptedReviewAuthorityResolver
	approvedBaselines ApprovedBaselineAuthorityResolver
	observations      MonitorObservationInspector
	snapshots         PRRevisionSnapshotInspector
	candidates        PRRevisionCandidateInspector
	evidence          publisher.EvidenceVerifier
	impact            publisher.PRImpactVerifier
	publications      publisher.PRService
	externalURL       string
}

func NewPRRevisionExecutor(
	bindings BindingStore,
	acceptedReviews AcceptedReviewAuthorityResolver,
	approvedBaselines ApprovedBaselineAuthorityResolver,
	observations MonitorObservationInspector,
	snapshots PRRevisionSnapshotInspector,
	candidates PRRevisionCandidateInspector,
	evidence publisher.EvidenceVerifier,
	impact publisher.PRImpactVerifier,
	publications publisher.PRService,
	externalURL string,
) (publisher.PRRevisionExecutor, error) {
	if nilPRRevisionDependency(bindings) ||
		nilPRRevisionDependency(acceptedReviews) ||
		nilPRRevisionDependency(approvedBaselines) ||
		nilPRRevisionDependency(observations) ||
		nilPRRevisionDependency(snapshots) ||
		nilPRRevisionDependency(candidates) ||
		nilPRRevisionDependency(evidence) ||
		nilPRRevisionDependency(impact) ||
		nilPRRevisionDependency(publications) {
		return nil, fmt.Errorf(
			"pullrequest: PR revision executor dependencies are required",
		)
	}
	normalizedExternalURL, err := normalizePRRevisionExternalURL(
		externalURL,
	)
	if err != nil {
		return nil, err
	}
	return &prRevisionExecutor{
		bindings: bindings, acceptedReviews: acceptedReviews,
		approvedBaselines: approvedBaselines,
		observations:      observations,
		snapshots:         snapshots, candidates: candidates,
		evidence: evidence, impact: impact,
		publications: publications,
		externalURL:  normalizedExternalURL,
	}, nil
}

func (executor *prRevisionExecutor) ExecutePRRevision(
	ctx context.Context,
	request publisher.PRRevisionPublicationRequest,
) error {
	if executor == nil || ctx == nil {
		return fmt.Errorf(
			"%w: executor and context are required",
			ErrPRRevisionAuthority,
		)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrPRRevisionAuthority, err)
	}

	binding, found, err := executor.bindings.Get(
		ctx, request.Authority.TeamID, request.BindingID,
	)
	if err != nil {
		return fmt.Errorf("pullrequest: reopen PR binding: %w", err)
	}
	if !found {
		return ErrBindingNotFound
	}
	target, active, err := exactPRRevisionBinding(binding, request)
	if err != nil {
		return err
	}

	observation, err := executor.observations.InspectMonitorObservation(
		ctx, request.Authority.TeamID, request.Observation,
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reopen exact revision observation: %w", err,
		)
	}
	trigger, err := exactPRRevisionObservation(
		binding, active, observation,
	)
	if err != nil {
		return err
	}

	change, err := executor.candidates.InspectExactPRCandidate(
		ctx, request.Authority.TeamID, request.Candidate,
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reopen exact revision candidate: %w", err,
		)
	}
	defer change.Close()
	if err := exactPRRevisionCandidate(change, active); err != nil {
		return err
	}

	records, err := executor.snapshots.InspectPRRevisionSnapshots(
		ctx,
		request.Authority.TeamID,
		PRRevisionSnapshotRefs{
			Validation: request.Validation,
			Impact:     request.Impact,
			Response:   request.Response,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reopen exact revision evidence: %w", err,
		)
	}
	if err := exactPRRevisionSnapshots(
		request, observation, trigger, records,
	); err != nil {
		return err
	}
	if err := executor.reverifyPRRevisionAuthority(
		ctx, binding, target, request, records,
	); err != nil {
		return err
	}

	locator := publisher.PRLocator{
		Provider:   publisher.PRProvider(binding.Locator.Provider),
		Repository: binding.Locator.Repository,
		ExternalID: binding.Locator.ExternalID,
	}
	branch := publisher.BranchPublicationRequest{
		Authority:             request.Authority,
		Observation:           request.Observation,
		Candidate:             request.Candidate,
		Validation:            request.Validation,
		Impact:                request.Impact,
		Evidence:              request.Evidence.Clone(),
		Destination:           target.Destination,
		ApprovalPolicyVersion: target.ApprovalPolicyVersion,
		Locator:               locator,
		SourceRef:             target.SourceRef,
		TargetRef:             target.TargetRef,
		ExpectedSource: contracts.PullRequestHeadExpectation{
			Exists: true, SHA: active.SourceSHA,
		},
		ExpectedTargetSHA: active.TargetSHA,
		NewSourceSHA:      change.ResultSHA,
	}
	if err := branch.Validate(); err != nil {
		return fmt.Errorf(
			"%w: exact branch operation is invalid: %v",
			ErrPRRevisionAuthority, err,
		)
	}
	branchPublication, err := executor.publications.PublishBranch(
		ctx, branch,
	)
	if err != nil {
		return err
	}
	if err := exactPRRevisionBranchPublication(
		branchPublication, branch,
	); err != nil {
		return err
	}
	if branchPublication.Status == publisher.StatusRebaseRequired {
		return nil
	}

	status := publisher.StatusPublicationRequest{
		Authority:             request.Authority,
		Observation:           request.Observation,
		Validation:            request.Validation,
		Evidence:              request.Evidence.Clone(),
		Destination:           target.Destination,
		ApprovalPolicyVersion: target.ApprovalPolicyVersion,
		Locator:               locator,
		TargetRef:             target.TargetRef,
		SourceSHA:             change.ResultSHA,
		State:                 "success",
		Description:           "Validated",
		TargetURL: executor.externalURL + "/builds/" +
			strconv.FormatInt(request.Authority.BuildID, 10),
	}
	if err := status.Validate(); err != nil {
		return fmt.Errorf(
			"%w: exact status operation is invalid: %v",
			ErrPRRevisionAuthority, err,
		)
	}
	statusPublication, err := executor.publications.PublishStatus(
		ctx, status,
	)
	if err != nil {
		return err
	}
	if err := exactPRRevisionStatusPublication(
		statusPublication, status,
	); err != nil {
		return err
	}

	if trigger != contracts.PullRequestReviewBatchTrigger {
		return nil
	}
	response := publisher.ResponsePublicationRequest{
		Authority:             request.Authority,
		Observation:           request.Observation,
		ResponseSnapshot:      request.Response,
		Evidence:              request.Evidence.Clone(),
		Destination:           target.Destination,
		ApprovalPolicyVersion: target.ApprovalPolicyVersion,
		Locator:               locator,
		TargetRef:             target.TargetRef,
		Batch:                 observation.ReviewBatches[0],
		Response:              records.Response,
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf(
			"%w: exact response operation is invalid: %v",
			ErrPRRevisionAuthority, err,
		)
	}
	responsePublication, err := executor.publications.PublishResponse(
		ctx, response,
	)
	if err != nil {
		return err
	}
	return exactPRRevisionResponsePublication(
		responsePublication, response,
	)
}

func exactPRRevisionBinding(
	binding Binding,
	request publisher.PRRevisionPublicationRequest,
) (MonitorPublicationTarget, LaunchReservation, error) {
	if binding.ID != request.BindingID ||
		binding.TeamID != request.Authority.TeamID ||
		binding.State != BindingActive ||
		binding.Paused || binding.OperatorTerminated ||
		binding.Active == nil {
		return MonitorPublicationTarget{}, LaunchReservation{},
			ErrPRRevisionAuthority
	}
	target, err := NewMonitorPublicationTarget(
		MonitorPublicationTargetSpec{
			Destination:           binding.Destination,
			ApprovalPolicyVersion: binding.ApprovalPolicyVersion,
			SourceRef:             binding.SourceRef,
			TargetRef:             binding.TargetRef,
		},
	)
	if err != nil {
		return MonitorPublicationTarget{}, LaunchReservation{},
			fmt.Errorf(
				"%w: binding publication target is invalid",
				ErrPRRevisionAuthority,
			)
	}
	protected, err := target.Protected()
	if err != nil {
		return MonitorPublicationTarget{}, LaunchReservation{}, err
	}
	active := *binding.Active
	if active.BindingID != binding.ID ||
		active.BaseRevision <= 0 ||
		active.BindingRevision != active.BaseRevision+1 ||
		binding.Revision != active.BindingRevision+1 ||
		active.WorkflowRunID == nil ||
		*active.WorkflowRunID != request.Authority.WorkflowRunID ||
		active.ActionDigest != request.ActionDigest ||
		active.ObservationSnapshotID != request.Observation.ID ||
		active.SourceSHA != request.SourceHead ||
		active.TargetSHA != request.TargetHead ||
		active.Cursor == "" || active.Cursor.Validate() != nil ||
		strings.TrimSpace(active.Token) != active.Token ||
		active.Token == "" || len(active.Token) > 128 ||
		request.Destination != protected.Destination ||
		request.ApprovalPolicyVersion !=
			protected.ApprovalPolicyVersion {
		return MonitorPublicationTarget{}, LaunchReservation{},
			ErrPRRevisionAuthority
	}
	return protected, active, nil
}

func exactPRRevisionObservation(
	binding Binding,
	active LaunchReservation,
	observation contracts.PullRequestBody,
) (contracts.PullRequestTrigger, error) {
	if err := observation.Validate(nil); err != nil {
		return "", fmt.Errorf(
			"%w: sealed observation is invalid",
			ErrPRRevisionAuthority,
		)
	}
	if observation.Provider != string(binding.Locator.Provider) ||
		observation.Repository != binding.Locator.Repository ||
		observation.ExternalID != binding.Locator.ExternalID ||
		observation.URL != binding.URL ||
		observation.SourceRef != binding.SourceRef ||
		observation.SourceSHA != active.SourceSHA ||
		observation.TargetRef != binding.TargetRef ||
		observation.TargetSHA != active.TargetSHA {
		return "", ErrPRRevisionAuthority
	}
	switch observation.Trigger {
	case contracts.PullRequestCompletedTrigger,
		contracts.PullRequestAbandonedTrigger:
		return "", ErrTerminalMonitorAction
	case contracts.PullRequestReviewBatchTrigger:
		if observation.State != contracts.PullRequestActive ||
			len(observation.ReviewBatches) != 1 {
			return "", ErrPRRevisionAuthority
		}
	case contracts.PullRequestConflictTrigger:
		if observation.State != contracts.PullRequestActive ||
			observation.Mergeability !=
				contracts.PullRequestConflicted ||
			len(observation.ReviewBatches) != 0 {
			return "", ErrPRRevisionAuthority
		}
	case contracts.PullRequestFreshnessTrigger:
		if observation.State != contracts.PullRequestActive ||
			len(observation.ReviewBatches) != 0 {
			return "", ErrPRRevisionAuthority
		}
	default:
		return "", ErrPRRevisionAuthority
	}
	return observation.Trigger, nil
}

func exactPRRevisionCandidate(
	change publisher.RepositoryChange,
	active LaunchReservation,
) error {
	if err := change.Validate(); err != nil ||
		change.BaseSHA != active.TargetSHA ||
		!validPRRevisionObjectID(change.ResultSHA) ||
		len(change.ResultSHA) != len(active.SourceSHA) ||
		len(change.ResultSHA) != len(active.TargetSHA) {
		return fmt.Errorf(
			"%w: candidate result does not bind the exact target",
			ErrPRRevisionAuthority,
		)
	}
	return nil
}

func exactPRRevisionSnapshots(
	request publisher.PRRevisionPublicationRequest,
	observation contracts.PullRequestBody,
	trigger contracts.PullRequestTrigger,
	records PRRevisionSnapshots,
) error {
	if records.ValidationCandidate.Validate() != nil ||
		records.ValidationCandidate.Type != request.Candidate.Type ||
		records.ValidationCandidate.Digest != request.Candidate.Digest ||
		records.ValidationConclusion != "passed" ||
		records.Impact.Validate(nil) != nil ||
		records.Impact.CandidateDigest !=
			request.Candidate.Digest.String() ||
		records.ResponseObservation.Validate() != nil ||
		records.ResponseObservation.Type != request.Observation.Type ||
		records.ResponseObservation.Digest !=
			request.Observation.Digest ||
		records.Response.Validate(nil) != nil ||
		contracts.ValidatePullRequestResponseAgainst(
			records.Response, observation,
		) != nil {
		return fmt.Errorf(
			"%w: exact revision snapshots do not bind one another",
			ErrPRRevisionAuthority,
		)
	}
	switch trigger {
	case contracts.PullRequestReviewBatchTrigger:
		if records.Response.Kind !=
			contracts.PullRequestResponseReviewResponse {
			return ErrPRRevisionAuthority
		}
	case contracts.PullRequestConflictTrigger,
		contracts.PullRequestFreshnessTrigger:
		if records.Response.Kind !=
			contracts.PullRequestResponseNoResponse {
			return ErrPRRevisionAuthority
		}
	default:
		return ErrPRRevisionAuthority
	}
	return nil
}

func (executor *prRevisionExecutor) reverifyPRRevisionAuthority(
	ctx context.Context,
	binding Binding,
	target MonitorPublicationTarget,
	request publisher.PRRevisionPublicationRequest,
	records PRRevisionSnapshots,
) error {
	if binding.OriginatingPublicationOccurrence == nil ||
		*binding.OriginatingPublicationOccurrence <= 0 {
		return ErrPRRevisionAuthority
	}
	originatingOccurrenceID :=
		*binding.OriginatingPublicationOccurrence
	originalAuthority, found, err :=
		executor.acceptedReviews.ResolveAcceptedReviewAuthority(
			ctx,
			request.Authority.TeamID,
			originatingOccurrenceID,
		)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: resolve original accepted review: %w", err,
		)
	}
	if !found {
		return ErrPRRevisionAuthority
	}
	protectedOriginal, err := originalAuthority.Protected()
	if err != nil ||
		protectedOriginal.TeamID != request.Authority.TeamID ||
		protectedOriginal.PublicationOccurrenceID !=
			originatingOccurrenceID {
		return ErrPRRevisionAuthority
	}
	acceptedRequest := publisher.AcceptedReviewEvidenceRequest{
		Review:              protectedOriginal.Review,
		Candidate:           protectedOriginal.Candidate,
		Validation:          protectedOriginal.Validation,
		ReviewWorkflowRunID: protectedOriginal.ReviewWorkflowRunID,
		OutcomeRevision:     protectedOriginal.OutcomeRevision,
	}
	acceptedEvidence, err := executor.evidence.Verify(
		ctx,
		publisher.EvidenceRequest{
			TeamID:         request.Authority.TeamID,
			AcceptedReview: &acceptedRequest,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reverify accepted-review baseline: %w", err,
		)
	}
	if !acceptedRequest.Matches(acceptedEvidence) {
		return ErrPRRevisionAuthority
	}

	baselineLookup := ApprovedBaselineAuthorityLookup{
		TeamID:    request.Authority.TeamID,
		BindingID: binding.ID,
		PublicationOccurrenceID: binding.
			ApprovedBaselinePublicationOccurrenceID,
		RepositorySnapshotID: binding.
			ApprovedBaselineRepositorySnapshotID,
		ValidationSnapshotID: binding.
			ApprovedBaselineValidationSnapshotID,
	}
	if err := baselineLookup.Validate(); err != nil {
		return ErrPRRevisionAuthority
	}
	baselineAuthority, found, err :=
		executor.approvedBaselines.ResolveApprovedBaselineAuthority(
			ctx, baselineLookup,
		)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: resolve current approved baseline: %w", err,
		)
	}
	if !found {
		return ErrPRRevisionAuthority
	}
	protectedBaseline, err := baselineAuthority.Protected()
	if err != nil ||
		protectedBaseline.TeamID != baselineLookup.TeamID ||
		protectedBaseline.BindingID != baselineLookup.BindingID ||
		protectedBaseline.PublicationOccurrenceID !=
			baselineLookup.PublicationOccurrenceID ||
		protectedBaseline.Repository.ID !=
			baselineLookup.RepositorySnapshotID ||
		protectedBaseline.Validation.ID !=
			baselineLookup.ValidationSnapshotID {
		return ErrPRRevisionAuthority
	}
	if protectedBaseline.PublicationOccurrenceID ==
		originatingOccurrenceID {
		if protectedBaseline.Kind !=
			publisher.EvidenceAcceptedReview ||
			protectedBaseline.Repository !=
				protectedOriginal.Candidate ||
			protectedBaseline.Validation !=
				protectedOriginal.Validation {
			return ErrPRRevisionAuthority
		}
	} else if protectedBaseline.Kind !=
		publisher.EvidenceHumanWait {
		return ErrPRRevisionAuthority
	}

	switch request.Evidence.Kind {
	case publisher.EvidenceAcceptedReview:
		if request.AcceptedReview == nil ||
			*request.AcceptedReview != acceptedRequest ||
			!reflect.DeepEqual(request.Evidence, acceptedEvidence) {
			return ErrPRRevisionAuthority
		}
	case publisher.EvidenceHumanWait:
		if err := executor.reverifyPRRevisionHumanWait(
			ctx, request,
		); err != nil {
			return err
		}
	default:
		return ErrPRRevisionAuthority
	}
	impactRequest := publisher.PRImpactVerificationRequest{
		TeamID:             request.Authority.TeamID,
		BindingID:          request.BindingID,
		ActionDigest:       snapshot.Digest(request.ActionDigest),
		PolicyVersion:      target.ApprovalPolicyVersion,
		Observation:        request.Observation,
		Baseline:           protectedBaseline.Repository,
		BaselineValidation: protectedBaseline.Validation,
		Candidate:          request.Candidate,
		Validation:         request.Validation,
		Impact:             request.Impact,
		Response:           request.Response,
		AcceptedReview:     acceptedEvidence.Clone(),
		Body:               records.Impact,
	}
	if err := impactRequest.Validate(); err != nil {
		return fmt.Errorf(
			"%w: exact PR impact verification request is invalid",
			ErrPRRevisionAuthority,
		)
	}
	verifiedImpact, err := executor.impact.VerifyPRImpact(
		ctx, impactRequest,
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reverify exact PR revision impact: %w", err,
		)
	}
	if !reflect.DeepEqual(verifiedImpact, records.Impact) {
		return ErrPRRevisionAuthority
	}
	if verifiedImpact.ReapprovalRequired {
		if request.Evidence.Kind != publisher.EvidenceHumanWait {
			return ErrPRRevisionAuthority
		}
	} else if request.Evidence.Kind !=
		publisher.EvidenceAcceptedReview {
		return ErrPRRevisionAuthority
	}
	return nil
}

func (executor *prRevisionExecutor) reverifyPRRevisionHumanWait(
	ctx context.Context,
	request publisher.PRRevisionPublicationRequest,
) error {
	if request.ApprovalContext == nil ||
		request.Evidence.HumanWait == nil {
		return ErrPRRevisionAuthority
	}
	expectedContext, err := json.Marshal(request.ApprovalContext)
	if err != nil {
		return fmt.Errorf(
			"%w: encode PR approval context",
			ErrPRRevisionAuthority,
		)
	}
	verified, err := executor.evidence.Verify(
		ctx,
		publisher.EvidenceRequest{
			TeamID: request.Authority.TeamID,
			HumanWait: &publisher.DurableApprovalRequest{
				TeamID:          request.Authority.TeamID,
				WorkflowRunID:   request.Authority.WorkflowRunID,
				BuildID:         request.Authority.BuildID,
				Approval:        request.Evidence.HumanWait.Answer,
				ExpectedContext: expectedContext,
			},
		},
	)
	if err != nil {
		return fmt.Errorf(
			"pullrequest: reverify exact PR revision evidence: %w", err,
		)
	}
	if !reflect.DeepEqual(verified, request.Evidence) {
		return ErrPRRevisionAuthority
	}
	return nil
}

func exactPRRevisionBranchPublication(
	publication publisher.Publication,
	request publisher.BranchPublicationRequest,
) error {
	action := publisher.PRAction{
		Kind:   publisher.OperationPublishPRBranch,
		Branch: &request,
	}
	if err := exactPRRevisionPublication(publication, action); err != nil {
		return err
	}
	switch publication.Status {
	case publisher.StatusSucceeded:
		if publication.Result.ExternalID != request.SourceRef ||
			publication.Result.URL != "" ||
			publication.Result.HeadSHA != request.NewSourceSHA ||
			publication.Result.BaseSHA != request.ExpectedTargetSHA ||
			publication.Result.Detail != "" {
			return ErrPRRevisionAuthority
		}
	case publisher.StatusRebaseRequired:
		if publication.Result.ExternalID != "" ||
			publication.Result.URL != "" ||
			publication.Result.HeadSHA != request.NewSourceSHA ||
			publication.Result.BaseSHA != request.ExpectedTargetSHA ||
			strings.TrimSpace(publication.Result.Detail) == "" {
			return ErrPRRevisionAuthority
		}
	default:
		return ErrPRRevisionAuthority
	}
	return nil
}

func exactPRRevisionStatusPublication(
	publication publisher.Publication,
	request publisher.StatusPublicationRequest,
) error {
	action := publisher.PRAction{
		Kind:   publisher.OperationPublishPRStatus,
		Status: &request,
	}
	if err := exactPRRevisionPublication(publication, action); err != nil {
		return err
	}
	if publication.Status != publisher.StatusSucceeded ||
		strings.TrimSpace(publication.Result.ExternalID) == "" ||
		validateURL(publication.Result.URL) != nil ||
		publication.Result.HeadSHA != request.SourceSHA ||
		publication.Result.BaseSHA != "" ||
		publication.Result.Detail != "" {
		return ErrPRRevisionAuthority
	}
	return nil
}

func exactPRRevisionResponsePublication(
	publication publisher.Publication,
	request publisher.ResponsePublicationRequest,
) error {
	action := publisher.PRAction{
		Kind:     publisher.OperationRespondToReview,
		Response: &request,
	}
	if err := exactPRRevisionPublication(publication, action); err != nil {
		return err
	}
	if publication.Status != publisher.StatusSucceeded ||
		strings.TrimSpace(publication.Result.ExternalID) == "" ||
		validateURL(publication.Result.URL) != nil ||
		publication.Result.HeadSHA != request.Batch.CommitSHA ||
		publication.Result.BaseSHA != "" ||
		publication.Result.Detail != "" {
		return ErrPRRevisionAuthority
	}
	return nil
}

func exactPRRevisionPublication(
	publication publisher.Publication,
	action publisher.PRAction,
) error {
	if publication.ID <= 0 ||
		publication.Attempt <= 0 ||
		publication.CreatedAt.IsZero() ||
		publication.UpdatedAt.IsZero() ||
		publication.UpdatedAt.Before(publication.CreatedAt) ||
		!publication.LeaseUntil.IsZero() ||
		publication.OperationKind != action.Kind ||
		publication.PRAction == nil ||
		publication.Status != publication.Result.Status ||
		publication.Result.Validate() != nil ||
		action.ValidatePersisted() != nil ||
		!reflect.DeepEqual(*publication.PRAction, action) {
		return ErrPRRevisionAuthority
	}
	key, err := action.OperationKey()
	if err != nil || publication.OperationKey != key {
		return ErrPRRevisionAuthority
	}
	return nil
}

func normalizePRRevisionExternalURL(value string) (string, error) {
	if strings.TrimSpace(value) != value || value == "" ||
		len(value) > 1900 {
		return "", fmt.Errorf(
			"pullrequest: PR revision external URL is invalid",
		)
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme != "http" && parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.User != nil {
		return "", fmt.Errorf(
			"pullrequest: PR revision external URL is invalid",
		)
	}
	return strings.TrimRight(value, "/"), nil
}

func validPRRevisionObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func nilPRRevisionDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ publisher.PRRevisionExecutor = (*prRevisionExecutor)(nil)
var _ PRRevisionSnapshotInspector = (*snapshotPRRevisionInspector)(nil)
var _ PRRevisionCandidateInspector = (*publisher.SnapshotChangeInspector)(nil)
