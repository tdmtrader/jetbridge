package pullrequest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestInitialPRCoordinatorPublishesReobservesSealsAndCreatesBinding(t *testing.T) {
	fixture := newInitialPRCoordinatorFixture(t)

	binding, err := fixture.coordinator.PublishInitialPR(
		context.Background(), fixture.request,
	)
	if err != nil {
		t.Fatalf("publish initial PR: %v", err)
	}
	if binding.ID != 5001 ||
		binding.Locator.ExternalID != "42" ||
		binding.CreationPublicationOccurrenceID == nil ||
		*binding.CreationPublicationOccurrenceID != 1002 {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	expectedOrder := []string{
		"inspect-missing", "inspect-candidate", "inspect-final",
		"verify-evidence", "verify-impact", "publish-branch",
		"create-pr", "get-binding", "observe-created", "seal-created",
		"inspect-sealed", "create-binding",
	}
	if !reflect.DeepEqual(fixture.events, expectedOrder) {
		t.Fatalf("unexpected order:\n got: %#v\nwant: %#v", fixture.events, expectedOrder)
	}
	if len(fixture.publications.branchRequests) != 1 ||
		len(fixture.publications.createRequests) != 1 {
		t.Fatalf("unexpected publication counts")
	}
	branch := fixture.publications.branchRequests[0]
	created := fixture.publications.createRequests[0]
	if branch.ExpectedSource.Exists ||
		branch.ExpectedSource.SHA != "" ||
		branch.NewSourceSHA != initialPRSHA('c') ||
		created.SourceSHA != initialPRSHA('c') ||
		created.TargetSHA != initialPRSHA('b') ||
		created.Locator.ExternalID != "" {
		t.Fatalf("publication did not preserve exact missing/head authority")
	}
	if branch.Evidence.Kind != publisher.EvidenceAcceptedReview ||
		created.Evidence.Kind != publisher.EvidenceAcceptedReview ||
		!reflect.DeepEqual(branch.Evidence, created.Evidence) {
		t.Fatalf("publication evidence was not exact")
	}
	if fixture.observer.cursor != "" {
		t.Fatalf("created PR must be reobserved from an empty provider cursor")
	}
	if fixture.sealer.request.Body.Trigger !=
		contracts.PullRequestFreshnessTrigger ||
		fixture.sealer.request.Body.ExternalID != "42" ||
		fixture.sealer.request.Body.SourceSHA != initialPRSHA('c') {
		t.Fatalf("sealer did not receive the exact provider observation")
	}
	create := fixture.bindings.requests[0]
	if create.OriginatingPublicationOccurrence != 901 ||
		create.CreationPublicationOccurrenceID != 1002 ||
		create.AcknowledgedCursor != "opaque-created-cursor" ||
		create.LastObservationSnapshotID != fixture.sealer.reference.ID ||
		create.LastReconciledSourceSHA != initialPRSHA('c') ||
		create.LastReconciledTargetSHA != initialPRSHA('b') {
		t.Fatalf("binding did not retain exact creation authority: %#v", create)
	}
}

func TestInitialPRCoordinatorRetryReusesDurableOperationAndBindingIdentities(t *testing.T) {
	fixture := newInitialPRCoordinatorFixture(t)

	first, err := fixture.coordinator.PublishInitialPR(
		context.Background(), fixture.request,
	)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	second, err := fixture.coordinator.PublishInitialPR(
		context.Background(), fixture.request,
	)
	if err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if first.ID != second.ID || len(fixture.bindings.requests) != 1 {
		t.Fatalf("retry did not reuse the binding")
	}
	firstBranch := fixture.publications.branchRequests[0]
	secondBranch := fixture.publications.branchRequests[1]
	firstBranchKey, err := firstBranch.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	secondBranchKey, err := secondBranch.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	firstCreate := fixture.publications.createRequests[0]
	secondCreate := fixture.publications.createRequests[1]
	firstCreateKey, err := firstCreate.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	secondCreateKey, err := secondCreate.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if firstBranchKey != secondBranchKey ||
		firstCreateKey != secondCreateKey ||
		!reflect.DeepEqual(firstBranch, secondBranch) ||
		!reflect.DeepEqual(firstCreate, secondCreate) {
		t.Fatalf("retry changed a durable publisher identity")
	}
	if fixture.observer.calls != 1 || fixture.sealer.calls != 1 {
		t.Fatalf("retry reobserved or resealed an existing binding")
	}
}

func TestInitialPRCoordinatorRecoversExistingBindingBeforeProviderProgress(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*initialPRCoordinatorFixture, *Binding)
	}{
		{
			name: "ready review batch",
			mutate: func(
				fixture *initialPRCoordinatorFixture,
				_ *Binding,
			) {
				fixture.observer.observation.ReviewBatches =
					[]ReviewBatch{{
						ID: "batch-2", ReviewID: "review-2",
						CommitSHA: initialPRSHA('c'),
						Reviewer:  "reviewer", Ready: true,
					}}
			},
		},
		{
			name: "advanced head",
			mutate: func(
				fixture *initialPRCoordinatorFixture,
				binding *Binding,
			) {
				binding.AcknowledgedCursor = "advanced-cursor"
				binding.LastReconciledSourceSHA = initialPRSHA('d')
				binding.Revision = 4
				binding.ApprovedBaselineRepositorySnapshotID = 801
				binding.ApprovedBaselineValidationSnapshotID = 802
				binding.ApprovedBaselinePublicationOccurrenceID = 803
				fixture.observer.observation.SourceSHA = initialPRSHA('d')
			},
		},
		{
			name: "terminal provider state",
			mutate: func(
				fixture *initialPRCoordinatorFixture,
				binding *Binding,
			) {
				terminalObservation := snapshot.SnapshotID(901)
				terminalAt := time.Date(
					2026, time.July, 30, 9, 0, 0, 0, time.UTC,
				)
				binding.State = BindingCompleted
				binding.TerminalObservationSnapshotID =
					&terminalObservation
				binding.TerminalAt = &terminalAt
				binding.Revision = 5
				fixture.observer.observation.State =
					contracts.PullRequestCompleted
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPRCoordinatorFixture(t)
			first, err := fixture.coordinator.PublishInitialPR(
				context.Background(), fixture.request,
			)
			if err != nil {
				t.Fatalf("first publish: %v", err)
			}
			test.mutate(fixture, fixture.bindings.binding)
			progressed := *fixture.bindings.binding

			recovered, err := fixture.coordinator.PublishInitialPR(
				context.Background(), fixture.request,
			)
			if err != nil {
				t.Fatalf("recover progressed binding: %v", err)
			}
			if first.ID != recovered.ID ||
				!reflect.DeepEqual(recovered, progressed) {
				t.Fatalf("did not return exact progressed binding")
			}
			if fixture.observer.calls != 1 ||
				fixture.sealer.calls != 1 ||
				len(fixture.bindings.requests) != 1 {
				t.Fatalf(
					"recovery touched provider/sealer/create: observe=%d seal=%d create=%d",
					fixture.observer.calls, fixture.sealer.calls,
					len(fixture.bindings.requests),
				)
			}
		})
	}
}

func TestInitialPRCoordinatorRejectsForgedExistingBindingBeforeReobservation(t *testing.T) {
	fixture := newInitialPRCoordinatorFixture(t)
	_, err := fixture.coordinator.PublishInitialPR(
		context.Background(), fixture.request,
	)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	forgedOccurrence := int64(9999)
	fixture.bindings.binding.CreationPublicationOccurrenceID =
		&forgedOccurrence

	_, err = fixture.coordinator.PublishInitialPR(
		context.Background(), fixture.request,
	)
	if err == nil {
		t.Fatal("expected forged existing binding rejection")
	}
	if fixture.observer.calls != 1 ||
		fixture.sealer.calls != 1 ||
		len(fixture.bindings.requests) != 1 {
		t.Fatal("forged existing binding reached reobservation or creation")
	}
}

func TestInitialPRCoordinatorRejectsMalformedNewBindingProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Binding)
	}{
		{
			name: "cursor mismatch",
			mutate: func(binding *Binding) {
				binding.AcknowledgedCursor = "other-cursor"
			},
		},
		{
			name: "observation mismatch",
			mutate: func(binding *Binding) {
				value := snapshot.SnapshotID(999)
				binding.LastObservationSnapshotID = &value
			},
		},
		{
			name: "reconciled head mismatch",
			mutate: func(binding *Binding) {
				binding.LastReconciledSourceSHA = initialPRSHA('d')
			},
		},
		{
			name: "reconciliation time mismatch",
			mutate: func(binding *Binding) {
				binding.LastReconciledAt =
					binding.LastReconciledAt.Add(time.Second)
			},
		},
		{
			name: "attention state",
			mutate: func(binding *Binding) {
				binding.State = BindingAttentionRequired
				binding.AttentionReason = "unexpected"
			},
		},
		{
			name: "paused",
			mutate: func(binding *Binding) {
				binding.Paused = true
			},
		},
		{
			name: "active reservation",
			mutate: func(binding *Binding) {
				binding.Active = &LaunchReservation{
					BindingID: binding.ID,
				}
			},
		},
		{
			name: "unexpected revision",
			mutate: func(binding *Binding) {
				binding.Revision = 2
			},
		},
		{
			name: "acknowledged action",
			mutate: func(binding *Binding) {
				runID := snapshot.WorkflowRunID(902)
				binding.LastAcknowledgedActionDigest =
					"sha256:" + strings.Repeat("a", 64)
				binding.LastAcknowledgedWorkflowRunID = &runID
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPRCoordinatorFixture(t)
			fixture.bindings.mutateNew = test.mutate

			_, err := fixture.coordinator.PublishInitialPR(
				context.Background(), fixture.request,
			)
			if err == nil {
				t.Fatal("expected malformed new binding rejection")
			}
		})
	}
}

func TestInitialPRCoordinatorFailsClosedBeforeBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*initialPRCoordinatorFixture)
		stale  bool
		stage  string
	}{
		{
			name: "branch rebase required",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.publications.branchStatus =
					publisher.StatusRebaseRequired
			},
			stale: true,
			stage: "publish-branch",
		},
		{
			name: "ambiguous create result",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.publications.ambiguousCreate = true
			},
			stage: "create-pr",
		},
		{
			name: "terminal reobservation",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.observer.observation.State =
					contracts.PullRequestCompleted
			},
			stage: "observe-created",
		},
		{
			name: "mismatched reobservation",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.observer.observation.SourceSHA =
					initialPRSHA('d')
			},
			stage: "observe-created",
		},
		{
			name: "empty reobservation cursor",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.observer.observation.Cursor = ""
			},
			stage: "observe-created",
		},
		{
			name: "ready review would be skipped",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.observer.observation.ReviewBatches =
					[]ReviewBatch{{
						ID: "batch-1", ReviewID: "review-1",
						CommitSHA: initialPRSHA('c'),
						Reviewer:  "reviewer", Ready: true,
					}}
			},
			stage: "observe-created",
		},
		{
			name: "sealing failure",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.sealer.err = errors.New("seal unavailable")
			},
			stage: "seal-created",
		},
		{
			name: "sealed observation substitution",
			mutate: func(fixture *initialPRCoordinatorFixture) {
				fixture.sealer.substitute = true
			},
			stage: "inspect-sealed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPRCoordinatorFixture(t)
			test.mutate(fixture)

			_, err := fixture.coordinator.PublishInitialPR(
				context.Background(), fixture.request,
			)
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
			if test.stale != errors.Is(err, ErrInitialPRStale) {
				t.Fatalf("unexpected stale classification: %v", err)
			}
			if len(fixture.bindings.requests) != 0 {
				t.Fatalf("binding was created after %s failure", test.stage)
			}
			if test.stage == "publish-branch" &&
				len(fixture.publications.createRequests) != 0 {
				t.Fatal("PR create ran after stale branch")
			}
			if (test.stage == "create-pr" ||
				test.stage == "observe-created") &&
				fixture.sealer.calls != 0 {
				t.Fatal("observation was sealed after earlier failure")
			}
		})
	}
}

func TestInitialPRCoordinatorRejectsAlteredProtectedOrMissingAuthorityBeforeMutation(t *testing.T) {
	t.Run("protected config changed", func(t *testing.T) {
		fixture := newInitialPRCoordinatorFixture(t)
		fixture.request.Authority.SourceRef =
			"refs/heads/forged"

		_, err := fixture.coordinator.PublishInitialPR(
			context.Background(), fixture.request,
		)
		if err == nil || len(fixture.publications.branchRequests) != 0 {
			t.Fatalf("altered protected authority was not rejected: %v", err)
		}
	})

	t.Run("accepted review changed", func(t *testing.T) {
		fixture := newInitialPRCoordinatorFixture(t)
		fixture.request.AcceptedReview.Candidate =
			initialPRSnapshotRef(99, "repository/v1", '9')

		_, err := fixture.coordinator.PublishInitialPR(
			context.Background(), fixture.request,
		)
		if err == nil || len(fixture.publications.branchRequests) != 0 {
			t.Fatalf("altered accepted review was not rejected: %v", err)
		}
	})

	t.Run("missing observation mismatches target", func(t *testing.T) {
		fixture := newInitialPRCoordinatorFixture(t)
		fixture.observations.bodies[fixture.request.MissingObservation.ID] =
			initialPRMissingBody(initialPRSHA('d'))

		_, err := fixture.coordinator.PublishInitialPR(
			context.Background(), fixture.request,
		)
		if err == nil || len(fixture.publications.branchRequests) != 0 {
			t.Fatalf("mismatched missing observation was not rejected: %v", err)
		}
	})
}

type initialPRCoordinatorFixture struct {
	t            *testing.T
	events       []string
	request      InitialPRPublishRequest
	coordinator  InitialPRCoordinator
	observations *initialPRObservationInspector
	candidates   *initialPRCandidateInspector
	snapshots    *initialPRFinalSnapshotInspector
	evidence     *initialPREvidenceVerifier
	impact       *initialPRImpactVerifier
	publications *initialPRPublicationService
	observer     *initialPRProviderObserver
	sealer       *initialPRObservationSealer
	bindings     *initialPRBindingStore
}

func newInitialPRCoordinatorFixture(t *testing.T) *initialPRCoordinatorFixture {
	t.Helper()
	fixture := &initialPRCoordinatorFixture{t: t}
	event := func(value string) {
		fixture.events = append(fixture.events, value)
	}
	missing := initialPRSnapshotRef(501, "pull-request/v1", '1')
	candidate := initialPRSnapshotRef(502, "repository-change/v1", '2')
	validation := initialPRSnapshotRef(503, "validation/v1", '3')
	impact := initialPRSnapshotRef(504, "publish-impact/v1", '4')
	review := initialPRSnapshotRef(601, "review/v1", '5')
	baseline := initialPRSnapshotRef(602, "repository/v1", '6')
	baselineValidation :=
		initialPRSnapshotRef(603, "validation/v1", '7')
	sealedCreated :=
		initialPRSnapshotRef(701, "pull-request/v1", '8')

	serverAuthority, err := NewInitialPRServerAuthority(
		InitialPRServerAuthoritySpec{
			Authority: publisher.Authority{
				TeamID: 7, TeamName: "main", BuildID: 81,
				WorkflowRunID: 71, Actor: "concourse",
			},
			Provider:                    ProviderGitHub,
			Repository:                  "acme/widget",
			Destination:                 "github-production",
			ApprovalPolicyVersion:       "policy-v3",
			SourceRef:                   "refs/heads/agent/change-1",
			TargetRef:                   "refs/heads/main",
			Title:                       "Validated change",
			Body:                        "Ready for provider review.",
			MonitorWorkflowDefinitionID: 91,
			MonitorWorkflowVersion:      3,
		},
	)
	if err != nil {
		t.Fatalf("server authority: %v", err)
	}
	accepted, err := NewAcceptedReviewAuthority(
		AcceptedReviewAuthoritySpec{
			TeamID: 7, PublicationOccurrenceID: 901,
			Review: review, Candidate: baseline,
			Validation:          baselineValidation,
			ReviewWorkflowRunID: 61, OutcomeRevision: 2,
		},
	)
	if err != nil {
		t.Fatalf("accepted review: %v", err)
	}
	fixture.request = InitialPRPublishRequest{
		Authority: serverAuthority, AcceptedReview: accepted,
		MissingObservation: missing, Candidate: candidate,
		Validation: validation, Impact: impact,
		SourceSHA: initialPRSHA('c'), TargetSHA: initialPRSHA('b'),
	}
	fixture.observations = &initialPRObservationInspector{
		bodies: map[snapshot.SnapshotID]contracts.PullRequestBody{
			missing.ID: initialPRMissingBody(initialPRSHA('b')),
		},
		event: event,
	}
	fixture.candidates = &initialPRCandidateInspector{
		change: publisher.RepositoryChange{
			BaseSHA: initialPRSHA('b'), ResultSHA: initialPRSHA('c'),
			MaterializedRoot: "/private/tmp/initial-pr-change",
		},
		event: event,
	}
	impactBody := contracts.PublishImpactBody{
		BaselineDigest:  baseline.Digest.String(),
		CandidateDigest: candidate.Digest.String(),
		ChangedFiles: []contracts.PublishChangedFile{{
			Path: "README.md", AddedLines: 1,
		}},
		ChangedLines: 1,
		AgentAssessment: &contracts.AgentImpactAssessment{
			ReapprovalRequired: false,
			Rationale:          "The reviewed intent is unchanged.",
		},
	}
	fixture.snapshots = &initialPRFinalSnapshotInspector{
		result: InitialPRFinalSnapshots{
			ValidationCandidate: contracts.StableSnapshotRef{
				Type: candidate.Type, Digest: candidate.Digest,
			},
			ValidationConclusion: "passed",
			Impact:               impactBody,
		},
		event: event,
	}
	acceptedEvidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review: review, Candidate: baseline,
			Validation:          baselineValidation,
			ReviewWorkflowRunID: 61, OutcomeRevision: 2,
			AcceptedBy: "reviewer",
			AcceptedAt: time.Date(
				2026, time.July, 30, 6, 0, 0, 0, time.UTC,
			),
		},
	}
	fixture.evidence = &initialPREvidenceVerifier{
		result: acceptedEvidence, event: event,
	}
	fixture.impact = &initialPRImpactVerifier{
		result: impactBody, event: event,
	}
	fixture.publications = &initialPRPublicationService{
		event: event, branchStatus: publisher.StatusSucceeded,
	}
	fixture.observer = &initialPRProviderObserver{
		observation: initialPRCreatedObservation(),
		event:       event,
	}
	fixture.sealer = &initialPRObservationSealer{
		reference: sealedCreated,
		inspector: fixture.observations,
		event:     event,
	}
	fixture.bindings = &initialPRBindingStore{event: event}

	coordinator, err := NewInitialPRCoordinator(
		fixture.observations,
		fixture.candidates,
		fixture.snapshots,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		fixture.observer,
		fixture.sealer,
		fixture.bindings,
	)
	if err != nil {
		t.Fatalf("coordinator: %v", err)
	}
	fixture.coordinator = coordinator
	return fixture
}

type initialPRObservationInspector struct {
	bodies map[snapshot.SnapshotID]contracts.PullRequestBody
	event  func(string)
}

func (inspector *initialPRObservationInspector) InspectMonitorObservation(
	_ context.Context,
	_ int,
	reference snapshot.SnapshotRef,
) (contracts.PullRequestBody, error) {
	if reference.ID == 501 {
		inspector.event("inspect-missing")
	} else {
		inspector.event("inspect-sealed")
	}
	body, found := inspector.bodies[reference.ID]
	if !found {
		return contracts.PullRequestBody{}, snapshot.ErrNotFound
	}
	return body, nil
}

type initialPRCandidateInspector struct {
	change publisher.RepositoryChange
	event  func(string)
}

func (inspector *initialPRCandidateInspector) InspectExactPRCandidate(
	_ context.Context,
	_ int,
	_ snapshot.SnapshotRef,
) (publisher.RepositoryChange, error) {
	inspector.event("inspect-candidate")
	return inspector.change, nil
}

type initialPRFinalSnapshotInspector struct {
	result InitialPRFinalSnapshots
	event  func(string)
}

func (inspector *initialPRFinalSnapshotInspector) InspectInitialPRFinalSnapshots(
	_ context.Context,
	_ int,
	_ InitialPRFinalSnapshotRefs,
) (InitialPRFinalSnapshots, error) {
	inspector.event("inspect-final")
	return inspector.result, nil
}

type initialPREvidenceVerifier struct {
	result publisher.PublicationEvidence
	event  func(string)
}

func (verifier *initialPREvidenceVerifier) Verify(
	_ context.Context,
	_ publisher.EvidenceRequest,
) (publisher.PublicationEvidence, error) {
	verifier.event("verify-evidence")
	return verifier.result.Clone(), nil
}

type initialPRImpactVerifier struct {
	request InitialPRImpactVerificationRequest
	result  contracts.PublishImpactBody
	event   func(string)
}

func (verifier *initialPRImpactVerifier) VerifyInitialPRImpact(
	_ context.Context,
	request InitialPRImpactVerificationRequest,
) (contracts.PublishImpactBody, error) {
	verifier.event("verify-impact")
	verifier.request = request
	return verifier.result, nil
}

type initialPRPublicationService struct {
	publisher.PRService
	event           func(string)
	branchStatus    publisher.Status
	ambiguousCreate bool
	branchRequests  []publisher.BranchPublicationRequest
	createRequests  []publisher.PullRequestPublicationRequest
}

func (service *initialPRPublicationService) PublishBranch(
	_ context.Context,
	request publisher.BranchPublicationRequest,
) (publisher.Publication, error) {
	service.event("publish-branch")
	service.branchRequests = append(service.branchRequests, request)
	status := service.branchStatus
	result := publisher.Result{
		Status: status, HeadSHA: request.NewSourceSHA,
		BaseSHA: request.ExpectedTargetSHA,
	}
	switch status {
	case publisher.StatusSucceeded:
		result.ExternalID = request.SourceRef
	case publisher.StatusRebaseRequired:
		result.Detail = "fresh observation required"
	}
	return initialPRPublication(
		1001,
		publisher.PRAction{
			Kind:   publisher.OperationPublishPRBranch,
			Branch: &request,
		},
		result,
	), nil
}

func (service *initialPRPublicationService) FindOrCreate(
	_ context.Context,
	request publisher.PullRequestPublicationRequest,
) (publisher.Publication, error) {
	service.event("create-pr")
	service.createRequests = append(service.createRequests, request)
	result := publisher.Result{
		Status: publisher.StatusSucceeded, ExternalID: "42",
		URL:     "https://github.example/acme/widget/pull/42",
		HeadSHA: request.SourceSHA, BaseSHA: request.TargetSHA,
	}
	if service.ambiguousCreate {
		result.ExternalID = "ambiguous"
	}
	return initialPRPublication(
		1002,
		publisher.PRAction{
			Kind:        publisher.OperationCreatePR,
			PullRequest: &request,
		},
		result,
	), nil
}

type initialPRProviderObserver struct {
	observation Observation
	cursor      Cursor
	calls       int
	event       func(string)
}

func (observer *initialPRProviderObserver) Observe(
	_ context.Context,
	_ Locator,
	cursor Cursor,
) (Observation, error) {
	observer.event("observe-created")
	observer.calls++
	observer.cursor = cursor
	return observer.observation.Clone(), nil
}

type initialPRObservationSealer struct {
	reference  snapshot.SnapshotRef
	request    InitialPRObservationSealRequest
	inspector  *initialPRObservationInspector
	err        error
	substitute bool
	calls      int
	event      func(string)
}

func (sealer *initialPRObservationSealer) SealInitialPRObservation(
	_ context.Context,
	request InitialPRObservationSealRequest,
) (snapshot.SnapshotRef, error) {
	sealer.event("seal-created")
	sealer.calls++
	sealer.request = request
	if sealer.err != nil {
		return snapshot.SnapshotRef{}, sealer.err
	}
	body := request.Body
	if sealer.substitute {
		body.TargetSHA = initialPRSHA('d')
	}
	sealer.inspector.bodies[sealer.reference.ID] = body
	return sealer.reference, nil
}

type initialPRBindingStore struct {
	BindingStore
	requests  []CreateBinding
	binding   *Binding
	mutateNew func(*Binding)
	event     func(string)
}

func (store *initialPRBindingStore) GetByExternal(
	_ context.Context,
	_ int,
	_ Locator,
) (Binding, bool, error) {
	store.event("get-binding")
	if store.binding == nil {
		return Binding{}, false, nil
	}
	return *store.binding, true, nil
}

func (store *initialPRBindingStore) Create(
	_ context.Context,
	request CreateBinding,
) (Binding, bool, error) {
	store.event("create-binding")
	store.requests = append(store.requests, request)
	if store.binding != nil {
		return *store.binding, false, nil
	}
	originRun := request.OriginatingWorkflowRunID
	originOccurrence := request.OriginatingPublicationOccurrence
	creationOccurrence := request.CreationPublicationOccurrenceID
	observation := request.LastObservationSnapshotID
	now := time.Date(2026, time.July, 30, 8, 0, 0, 0, time.UTC)
	binding := Binding{
		ID: 5001, TeamID: request.TeamID,
		Locator: request.Locator, URL: request.URL,
		SourceRef: request.SourceRef, TargetRef: request.TargetRef,
		Destination:                             request.Destination,
		ApprovalPolicyVersion:                   request.ApprovalPolicyVersion,
		OriginatingWorkflowRunID:                &originRun,
		OriginatingPublicationOccurrence:        &originOccurrence,
		CreationPublicationOccurrenceID:         &creationOccurrence,
		ApprovedBaselineRepositorySnapshotID:    602,
		ApprovedBaselineValidationSnapshotID:    603,
		ApprovedBaselinePublicationOccurrenceID: 901,
		MonitorWorkflowDefinitionID:             request.MonitorWorkflowDefinitionID,
		MonitorWorkflowVersion:                  request.MonitorWorkflowVersion,
		AcknowledgedCursor:                      request.AcknowledgedCursor,
		LastObservationSnapshotID:               &observation,
		LastReconciledSourceSHA:                 request.LastReconciledSourceSHA,
		LastReconciledTargetSHA:                 request.LastReconciledTargetSHA,
		LastReconciledAt:                        request.LastReconciledAt,
		State:                                   BindingActive,
		Revision:                                1,
		CreatedAt:                               now,
		UpdatedAt:                               now,
	}
	if store.mutateNew != nil {
		store.mutateNew(&binding)
	}
	store.binding = &binding
	return binding, true, nil
}

func initialPRPublication(
	id snapshot.DatabaseID,
	action publisher.PRAction,
	result publisher.Result,
) publisher.Publication {
	key, err := action.OperationKey()
	if err != nil {
		panic(err)
	}
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	return publisher.Publication{
		ID: id, OperationKey: key, OperationKind: action.Kind,
		PRAction: &action, Status: result.Status,
		Attempt: 1, Result: result,
		CreatedAt: now, UpdatedAt: now,
	}
}

func initialPRMissingBody(targetSHA string) contracts.PullRequestBody {
	return contracts.PullRequestBody{
		Provider: string(ProviderGitHub), Repository: "acme/widget",
		State:        contracts.PullRequestMissing,
		Mergeability: contracts.PullRequestUnknown,
		SourceRef:    "refs/heads/agent/change-1",
		ExpectedSource: &contracts.PullRequestHeadExpectation{
			Exists: false,
		},
		TargetRef: "refs/heads/main", TargetSHA: targetSHA,
		Iteration: "missing-1",
		Trigger:   contracts.PullRequestInitialPublishTrigger,
	}
}

func initialPRCreatedObservation() Observation {
	return Observation{
		Locator: Locator{
			Provider: ProviderGitHub, Repository: "acme/widget",
			ExternalID: "42",
		},
		Cursor:       "opaque-created-cursor",
		URL:          "https://github.example/acme/widget/pull/42",
		State:        contracts.PullRequestActive,
		Mergeability: contracts.PullRequestMergeable,
		SourceRef:    "refs/heads/agent/change-1",
		SourceSHA:    initialPRSHA('c'),
		TargetRef:    "refs/heads/main",
		TargetSHA:    initialPRSHA('b'),
		Iteration:    "iteration-1",
	}
}

func initialPRSnapshotRef(
	id snapshot.SnapshotID,
	refType snapshot.TypeRef,
	digestByte byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: refType,
		Digest: snapshot.Digest(
			"sha256:" + strings.Repeat(string(digestByte), 64),
		),
	}
}

func initialPRSHA(value byte) string {
	return strings.Repeat(string(value), 40)
}
