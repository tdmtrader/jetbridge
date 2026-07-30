package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var (
	ErrInitialPRAuthority = errors.New(
		"pullrequest: exact initial PR authority is unavailable",
	)
	ErrInitialPRStale = errors.New(
		"pullrequest: initial PR publication requires a fresh observation",
	)
)

// InitialPRServerAuthority is the protected server-owned publication and
// monitor configuration for one binding-free initial publication. Public
// fields are diagnostic; mutation after construction invalidates the value.
type InitialPRServerAuthority struct {
	Authority                   publisher.Authority
	Provider                    Provider
	Repository                  string
	Destination                 string
	ApprovalPolicyVersion       string
	SourceRef                   string
	TargetRef                   string
	Title                       string
	Body                        string
	MonitorWorkflowDefinitionID int
	MonitorWorkflowVersion      int

	authority *initialPRServerAuthority
}

type InitialPRServerAuthoritySpec struct {
	Authority                   publisher.Authority
	Provider                    Provider
	Repository                  string
	Destination                 string
	ApprovalPolicyVersion       string
	SourceRef                   string
	TargetRef                   string
	Title                       string
	Body                        string
	MonitorWorkflowDefinitionID int
	MonitorWorkflowVersion      int
}

type initialPRServerAuthority struct {
	value InitialPRServerAuthority
}

func NewInitialPRServerAuthority(
	spec InitialPRServerAuthoritySpec,
) (InitialPRServerAuthority, error) {
	value := InitialPRServerAuthority{
		Authority: spec.Authority,
		Provider:  spec.Provider, Repository: spec.Repository,
		Destination:           spec.Destination,
		ApprovalPolicyVersion: spec.ApprovalPolicyVersion,
		SourceRef:             spec.SourceRef, TargetRef: spec.TargetRef,
		Title: spec.Title, Body: spec.Body,
		MonitorWorkflowDefinitionID: spec.MonitorWorkflowDefinitionID,
		MonitorWorkflowVersion:      spec.MonitorWorkflowVersion,
	}
	if err := validateInitialPRServerAuthority(value); err != nil {
		return InitialPRServerAuthority{}, err
	}
	protected := value
	value.authority = &initialPRServerAuthority{value: protected}
	return value, nil
}

func (value InitialPRServerAuthority) Protected() (
	InitialPRServerAuthority,
	error,
) {
	if value.authority == nil {
		return InitialPRServerAuthority{}, ErrInitialPRAuthority
	}
	public := value
	public.authority = nil
	if !reflect.DeepEqual(public, value.authority.value) {
		return InitialPRServerAuthority{}, fmt.Errorf(
			"%w: server authority changed after construction",
			ErrInitialPRAuthority,
		)
	}
	protected := value.authority.value
	protected.authority = value.authority
	return protected, nil
}

func validateInitialPRServerAuthority(
	value InitialPRServerAuthority,
) error {
	if value.Authority.TeamID <= 0 ||
		value.Authority.BuildID <= 0 ||
		value.Authority.WorkflowRunID.Validate() != nil ||
		validateBoundedText(
			"initial PR team name", value.Authority.TeamName, 256,
		) != nil ||
		validateBoundedText(
			"initial PR actor", value.Authority.Actor, 256,
		) != nil {
		return fmt.Errorf("%w: publication authority is invalid", ErrInitialPRAuthority)
	}
	if err := (Locator{
		Provider: value.Provider, Repository: value.Repository,
	}).validate(contracts.PullRequestMissing); err != nil {
		return fmt.Errorf("%w: provider locator is invalid", ErrInitialPRAuthority)
	}
	if _, err := NewMonitorPublicationTarget(
		MonitorPublicationTargetSpec{
			Destination:           value.Destination,
			ApprovalPolicyVersion: value.ApprovalPolicyVersion,
			SourceRef:             value.SourceRef, TargetRef: value.TargetRef,
		},
	); err != nil {
		return fmt.Errorf("%w: publication target is invalid", ErrInitialPRAuthority)
	}
	if err := validateBoundedText(
		"initial PR title", value.Title, 256,
	); err != nil ||
		value.Body == "" || !utf8.ValidString(value.Body) ||
		len(value.Body) > 32<<10 ||
		strings.IndexByte(value.Body, 0) >= 0 ||
		strings.Contains(value.Body, "Jetbridge-Operation:") {
		return fmt.Errorf("%w: pull request text is invalid", ErrInitialPRAuthority)
	}
	if value.MonitorWorkflowDefinitionID <= 0 ||
		value.MonitorWorkflowVersion <= 0 {
		return fmt.Errorf("%w: monitor workflow identity is invalid", ErrInitialPRAuthority)
	}
	return nil
}

// InitialPRPublishRequest names only immutable snapshot/head authority in
// addition to the two protected server values. Expected source state is
// reopened from MissingObservation rather than accepted separately.
type InitialPRPublishRequest struct {
	Authority          InitialPRServerAuthority
	AcceptedReview     AcceptedReviewAuthority
	MissingObservation snapshot.SnapshotRef
	Candidate          snapshot.SnapshotRef
	Validation         snapshot.SnapshotRef
	Impact             snapshot.SnapshotRef
	SourceSHA          string
	TargetSHA          string
}

type InitialPRFinalSnapshotRefs struct {
	Validation snapshot.SnapshotRef
	Impact     snapshot.SnapshotRef
}

// InitialPRFinalSnapshots is a semantically verified projection of the exact
// final validation and impact records.
type InitialPRFinalSnapshots struct {
	ValidationCandidate  contracts.StableSnapshotRef
	ValidationConclusion string
	Impact               contracts.PublishImpactBody
}

// InitialPRFinalSnapshotInspector must team-authorize, rehash, and revalidate
// both exact references before returning this projection.
type InitialPRFinalSnapshotInspector interface {
	InspectInitialPRFinalSnapshots(
		context.Context,
		int,
		InitialPRFinalSnapshotRefs,
	) (InitialPRFinalSnapshots, error)
}

// InitialPRImpactVerificationRequest is the complete binding-free impact
// authority. The existing publisher.PRImpactVerifier is deliberately not
// reused: its positive BindingID and response requirements describe monitor
// revisions, not an initial publication that has no binding yet.
type InitialPRImpactVerificationRequest struct {
	TeamID         int
	WorkflowRunID  snapshot.WorkflowRunID
	PolicyVersion  string
	Destination    string
	Observation    snapshot.SnapshotRef
	Baseline       snapshot.SnapshotRef
	Candidate      snapshot.SnapshotRef
	Validation     snapshot.SnapshotRef
	Impact         snapshot.SnapshotRef
	AcceptedReview publisher.PublicationEvidence
	SourceRef      string
	ExpectedSource contracts.PullRequestHeadExpectation
	SourceSHA      string
	TargetRef      string
	TargetSHA      string
	Body           contracts.PublishImpactBody
}

func (request InitialPRImpactVerificationRequest) Validate() error {
	if request.TeamID <= 0 ||
		request.WorkflowRunID.Validate() != nil ||
		strings.TrimSpace(request.PolicyVersion) != request.PolicyVersion ||
		request.PolicyVersion == "" ||
		len(request.PolicyVersion) > 128 ||
		strings.TrimSpace(request.Destination) != request.Destination ||
		request.Destination == "" ||
		len(request.Destination) > maxURLBytes ||
		request.Observation.Validate() != nil ||
		request.Observation.Type != snapshot.TypeRef("pull-request/v1") ||
		request.Baseline.Validate() != nil ||
		request.Baseline.Type != snapshot.TypeRef("repository/v1") ||
		request.Candidate.Validate() != nil ||
		request.Candidate.Type != snapshot.TypeRef("repository-change/v1") ||
		request.Validation.Validate() != nil ||
		request.Validation.Type != snapshot.TypeRef("validation/v1") ||
		request.Impact.Validate() != nil ||
		request.Impact.Type != snapshot.TypeRef("publish-impact/v1") ||
		request.AcceptedReview.Kind != publisher.EvidenceAcceptedReview ||
		request.AcceptedReview.AcceptedReview == nil ||
		request.AcceptedReview.HumanWait != nil ||
		request.AcceptedReview.Validate() != nil ||
		request.AcceptedReview.AcceptedReview.Candidate != request.Baseline ||
		validateRef("initial PR impact source ref", request.SourceRef) != nil ||
		validateRef("initial PR impact target ref", request.TargetRef) != nil ||
		request.SourceRef == request.TargetRef ||
		request.ExpectedSource.Validate() != nil ||
		validateObjectID("initial PR impact source sha", request.SourceSHA) != nil ||
		validateObjectID("initial PR impact target sha", request.TargetSHA) != nil ||
		len(request.SourceSHA) != len(request.TargetSHA) ||
		request.Body.Validate(nil) != nil ||
		request.Body.BaselineDigest != request.Baseline.Digest.String() ||
		request.Body.CandidateDigest != request.Candidate.Digest.String() {
		return fmt.Errorf("%w: initial PR impact verification request is invalid", ErrInitialPRAuthority)
	}
	return nil
}

// InitialPRImpactVerifier independently recomputes the final impact under the
// deployment-owned policy and compares the complete sealed decision.
type InitialPRImpactVerifier interface {
	VerifyInitialPRImpact(
		context.Context,
		InitialPRImpactVerificationRequest,
	) (contracts.PublishImpactBody, error)
}

// InitialPRObservationSealRequest is constructed only from a provider
// reobservation. Freshness is used as the valid active-record trigger for this
// already-acknowledged baseline; it does not schedule a monitor action.
type InitialPRObservationSealRequest struct {
	TeamID        int
	WorkflowRunID snapshot.WorkflowRunID
	Cursor        Cursor
	Body          contracts.PullRequestBody
}

func (request InitialPRObservationSealRequest) Validate() error {
	if request.TeamID <= 0 ||
		request.WorkflowRunID.Validate() != nil ||
		request.Cursor == "" ||
		request.Cursor.Validate() != nil ||
		request.Body.Validate(nil) != nil ||
		request.Body.State != contracts.PullRequestActive ||
		request.Body.Trigger != contracts.PullRequestFreshnessTrigger ||
		len(request.Body.ReviewBatches) != 0 {
		return fmt.Errorf("%w: created PR observation seal request is invalid", ErrInitialPRAuthority)
	}
	return nil
}

// InitialPRObservationSealer must canonicalize, validate, and durably seal the
// exact body. The coordinator reopens its result before binding.
type InitialPRObservationSealer interface {
	SealInitialPRObservation(
		context.Context,
		InitialPRObservationSealRequest,
	) (snapshot.SnapshotRef, error)
}

type InitialPRCoordinator interface {
	PublishInitialPR(
		context.Context,
		InitialPRPublishRequest,
	) (Binding, error)
}

type initialPRCoordinator struct {
	observations MonitorObservationInspector
	candidates   PRRevisionCandidateInspector
	final        InitialPRFinalSnapshotInspector
	evidence     publisher.EvidenceVerifier
	impact       InitialPRImpactVerifier
	publications publisher.PRService
	observer     Observer
	sealer       InitialPRObservationSealer
	bindings     BindingStore
}

func NewInitialPRCoordinator(
	observations MonitorObservationInspector,
	candidates PRRevisionCandidateInspector,
	final InitialPRFinalSnapshotInspector,
	evidence publisher.EvidenceVerifier,
	impact InitialPRImpactVerifier,
	publications publisher.PRService,
	observer Observer,
	sealer InitialPRObservationSealer,
	bindings BindingStore,
) (InitialPRCoordinator, error) {
	for _, dependency := range []any{
		observations, candidates, final, evidence, impact,
		publications, observer, sealer, bindings,
	} {
		if nilInitialPRDependency(dependency) {
			return nil, fmt.Errorf(
				"pullrequest: initial PR coordinator dependencies are required",
			)
		}
	}
	return &initialPRCoordinator{
		observations: observations, candidates: candidates, final: final,
		evidence: evidence, impact: impact, publications: publications,
		observer: observer, sealer: sealer, bindings: bindings,
	}, nil
}

func (coordinator *initialPRCoordinator) PublishInitialPR(
	ctx context.Context,
	request InitialPRPublishRequest,
) (Binding, error) {
	if coordinator == nil || ctx == nil {
		return Binding{}, fmt.Errorf(
			"%w: coordinator and context are required", ErrInitialPRAuthority,
		)
	}
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	authority, accepted, err := protectInitialPRRequest(request)
	if err != nil {
		return Binding{}, err
	}

	missing, err := coordinator.observations.InspectMonitorObservation(
		ctx, authority.Authority.TeamID, request.MissingObservation,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reopen exact missing PR observation: %w", err,
		)
	}
	if err := exactInitialPRMissingObservation(
		missing, authority, request.TargetSHA,
	); err != nil {
		return Binding{}, err
	}

	change, err := coordinator.candidates.InspectExactPRCandidate(
		ctx, authority.Authority.TeamID, request.Candidate,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reopen exact initial PR candidate: %w", err,
		)
	}
	if err := exactInitialPRCandidate(
		change, request.SourceSHA, request.TargetSHA,
	); err != nil {
		_ = change.Close()
		return Binding{}, err
	}
	if err := change.Close(); err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: close exact initial PR candidate: %w", err,
		)
	}

	final, err := coordinator.final.InspectInitialPRFinalSnapshots(
		ctx,
		authority.Authority.TeamID,
		InitialPRFinalSnapshotRefs{
			Validation: request.Validation, Impact: request.Impact,
		},
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reopen exact initial PR validation and impact: %w",
			err,
		)
	}
	final.Impact = cloneInitialPRImpact(final.Impact)
	if err := exactInitialPRFinalSnapshots(
		request, accepted, final,
	); err != nil {
		return Binding{}, err
	}

	evidenceRequest := publisher.AcceptedReviewEvidenceRequest{
		Review: accepted.Review, Candidate: accepted.Candidate,
		Validation:          accepted.Validation,
		ReviewWorkflowRunID: accepted.ReviewWorkflowRunID,
		OutcomeRevision:     accepted.OutcomeRevision,
	}
	evidence, err := coordinator.evidence.Verify(
		ctx,
		publisher.EvidenceRequest{
			TeamID:         authority.Authority.TeamID,
			AcceptedReview: &evidenceRequest,
		},
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reverify exact initial accepted review: %w", err,
		)
	}
	if !evidenceRequest.Matches(evidence) {
		return Binding{}, ErrInitialPRAuthority
	}

	impactRequest := InitialPRImpactVerificationRequest{
		TeamID:        authority.Authority.TeamID,
		WorkflowRunID: authority.Authority.WorkflowRunID,
		PolicyVersion: authority.ApprovalPolicyVersion,
		Destination:   authority.Destination,
		Observation:   request.MissingObservation,
		Baseline:      accepted.Candidate, Candidate: request.Candidate,
		Validation: request.Validation, Impact: request.Impact,
		AcceptedReview: evidence.Clone(),
		SourceRef:      authority.SourceRef,
		ExpectedSource: *missing.ExpectedSource,
		SourceSHA:      request.SourceSHA,
		TargetRef:      authority.TargetRef, TargetSHA: request.TargetSHA,
		Body: cloneInitialPRImpact(final.Impact),
	}
	if err := impactRequest.Validate(); err != nil {
		return Binding{}, err
	}
	verifiedImpact, err := coordinator.impact.VerifyInitialPRImpact(
		ctx, impactRequest,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: verify exact initial PR impact: %w", err,
		)
	}
	if verifiedImpact.Validate(nil) != nil ||
		!reflect.DeepEqual(verifiedImpact, final.Impact) ||
		verifiedImpact.ReapprovalRequired {
		return Binding{}, fmt.Errorf(
			"%w: accepted review does not authorize the final initial PR impact",
			ErrInitialPRAuthority,
		)
	}

	locator := publisher.PRLocator{
		Provider:   publisher.PRProvider(authority.Provider),
		Repository: authority.Repository,
	}
	branch := publisher.BranchPublicationRequest{
		Authority:   authority.Authority,
		Observation: request.MissingObservation,
		Candidate:   request.Candidate, Validation: request.Validation,
		Impact: request.Impact, Evidence: evidence.Clone(),
		Destination:           authority.Destination,
		ApprovalPolicyVersion: authority.ApprovalPolicyVersion,
		Locator:               locator, SourceRef: authority.SourceRef,
		TargetRef:         authority.TargetRef,
		ExpectedSource:    *missing.ExpectedSource,
		ExpectedTargetSHA: request.TargetSHA,
		NewSourceSHA:      request.SourceSHA,
	}
	if err := branch.Validate(); err != nil {
		return Binding{}, fmt.Errorf(
			"%w: exact initial branch operation is invalid: %v",
			ErrInitialPRAuthority, err,
		)
	}
	branchPublication, err := coordinator.publications.PublishBranch(
		ctx, branch,
	)
	if err != nil {
		return Binding{}, err
	}
	if err := exactInitialPRBranchPublication(
		branchPublication, branch,
	); err != nil {
		return Binding{}, err
	}
	if branchPublication.Status == publisher.StatusRebaseRequired {
		return Binding{}, ErrInitialPRStale
	}

	create := publisher.PullRequestPublicationRequest{
		Authority:   authority.Authority,
		Observation: request.MissingObservation,
		Candidate:   request.Candidate, Validation: request.Validation,
		Impact: request.Impact, Evidence: evidence.Clone(),
		Destination:           authority.Destination,
		ApprovalPolicyVersion: authority.ApprovalPolicyVersion,
		Locator:               locator, SourceRef: authority.SourceRef,
		SourceSHA: request.SourceSHA, TargetRef: authority.TargetRef,
		TargetSHA: request.TargetSHA, Title: authority.Title,
		Body: authority.Body,
	}
	if err := create.Validate(); err != nil {
		return Binding{}, fmt.Errorf(
			"%w: exact initial create operation is invalid: %v",
			ErrInitialPRAuthority, err,
		)
	}
	createPublication, err := coordinator.publications.FindOrCreate(
		ctx, create,
	)
	if err != nil {
		return Binding{}, err
	}
	if err := exactInitialPRCreatePublication(
		createPublication, create,
	); err != nil {
		return Binding{}, err
	}

	createdLocator := Locator{
		Provider: authority.Provider, Repository: authority.Repository,
		ExternalID: createPublication.Result.ExternalID,
	}
	existing, found, err := coordinator.bindings.GetByExternal(
		ctx, authority.Authority.TeamID, createdLocator,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: recover initial PR binding: %w", err,
		)
	}
	if found {
		if err := exactInitialPRExistingBinding(
			existing, authority, branchPublication, createPublication,
		); err != nil {
			return Binding{}, err
		}
		return existing, nil
	}
	observation, err := coordinator.observer.Observe(
		ctx, createdLocator, "",
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reobserve created PR: %w", err,
		)
	}
	if err := exactInitialPRCreatedObservation(
		observation, createdLocator, createPublication.Result.URL,
		authority, request.SourceSHA, request.TargetSHA,
	); err != nil {
		return Binding{}, err
	}
	body, err := initialPRCreatedObservationBody(observation)
	if err != nil {
		return Binding{}, err
	}
	sealRequest := InitialPRObservationSealRequest{
		TeamID:        authority.Authority.TeamID,
		WorkflowRunID: authority.Authority.WorkflowRunID,
		Cursor:        observation.Cursor, Body: body,
	}
	if err := sealRequest.Validate(); err != nil {
		return Binding{}, err
	}
	sealed, err := coordinator.sealer.SealInitialPRObservation(
		ctx, sealRequest,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: seal created PR observation: %w", err,
		)
	}
	if sealed.Validate() != nil ||
		sealed.Type != snapshot.TypeRef("pull-request/v1") {
		return Binding{}, fmt.Errorf(
			"%w: created PR sealer returned an invalid reference",
			ErrInitialPRAuthority,
		)
	}
	reopened, err := coordinator.observations.InspectMonitorObservation(
		ctx, authority.Authority.TeamID, sealed,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: reopen sealed created PR observation: %w", err,
		)
	}
	if !reflect.DeepEqual(reopened, body) {
		return Binding{}, fmt.Errorf(
			"%w: sealed created PR observation substituted provider facts",
			ErrInitialPRAuthority,
		)
	}

	createBinding := CreateBinding{
		TeamID:  authority.Authority.TeamID,
		Locator: observation.Locator, URL: observation.URL,
		SourceRef: authority.SourceRef, TargetRef: authority.TargetRef,
		Destination:                      authority.Destination,
		ApprovalPolicyVersion:            authority.ApprovalPolicyVersion,
		OriginatingWorkflowRunID:         authority.Authority.WorkflowRunID,
		OriginatingPublicationOccurrence: int64(branchPublication.ID),
		CreationPublicationOccurrenceID:  int64(createPublication.ID),
		MonitorWorkflowDefinitionID:      authority.MonitorWorkflowDefinitionID,
		MonitorWorkflowVersion:           authority.MonitorWorkflowVersion,
		AcknowledgedCursor:               observation.Cursor,
		LastObservationSnapshotID:        sealed.ID,
		LastReconciledSourceSHA:          observation.SourceSHA,
		LastReconciledTargetSHA:          observation.TargetSHA,
		LastReconciledAt: time.Now().UTC().Truncate(
			time.Microsecond,
		),
	}
	if err := createBinding.Validate(); err != nil {
		return Binding{}, fmt.Errorf(
			"%w: exact initial binding request is invalid: %v",
			ErrInitialPRAuthority, err,
		)
	}
	binding, created, err := coordinator.bindings.Create(
		ctx, createBinding,
	)
	if err != nil {
		return Binding{}, fmt.Errorf(
			"pullrequest: create initial PR binding: %w", err,
		)
	}
	if err := exactInitialPRBinding(
		binding, createBinding, accepted, created,
	); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func protectInitialPRRequest(
	request InitialPRPublishRequest,
) (
	InitialPRServerAuthority,
	AcceptedReviewAuthority,
	error,
) {
	authority, err := request.Authority.Protected()
	if err != nil {
		return InitialPRServerAuthority{}, AcceptedReviewAuthority{}, err
	}
	accepted, err := request.AcceptedReview.Protected()
	if err != nil {
		return InitialPRServerAuthority{}, AcceptedReviewAuthority{}, err
	}
	if accepted.TeamID != authority.Authority.TeamID ||
		request.MissingObservation.Validate() != nil ||
		request.MissingObservation.Type != snapshot.TypeRef("pull-request/v1") ||
		request.Candidate.Validate() != nil ||
		request.Candidate.Type != snapshot.TypeRef("repository-change/v1") ||
		request.Validation.Validate() != nil ||
		request.Validation.Type != snapshot.TypeRef("validation/v1") ||
		request.Impact.Validate() != nil ||
		request.Impact.Type != snapshot.TypeRef("publish-impact/v1") ||
		validateObjectID("initial PR source sha", request.SourceSHA) != nil ||
		validateObjectID("initial PR target sha", request.TargetSHA) != nil ||
		len(request.SourceSHA) != len(request.TargetSHA) {
		return InitialPRServerAuthority{}, AcceptedReviewAuthority{},
			ErrInitialPRAuthority
	}
	return authority, accepted, nil
}

func exactInitialPRMissingObservation(
	body contracts.PullRequestBody,
	authority InitialPRServerAuthority,
	targetSHA string,
) error {
	if body.Validate(nil) != nil ||
		body.State != contracts.PullRequestMissing ||
		body.Trigger != contracts.PullRequestInitialPublishTrigger ||
		body.Provider != string(authority.Provider) ||
		body.Repository != authority.Repository ||
		body.SourceRef != authority.SourceRef ||
		body.TargetRef != authority.TargetRef ||
		body.TargetSHA != targetSHA ||
		body.ExpectedSource == nil ||
		len(body.ReviewBatches) != 0 ||
		len(body.Threads) != 0 {
		return fmt.Errorf(
			"%w: sealed missing PR observation does not match publication authority",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRCandidate(
	change publisher.RepositoryChange,
	sourceSHA string,
	targetSHA string,
) error {
	if change.Validate() != nil ||
		change.BaseSHA != targetSHA ||
		change.ResultSHA != sourceSHA ||
		validateObjectID("initial PR candidate base sha", change.BaseSHA) != nil ||
		validateObjectID("initial PR candidate result sha", change.ResultSHA) != nil ||
		len(change.BaseSHA) != len(change.ResultSHA) {
		return fmt.Errorf(
			"%w: exact initial candidate does not bind the requested heads",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRFinalSnapshots(
	request InitialPRPublishRequest,
	accepted AcceptedReviewAuthority,
	final InitialPRFinalSnapshots,
) error {
	if final.ValidationCandidate.Validate() != nil ||
		final.ValidationCandidate.Type != request.Candidate.Type ||
		final.ValidationCandidate.Digest != request.Candidate.Digest ||
		final.ValidationConclusion != "passed" ||
		final.Impact.Validate(nil) != nil ||
		final.Impact.BaselineDigest != accepted.Candidate.Digest.String() ||
		final.Impact.CandidateDigest != request.Candidate.Digest.String() {
		return fmt.Errorf(
			"%w: final validation and impact do not bind exact initial inputs",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRCreatedObservation(
	observation Observation,
	locator Locator,
	url string,
	authority InitialPRServerAuthority,
	sourceSHA string,
	targetSHA string,
) error {
	if observation.Validate() != nil ||
		observation.State != contracts.PullRequestActive ||
		observation.Locator != locator ||
		observation.URL != url ||
		observation.SourceRef != authority.SourceRef ||
		observation.SourceSHA != sourceSHA ||
		observation.TargetRef != authority.TargetRef ||
		observation.TargetSHA != targetSHA ||
		observation.Cursor == "" ||
		len(observation.ReviewBatches) != 0 {
		return fmt.Errorf(
			"%w: provider reobservation does not match the created active PR",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func initialPRCreatedObservationBody(
	observation Observation,
) (contracts.PullRequestBody, error) {
	cloned := observation.Clone()
	body := contracts.PullRequestBody{
		Provider:   string(cloned.Provider),
		Repository: cloned.Repository, ExternalID: cloned.ExternalID,
		URL: cloned.URL, State: cloned.State,
		Mergeability: cloned.Mergeability,
		SourceRef:    cloned.SourceRef, SourceSHA: cloned.SourceSHA,
		TargetRef: cloned.TargetRef, TargetSHA: cloned.TargetSHA,
		Iteration:     cloned.Iteration,
		Trigger:       contracts.PullRequestFreshnessTrigger,
		ReviewBatches: cloned.ReviewBatches, Threads: cloned.Threads,
	}
	if err := body.Validate(nil); err != nil {
		return contracts.PullRequestBody{}, fmt.Errorf(
			"%w: normalize created PR observation: %v",
			ErrInitialPRAuthority, err,
		)
	}
	return body, nil
}

func exactInitialPRBranchPublication(
	publication publisher.Publication,
	request publisher.BranchPublicationRequest,
) error {
	action := publisher.PRAction{
		Kind: publisher.OperationPublishPRBranch, Branch: &request,
	}
	if err := exactInitialPRPublication(publication, action); err != nil {
		return err
	}
	switch publication.Status {
	case publisher.StatusSucceeded:
		if publication.Result.ExternalID != request.SourceRef ||
			publication.Result.URL != "" ||
			publication.Result.HeadSHA != request.NewSourceSHA ||
			publication.Result.BaseSHA != request.ExpectedTargetSHA ||
			publication.Result.Detail != "" {
			return ErrInitialPRAuthority
		}
	case publisher.StatusRebaseRequired:
		if publication.Result.ExternalID != "" ||
			publication.Result.URL != "" ||
			publication.Result.HeadSHA != request.NewSourceSHA ||
			publication.Result.BaseSHA != request.ExpectedTargetSHA ||
			strings.TrimSpace(publication.Result.Detail) == "" {
			return ErrInitialPRAuthority
		}
	default:
		return ErrInitialPRAuthority
	}
	return nil
}

func exactInitialPRCreatePublication(
	publication publisher.Publication,
	request publisher.PullRequestPublicationRequest,
) error {
	action := publisher.PRAction{
		Kind: publisher.OperationCreatePR, PullRequest: &request,
	}
	if err := exactInitialPRPublication(publication, action); err != nil {
		return err
	}
	if publication.Status != publisher.StatusSucceeded ||
		validateIdentifier(
			"created PR external id",
			publication.Result.ExternalID,
			maxExternalIDBytes,
		) != nil ||
		validateURL(publication.Result.URL) != nil ||
		publication.Result.HeadSHA != request.SourceSHA ||
		publication.Result.BaseSHA != request.TargetSHA ||
		publication.Result.Detail != "" {
		return fmt.Errorf(
			"%w: create result is absent, ambiguous, or mismatched",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRPublication(
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
		return ErrInitialPRAuthority
	}
	key, err := action.OperationKey()
	if err != nil || publication.OperationKey != key {
		return ErrInitialPRAuthority
	}
	return nil
}

func exactInitialPRBinding(
	binding Binding,
	request CreateBinding,
	accepted AcceptedReviewAuthority,
	created bool,
) error {
	if err := exactInitialPRBindingCreationAuthority(
		binding,
		request.TeamID,
		request.Locator,
		request.URL,
		request.SourceRef,
		request.TargetRef,
		request.Destination,
		request.ApprovalPolicyVersion,
		request.OriginatingWorkflowRunID,
		request.OriginatingPublicationOccurrence,
		request.CreationPublicationOccurrenceID,
		request.MonitorWorkflowDefinitionID,
		request.MonitorWorkflowVersion,
	); err != nil {
		return err
	}
	if !created {
		return nil
	}
	if binding.AcknowledgedCursor != request.AcknowledgedCursor ||
		binding.LastObservationSnapshotID == nil ||
		*binding.LastObservationSnapshotID !=
			request.LastObservationSnapshotID ||
		binding.LastReconciledSourceSHA !=
			request.LastReconciledSourceSHA ||
		binding.LastReconciledTargetSHA !=
			request.LastReconciledTargetSHA ||
		!binding.LastReconciledAt.Equal(request.LastReconciledAt) ||
		binding.State != BindingActive ||
		binding.AttentionReason != "" ||
		binding.Paused ||
		binding.OperatorTerminated ||
		binding.ObservationRequestedAt != nil ||
		binding.Active != nil ||
		binding.TerminalObservationSnapshotID != nil ||
		binding.TerminalAt != nil ||
		binding.PipelineID != nil ||
		binding.LastAcknowledgedActionDigest != "" ||
		binding.LastAcknowledgedWorkflowRunID != nil ||
		binding.Revision != 1 ||
		binding.CreatedAt.IsZero() ||
		binding.UpdatedAt.IsZero() ||
		!binding.UpdatedAt.Equal(binding.CreatedAt) ||
		binding.ApprovedBaselineRepositorySnapshotID !=
			accepted.Candidate.ID ||
		binding.ApprovedBaselineValidationSnapshotID !=
			accepted.Validation.ID ||
		binding.ApprovedBaselinePublicationOccurrenceID !=
			request.OriginatingPublicationOccurrence {
		return fmt.Errorf(
			"%w: new binding did not return the exact initial projection",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRExistingBinding(
	binding Binding,
	authority InitialPRServerAuthority,
	branchPublication publisher.Publication,
	createPublication publisher.Publication,
) error {
	locator := Locator{
		Provider: authority.Provider, Repository: authority.Repository,
		ExternalID: createPublication.Result.ExternalID,
	}
	if err := exactInitialPRBindingCreationAuthority(
		binding,
		authority.Authority.TeamID,
		locator,
		createPublication.Result.URL,
		authority.SourceRef,
		authority.TargetRef,
		authority.Destination,
		authority.ApprovalPolicyVersion,
		authority.Authority.WorkflowRunID,
		int64(branchPublication.ID),
		int64(createPublication.ID),
		authority.MonitorWorkflowDefinitionID,
		authority.MonitorWorkflowVersion,
	); err != nil {
		return err
	}
	if binding.State.Validate() != nil ||
		binding.Revision <= 0 ||
		binding.CreatedAt.IsZero() ||
		binding.UpdatedAt.IsZero() ||
		binding.UpdatedAt.Before(binding.CreatedAt) ||
		binding.ApprovedBaselineRepositorySnapshotID <= 0 ||
		binding.ApprovedBaselineValidationSnapshotID <= 0 ||
		binding.ApprovedBaselinePublicationOccurrenceID <= 0 {
		return fmt.Errorf(
			"%w: recovered binding projection is invalid",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func exactInitialPRBindingCreationAuthority(
	binding Binding,
	teamID int,
	locator Locator,
	url string,
	sourceRef string,
	targetRef string,
	destination string,
	approvalPolicyVersion string,
	originatingWorkflowRunID snapshot.WorkflowRunID,
	originatingPublicationOccurrence int64,
	creationPublicationOccurrenceID int64,
	monitorWorkflowDefinitionID int,
	monitorWorkflowVersion int,
) error {
	if binding.ID <= 0 ||
		binding.TeamID != teamID ||
		binding.Locator != locator ||
		binding.URL != url ||
		binding.SourceRef != sourceRef ||
		binding.TargetRef != targetRef ||
		binding.Destination != destination ||
		binding.ApprovalPolicyVersion != approvalPolicyVersion ||
		binding.OriginatingWorkflowRunID == nil ||
		*binding.OriginatingWorkflowRunID != originatingWorkflowRunID ||
		binding.OriginatingPublicationOccurrence == nil ||
		*binding.OriginatingPublicationOccurrence !=
			originatingPublicationOccurrence ||
		binding.CreationPublicationOccurrenceID == nil ||
		*binding.CreationPublicationOccurrenceID !=
			creationPublicationOccurrenceID ||
		binding.MonitorWorkflowDefinitionID !=
			monitorWorkflowDefinitionID ||
		binding.MonitorWorkflowVersion != monitorWorkflowVersion {
		return fmt.Errorf(
			"%w: binding store returned mismatched creation authority",
			ErrInitialPRAuthority,
		)
	}
	return nil
}

func cloneInitialPRImpact(
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

func nilInitialPRDependency(value any) bool {
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

var _ InitialPRCoordinator = (*initialPRCoordinator)(nil)
