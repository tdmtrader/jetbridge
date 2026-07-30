package pullrequest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

func TestPRRevisionExecutorPublishesExactReviewRevisionInOrder(t *testing.T) {
	fixture := newPRRevisionExecutorFixture(t, contracts.PullRequestReviewBatchTrigger)
	executor, err := NewPRRevisionExecutor(
		fixture.bindings,
		fixture.acceptedReview,
		fixture.approvedBaseline,
		fixture.observations,
		fixture.snapshots,
		fixture.candidates,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		"https://ci.example/concourse",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.ExecutePRRevision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatalf("ExecutePRRevision() error = %v", err)
	}

	if got, want := fixture.publications.calls,
		[]publisher.OperationKind{
			publisher.OperationPublishPRBranch,
			publisher.OperationPublishPRStatus,
			publisher.OperationRespondToReview,
		}; !reflect.DeepEqual(got, want) {
		t.Fatalf("publication order = %v, want %v", got, want)
	}
	if fixture.candidates.teamID != fixture.request.Authority.TeamID ||
		fixture.candidates.reference != fixture.request.Candidate {
		t.Fatalf(
			"candidate authority = team %d ref %+v",
			fixture.candidates.teamID,
			fixture.candidates.reference,
		)
	}
	if fixture.snapshots.teamID != fixture.request.Authority.TeamID ||
		fixture.snapshots.references != (PRRevisionSnapshotRefs{
			Validation: fixture.request.Validation,
			Impact:     fixture.request.Impact,
			Response:   fixture.request.Response,
		}) {
		t.Fatalf(
			"snapshot authority = team %d refs %+v",
			fixture.snapshots.teamID,
			fixture.snapshots.references,
		)
	}
	if len(fixture.evidence.requests) != 1 ||
		fixture.evidence.requests[0].TeamID != fixture.request.Authority.TeamID ||
		fixture.evidence.requests[0].AcceptedReview == nil ||
		*fixture.evidence.requests[0].AcceptedReview !=
			*fixture.request.AcceptedReview {
		t.Fatalf(
			"evidence request = %#v",
			fixture.evidence.requests,
		)
	}
	if fixture.acceptedReview.calls != 1 ||
		fixture.acceptedReview.teamID !=
			fixture.request.Authority.TeamID ||
		fixture.acceptedReview.occurrenceID !=
			*fixture.binding.OriginatingPublicationOccurrence {
		t.Fatalf(
			"original accepted-review resolution = calls %d team %d occurrence %d",
			fixture.acceptedReview.calls,
			fixture.acceptedReview.teamID,
			fixture.acceptedReview.occurrenceID,
		)
	}
	if fixture.approvedBaseline.calls != 1 ||
		fixture.approvedBaseline.lookup !=
			(ApprovedBaselineAuthorityLookup{
				TeamID:    fixture.request.Authority.TeamID,
				BindingID: fixture.binding.ID,
				PublicationOccurrenceID: fixture.binding.
					ApprovedBaselinePublicationOccurrenceID,
				RepositorySnapshotID: fixture.binding.
					ApprovedBaselineRepositorySnapshotID,
				ValidationSnapshotID: fixture.binding.
					ApprovedBaselineValidationSnapshotID,
			}) {
		t.Fatalf(
			"approved baseline lookup = calls %d lookup %#v",
			fixture.approvedBaseline.calls,
			fixture.approvedBaseline.lookup,
		)
	}
	if len(fixture.impact.requests) != 1 {
		t.Fatalf(
			"impact verification requests = %#v",
			fixture.impact.requests,
		)
	}
	impactRequest := fixture.impact.requests[0]
	protectedBaseline, err :=
		fixture.approvedBaseline.authority.Protected()
	if err != nil {
		t.Fatal(err)
	}
	protectedOriginal, err :=
		fixture.acceptedReview.authority.Protected()
	if err != nil {
		t.Fatal(err)
	}
	if protectedBaseline.Kind != publisher.EvidenceAcceptedReview ||
		protectedBaseline.Repository != protectedOriginal.Candidate ||
		protectedBaseline.Validation != protectedOriginal.Validation {
		t.Fatalf(
			"initial approved baseline = %#v, original review = %#v",
			protectedBaseline,
			protectedOriginal,
		)
	}
	if impactRequest.TeamID != fixture.request.Authority.TeamID ||
		impactRequest.BindingID != fixture.request.BindingID ||
		impactRequest.ActionDigest !=
			snapshot.Digest(fixture.request.ActionDigest) ||
		impactRequest.PolicyVersion !=
			fixture.binding.ApprovalPolicyVersion ||
		impactRequest.Observation != fixture.request.Observation ||
		impactRequest.Baseline != protectedBaseline.Repository ||
		impactRequest.BaselineValidation != protectedBaseline.Validation ||
		impactRequest.Candidate != fixture.request.Candidate ||
		impactRequest.Validation != fixture.request.Validation ||
		impactRequest.Impact != fixture.request.Impact ||
		impactRequest.Response != fixture.request.Response ||
		!reflect.DeepEqual(
			impactRequest.AcceptedReview, fixture.request.Evidence,
		) ||
		!reflect.DeepEqual(
			impactRequest.Body, fixture.snapshots.result.Impact,
		) {
		t.Fatalf("impact verification request = %#v", impactRequest)
	}

	branch := fixture.publications.branch
	if branch.Authority != fixture.request.Authority ||
		branch.Observation != fixture.request.Observation ||
		branch.Candidate != fixture.request.Candidate ||
		branch.Validation != fixture.request.Validation ||
		branch.Impact != fixture.request.Impact ||
		!reflect.DeepEqual(branch.Evidence, fixture.request.Evidence) ||
		branch.Destination != fixture.binding.Destination ||
		branch.ApprovalPolicyVersion !=
			fixture.binding.ApprovalPolicyVersion ||
		branch.Locator != (publisher.PRLocator{
			Provider:   publisher.PRProviderGitHub,
			Repository: fixture.binding.Locator.Repository,
			ExternalID: fixture.binding.Locator.ExternalID,
		}) ||
		branch.SourceRef != fixture.binding.SourceRef ||
		branch.TargetRef != fixture.binding.TargetRef ||
		!branch.ExpectedSource.Exists ||
		branch.ExpectedSource.SHA != fixture.request.SourceHead ||
		branch.ExpectedTargetSHA != fixture.request.TargetHead ||
		branch.NewSourceSHA != fixture.candidates.change.ResultSHA {
		t.Fatalf("branch publication = %#v", branch)
	}

	status := fixture.publications.status
	if status.Authority != branch.Authority ||
		status.Observation != branch.Observation ||
		status.Validation != branch.Validation ||
		!reflect.DeepEqual(status.Evidence, branch.Evidence) ||
		status.Destination != branch.Destination ||
		status.ApprovalPolicyVersion != branch.ApprovalPolicyVersion ||
		status.Locator != branch.Locator ||
		status.TargetRef != branch.TargetRef ||
		status.SourceSHA != branch.NewSourceSHA ||
		status.State != "success" ||
		status.Description != "Validated" ||
		status.TargetURL != "https://ci.example/concourse/builds/42" {
		t.Fatalf("status publication = %#v", status)
	}

	response := fixture.publications.response
	if response.Authority != branch.Authority ||
		response.Observation != branch.Observation ||
		response.ResponseSnapshot != fixture.request.Response ||
		!reflect.DeepEqual(response.Evidence, branch.Evidence) ||
		response.Destination != branch.Destination ||
		response.ApprovalPolicyVersion != branch.ApprovalPolicyVersion ||
		response.Locator != branch.Locator ||
		response.TargetRef != branch.TargetRef ||
		!reflect.DeepEqual(
			response.Batch,
			fixture.observations.body.ReviewBatches[0],
		) ||
		!reflect.DeepEqual(response.Response, fixture.snapshots.result.Response) {
		t.Fatalf("response publication = %#v", response)
	}
}

func TestPRRevisionExecutorAllowsCompletedReviewOfEarlierSourceHead(
	t *testing.T,
) {
	fixture := newPRRevisionExecutorFixture(
		t, contracts.PullRequestReviewBatchTrigger,
	)
	reviewedHead := revisionObjectID('9')
	fixture.observations.body.ReviewBatches[0].CommitSHA = reviewedHead
	executor, err := NewPRRevisionExecutor(
		fixture.bindings,
		fixture.acceptedReview,
		fixture.approvedBaseline,
		fixture.observations,
		fixture.snapshots,
		fixture.candidates,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		"https://ci.example",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.ExecutePRRevision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatalf("ExecutePRRevision() error = %v", err)
	}
	if fixture.publications.branch.ExpectedSource.SHA !=
		fixture.request.SourceHead {
		t.Fatalf(
			"expected source SHA = %q, want %q",
			fixture.publications.branch.ExpectedSource.SHA,
			fixture.request.SourceHead,
		)
	}
	if fixture.publications.response.Batch.CommitSHA != reviewedHead {
		t.Fatalf(
			"reviewed head = %q, want %q",
			fixture.publications.response.Batch.CommitSHA,
			reviewedHead,
		)
	}
}

func TestPRRevisionExecutorUsesLaterHumanWaitApprovedBaselineAndOriginalReview(
	t *testing.T,
) {
	fixture := newPRRevisionExecutorFixture(
		t, contracts.PullRequestReviewBatchTrigger,
	)
	currentBaseline := configureLaterApprovedBaseline(t, fixture)
	executor, err := NewPRRevisionExecutor(
		fixture.bindings,
		fixture.acceptedReview,
		fixture.approvedBaseline,
		fixture.observations,
		fixture.snapshots,
		fixture.candidates,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		"https://ci.example",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.ExecutePRRevision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatalf("ExecutePRRevision() error = %v", err)
	}
	if fixture.acceptedReview.occurrenceID !=
		*fixture.binding.OriginatingPublicationOccurrence {
		t.Fatalf(
			"original review occurrence = %d, want %d",
			fixture.acceptedReview.occurrenceID,
			*fixture.binding.OriginatingPublicationOccurrence,
		)
	}
	if len(fixture.evidence.requests) != 1 ||
		fixture.evidence.requests[0].AcceptedReview == nil ||
		*fixture.evidence.requests[0].AcceptedReview !=
			*fixture.request.AcceptedReview {
		t.Fatalf(
			"original accepted-review evidence requests = %#v",
			fixture.evidence.requests,
		)
	}
	if fixture.approvedBaseline.lookup !=
		(ApprovedBaselineAuthorityLookup{
			TeamID:    fixture.binding.TeamID,
			BindingID: fixture.binding.ID,
			PublicationOccurrenceID: fixture.binding.
				ApprovedBaselinePublicationOccurrenceID,
			RepositorySnapshotID: fixture.binding.
				ApprovedBaselineRepositorySnapshotID,
			ValidationSnapshotID: fixture.binding.
				ApprovedBaselineValidationSnapshotID,
		}) {
		t.Fatalf(
			"approved baseline lookup = %#v",
			fixture.approvedBaseline.lookup,
		)
	}
	if len(fixture.impact.requests) != 1 ||
		fixture.impact.requests[0].Baseline != currentBaseline ||
		fixture.impact.requests[0].BaselineValidation.ID !=
			fixture.binding.ApprovedBaselineValidationSnapshotID ||
		fixture.impact.requests[0].AcceptedReview.AcceptedReview == nil ||
		fixture.impact.requests[0].AcceptedReview.AcceptedReview.Candidate ==
			currentBaseline {
		t.Fatalf(
			"impact request did not separate original review and current baseline: %#v",
			fixture.impact.requests,
		)
	}
	if len(fixture.publications.calls) != 3 {
		t.Fatalf(
			"provider mutations = %v",
			fixture.publications.calls,
		)
	}
}

func TestPRRevisionExecutorRejectsAlteredApprovedBaselineAuthorityBeforeMutation(
	t *testing.T,
) {
	tests := map[string]func(*testing.T, *prRevisionExecutorFixture){
		"repository snapshot": func(
			_ *testing.T,
			fixture *prRevisionExecutorFixture,
		) {
			fixture.binding.ApprovedBaselineRepositorySnapshotID++
			fixture.bindings.binding = fixture.binding
		},
		"validation snapshot": func(
			_ *testing.T,
			fixture *prRevisionExecutorFixture,
		) {
			fixture.binding.ApprovedBaselineValidationSnapshotID++
			fixture.bindings.binding = fixture.binding
		},
		"publication occurrence": func(
			t *testing.T,
			fixture *prRevisionExecutorFixture,
		) {
			protected, err := fixture.approvedBaseline.authority.Protected()
			if err != nil {
				t.Fatal(err)
			}
			fixture.approvedBaseline.authority =
				mustPRRevisionApprovedBaselineAuthority(
					t,
					protected.TeamID,
					protected.BindingID,
					protected.PublicationOccurrenceID+1,
					protected.Kind,
					protected.Repository,
					protected.Validation,
				)
		},
		"authorization kind": func(
			t *testing.T,
			fixture *prRevisionExecutorFixture,
		) {
			protected, err := fixture.approvedBaseline.authority.Protected()
			if err != nil {
				t.Fatal(err)
			}
			fixture.approvedBaseline.authority =
				mustPRRevisionApprovedBaselineAuthority(
					t,
					protected.TeamID,
					protected.BindingID,
					protected.PublicationOccurrenceID,
					publisher.EvidenceAcceptedReview,
					protected.Repository,
					protected.Validation,
				)
		},
		"cross-binding authority": func(
			t *testing.T,
			fixture *prRevisionExecutorFixture,
		) {
			protected, err := fixture.approvedBaseline.authority.Protected()
			if err != nil {
				t.Fatal(err)
			}
			fixture.approvedBaseline.authority =
				mustPRRevisionApprovedBaselineAuthority(
					t,
					protected.TeamID,
					protected.BindingID+1,
					protected.PublicationOccurrenceID,
					protected.Kind,
					protected.Repository,
					protected.Validation,
				)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPRRevisionExecutorFixture(
				t, contracts.PullRequestReviewBatchTrigger,
			)
			configureLaterApprovedBaseline(t, fixture)
			mutate(t, fixture)
			executor, err := NewPRRevisionExecutor(
				fixture.bindings,
				fixture.acceptedReview,
				fixture.approvedBaseline,
				fixture.observations,
				fixture.snapshots,
				fixture.candidates,
				fixture.evidence,
				fixture.impact,
				fixture.publications,
				"https://ci.example",
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := executor.ExecutePRRevision(
				context.Background(), fixture.request,
			); err == nil {
				t.Fatal("ExecutePRRevision() error = nil")
			}
			if len(fixture.publications.calls) != 0 {
				t.Fatalf(
					"provider mutations = %v, want none",
					fixture.publications.calls,
				)
			}
		})
	}
}

func TestPRRevisionExecutorPublishesBranchProofForNoopWithoutSemanticResponse(
	t *testing.T,
) {
	for _, trigger := range []contracts.PullRequestTrigger{
		contracts.PullRequestConflictTrigger,
		contracts.PullRequestFreshnessTrigger,
	} {
		t.Run(string(trigger), func(t *testing.T) {
			fixture := newPRRevisionExecutorFixture(t, trigger)
			fixture.candidates.change.ResultSHA = fixture.request.SourceHead
			fixture.snapshots.result.Response =
				contracts.PullRequestResponseBody{
					Kind: contracts.PullRequestResponseNoResponse,
				}
			executor, err := NewPRRevisionExecutor(
				fixture.bindings,
				fixture.acceptedReview,
				fixture.approvedBaseline,
				fixture.observations,
				fixture.snapshots,
				fixture.candidates,
				fixture.evidence,
				fixture.impact,
				fixture.publications,
				"https://ci.example",
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := executor.ExecutePRRevision(
				context.Background(), fixture.request,
			); err != nil {
				t.Fatalf("ExecutePRRevision() error = %v", err)
			}
			if got, want := fixture.publications.calls,
				[]publisher.OperationKind{
					publisher.OperationPublishPRBranch,
					publisher.OperationPublishPRStatus,
				}; !reflect.DeepEqual(got, want) {
				t.Fatalf("publication order = %v, want %v", got, want)
			}
			if fixture.publications.branch.NewSourceSHA !=
				fixture.publications.branch.ExpectedSource.SHA {
				t.Fatalf(
					"no-op branch = %#v",
					fixture.publications.branch,
				)
			}
		})
	}
}

func TestPRRevisionExecutorTreatsExactRebaseRequiredBranchAsSafeStale(
	t *testing.T,
) {
	fixture := newPRRevisionExecutorFixture(
		t, contracts.PullRequestConflictTrigger,
	)
	fixture.snapshots.result.Response = contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseNoResponse,
	}
	fixture.publications.branchStatus = publisher.StatusRebaseRequired
	executor, err := NewPRRevisionExecutor(
		fixture.bindings,
		fixture.acceptedReview,
		fixture.approvedBaseline,
		fixture.observations,
		fixture.snapshots,
		fixture.candidates,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		"https://ci.example",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.ExecutePRRevision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatalf("safe stale ExecutePRRevision() error = %v", err)
	}
	if got, want := fixture.publications.calls,
		[]publisher.OperationKind{publisher.OperationPublishPRBranch}; !reflect.DeepEqual(got, want) {
		t.Fatalf("publication order = %v, want %v", got, want)
	}
}

func TestPRRevisionExecutorRejectsTerminalOrChangedAuthorityBeforeMutation(
	t *testing.T,
) {
	tests := map[string]func(*prRevisionExecutorFixture){
		"completed observation": func(fixture *prRevisionExecutorFixture) {
			fixture.observations.body.State =
				contracts.PullRequestCompleted
			fixture.observations.body.Trigger =
				contracts.PullRequestCompletedTrigger
		},
		"abandoned observation": func(fixture *prRevisionExecutorFixture) {
			fixture.observations.body.State =
				contracts.PullRequestAbandoned
			fixture.observations.body.Trigger =
				contracts.PullRequestAbandonedTrigger
		},
		"detached workflow run": func(fixture *prRevisionExecutorFixture) {
			runID := snapshot.WorkflowRunID(92)
			fixture.binding.Active.WorkflowRunID = &runID
			fixture.bindings.binding = fixture.binding
		},
		"changed destination": func(fixture *prRevisionExecutorFixture) {
			fixture.binding.Destination = "github.example/acme/other"
			fixture.bindings.binding = fixture.binding
		},
		"changed source head": func(fixture *prRevisionExecutorFixture) {
			fixture.binding.Active.SourceSHA = revisionObjectID('f')
			fixture.bindings.binding = fixture.binding
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPRRevisionExecutorFixture(
				t, contracts.PullRequestReviewBatchTrigger,
			)
			mutate(fixture)
			executor, err := NewPRRevisionExecutor(
				fixture.bindings,
				fixture.acceptedReview,
				fixture.approvedBaseline,
				fixture.observations,
				fixture.snapshots,
				fixture.candidates,
				fixture.evidence,
				fixture.impact,
				fixture.publications,
				"https://ci.example",
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := executor.ExecutePRRevision(
				context.Background(), fixture.request,
			); err == nil {
				t.Fatal("ExecutePRRevision() error = nil")
			}
			if len(fixture.publications.calls) != 0 {
				t.Fatalf(
					"provider mutations = %v, want none",
					fixture.publications.calls,
				)
			}
			if fixture.observations.body.State !=
				contracts.PullRequestActive &&
				fixture.candidates.calls != 0 {
				t.Fatalf(
					"terminal observation reached candidate inspection %d times",
					fixture.candidates.calls,
				)
			}
		})
	}
}

func TestPRRevisionExecutorRejectsMismatchedExactInputsBeforeMutation(
	t *testing.T,
) {
	tests := map[string]func(*prRevisionExecutorFixture){
		"candidate base": func(fixture *prRevisionExecutorFixture) {
			fixture.candidates.change.BaseSHA = revisionObjectID('f')
		},
		"validation candidate": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.ValidationCandidate.Digest =
				revisionDigest('f')
		},
		"validation conclusion": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.ValidationConclusion = "failed"
		},
		"impact candidate": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.Impact.CandidateDigest =
				revisionDigest('f').String()
		},
		"impact baseline": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.Impact.BaselineDigest =
				revisionDigest('f').String()
		},
		"response subject": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.ResponseObservation.Digest =
				revisionDigest('f')
		},
		"semantic no-response": func(fixture *prRevisionExecutorFixture) {
			fixture.snapshots.result.Response =
				contracts.PullRequestResponseBody{
					Kind: contracts.PullRequestResponseNoResponse,
				}
		},
		"unverified evidence": func(fixture *prRevisionExecutorFixture) {
			fixture.evidence.accepted.AcceptedReview.OutcomeRevision++
		},
		"stale no-wait baseline": func(
			fixture *prRevisionExecutorFixture,
		) {
			stale := revisionSnapshotRef(
				404, "repository/v1", '9',
			)
			fixture.request.AcceptedReview.Candidate = stale
			fixture.request.Evidence.AcceptedReview.Candidate = stale
		},
		"impact verifier mismatch": func(
			fixture *prRevisionExecutorFixture,
		) {
			fixture.impact.echoRequest = false
			fixture.impact.result =
				clonePRRevisionImpact(fixture.snapshots.result.Impact)
			fixture.impact.result.RuleResults[0].Reason =
				"A different impact was returned."
		},
		"missing accepted baseline": func(
			fixture *prRevisionExecutorFixture,
		) {
			fixture.acceptedReview.found = false
		},
		"binding baseline candidate": func(
			fixture *prRevisionExecutorFixture,
		) {
			fixture.binding.
				ApprovedBaselineRepositorySnapshotID++
			fixture.bindings.binding = fixture.binding
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPRRevisionExecutorFixture(
				t, contracts.PullRequestReviewBatchTrigger,
			)
			mutate(fixture)
			executor, err := NewPRRevisionExecutor(
				fixture.bindings,
				fixture.acceptedReview,
				fixture.approvedBaseline,
				fixture.observations,
				fixture.snapshots,
				fixture.candidates,
				fixture.evidence,
				fixture.impact,
				fixture.publications,
				"https://ci.example",
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := executor.ExecutePRRevision(
				context.Background(), fixture.request,
			); err == nil {
				t.Fatal("ExecutePRRevision() error = nil")
			}
			if len(fixture.publications.calls) != 0 {
				t.Fatalf(
					"provider mutations = %v, want none",
					fixture.publications.calls,
				)
			}
		})
	}
}

func TestPRRevisionExecutorReverifiesExactHumanWait(t *testing.T) {
	fixture := newPRRevisionExecutorFixture(
		t, contracts.PullRequestReviewBatchTrigger,
	)
	approval := publisher.ApprovalEvidence{
		WaitID:     71,
		Question:   revisionSnapshotRef(72, "question/v1", '7'),
		Answer:     revisionSnapshotRef(73, "human-answer/v1", '8'),
		ResolvedBy: "release-manager",
		ResolvedAt: time.Date(
			2026, time.July, 30, 6, 0, 0, 0, time.UTC,
		),
	}
	contextEnvelope, err := publisher.BuildPRApprovalContext(
		publisher.PRApprovalRequest{
			TeamID:                fixture.request.Authority.TeamID,
			WorkflowRunID:         fixture.request.Authority.WorkflowRunID,
			BuildID:               fixture.request.Authority.BuildID,
			Approval:              approval.Answer,
			BindingID:             fixture.request.BindingID,
			ActionDigest:          fixture.request.ActionDigest,
			Observation:           fixture.request.Observation,
			Candidate:             fixture.request.Candidate,
			SourceHead:            fixture.request.SourceHead,
			TargetHead:            fixture.request.TargetHead,
			Destination:           fixture.request.Destination,
			Response:              fixture.request.Response,
			Validation:            fixture.request.Validation,
			Impact:                fixture.request.Impact,
			ApprovalPolicyVersion: fixture.request.ApprovalPolicyVersion,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.AcceptedReview = nil
	fixture.request.ApprovalContext = &contextEnvelope
	fixture.request.Evidence = publisher.PublicationEvidence{
		Kind:      publisher.EvidenceHumanWait,
		HumanWait: &approval,
	}
	fixture.snapshots.result.Impact.ReapprovalRequired = true
	fixture.snapshots.result.Impact.Reasons = []string{
		"Human reapproval is required.",
	}
	fixture.evidence.human = fixture.request.Evidence.Clone()
	executor, err := NewPRRevisionExecutor(
		fixture.bindings,
		fixture.acceptedReview,
		fixture.approvedBaseline,
		fixture.observations,
		fixture.snapshots,
		fixture.candidates,
		fixture.evidence,
		fixture.impact,
		fixture.publications,
		"https://ci.example",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.ExecutePRRevision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatalf("ExecutePRRevision() error = %v", err)
	}
	if len(fixture.evidence.requests) != 2 ||
		fixture.evidence.requests[0].AcceptedReview == nil ||
		fixture.evidence.requests[1].HumanWait == nil ||
		fixture.evidence.requests[1].HumanWait.Approval !=
			approval.Answer ||
		len(fixture.evidence.requests[1].HumanWait.ExpectedContext) == 0 {
		t.Fatalf(
			"human-wait evidence request = %#v",
			fixture.evidence.requests,
		)
	}
}

func TestSnapshotPRRevisionInspectorReopensExactTeamAuthorizedRecords(
	t *testing.T,
) {
	candidate := revisionSnapshotRef(
		502, "repository-change/v1", '2',
	)
	observation := revisionSnapshotRef(
		501, "pull-request/v1", '1',
	)
	base := revisionSnapshotRef(500, "repository/v1", '0')
	logContent := []byte("validation passed\n")
	logHash := sha256.Sum256(logContent)
	logDigest := snapshot.Digest(
		"sha256:" + hex.EncodeToString(logHash[:]),
	)
	imageDigest := revisionDigest('a')
	validationBody := contracts.ValidationBody{
		Conclusion: "passed",
		Summary:    "All required checks passed.",
		Attestation: contracts.ValidationAttestation{
			CandidateDigest: candidate.Digest,
			BaseInputs: []contracts.ValidationBaseInput{{
				Input: "target-repository",
				Type:  base.Type, Digest: base.Digest,
			}},
			ProfileDigest:         revisionDigest('b'),
			ProtectedConfigDigest: revisionDigest('c'),
			CapabilityImage: "registry.example/validator@" +
				imageDigest.String(),
			CapabilityImageDigest: imageDigest,
			WorkflowDefinitionID:  31,
			WorkflowVersion:       3,
			Toolchain:             "go1.25.0",
		},
		Checks: []contracts.ValidationCheck{{
			ID: "unit", Kind: "test", Name: "Unit tests",
			Status: "passed",
			Attempts: []contracts.ValidationAttempt{{
				Number: 1, Status: "passed", Duration: "1s",
				Log: contracts.ValidationLog{
					Path:      "content/logs/unit.txt",
					Digest:    logDigest,
					Size:      int64(len(logContent)),
					MediaType: "text/plain",
				},
			}},
		}},
	}
	validationRecord, err := contracts.NewRecord(
		snapshot.TypeRef("validation/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput(
				"candidate", contracts.SubjectRolePrimary,
				"candidate", candidate,
			),
			contracts.SubjectFromInput(
				"target", contracts.SubjectRoleBase,
				"target-repository", base,
			),
		},
		validationBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	impactBody := contracts.PublishImpactBody{
		BaselineDigest:  base.Digest.String(),
		CandidateDigest: candidate.Digest.String(),
		RuleResults: []contracts.PublishImpactRule{{
			ID: "default", Passed: true,
			Reason: "No escalation rule matched.",
		}},
	}
	impactRecord, err := contracts.NewRecord(
		snapshot.TypeRef("publish-impact/v1"),
		nil,
		impactBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := contracts.PullRequestResponseBody{
		Kind:    contracts.PullRequestResponseReviewResponse,
		BatchID: "batch-2",
		Summary: "Addressed the completed review.",
	}
	responseRecord, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request-response/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"pull-request", contracts.SubjectRolePrimary,
			"pull-request", observation,
		)},
		responseBody,
	)
	if err != nil {
		t.Fatal(err)
	}

	validationManifest, validationArchive := revisionRecordSnapshot(
		t, 503, validationRecord,
		map[string][]byte{"content/logs/unit.txt": logContent},
	)
	impactManifest, impactArchive := revisionRecordSnapshot(
		t, 504, impactRecord, nil,
	)
	responseManifest, responseArchive := revisionRecordSnapshot(
		t, 505, responseRecord, nil,
	)
	manifests := map[snapshot.SnapshotID]snapshot.Snapshot{
		validationManifest.ID: validationManifest,
		impactManifest.ID:     impactManifest,
		responseManifest.ID:   responseManifest,
	}
	archives := map[snapshot.SnapshotID][]byte{
		validationManifest.ID: validationArchive,
		impactManifest.ID:     impactArchive,
		responseManifest.ID:   responseArchive,
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedStub = func(
		_ context.Context,
		teamID int,
		snapshotID snapshot.SnapshotID,
	) (snapshot.Snapshot, bool, error) {
		if teamID != 17 {
			return snapshot.Snapshot{}, false, nil
		}
		manifest, found := manifests[snapshotID]
		return manifest, found, nil
	}
	content := &snapshotfakes.FakeContentStore{}
	content.OpenStub = func(
		_ context.Context,
		manifest snapshot.Snapshot,
	) (io.ReadCloser, error) {
		archive, found := archives[manifest.ID]
		if !found {
			return nil, snapshot.ErrNotFound
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	inspector, err := NewSnapshotPRRevisionInspector(
		metadata, content, snapshot.Canonicalizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	references := PRRevisionSnapshotRefs{
		Validation: snapshot.SnapshotRef{
			ID: validationManifest.ID, Type: validationManifest.Type,
			Digest: validationManifest.Digest,
		},
		Impact: snapshot.SnapshotRef{
			ID: impactManifest.ID, Type: impactManifest.Type,
			Digest: impactManifest.Digest,
		},
		Response: snapshot.SnapshotRef{
			ID: responseManifest.ID, Type: responseManifest.Type,
			Digest: responseManifest.Digest,
		},
	}

	got, err := inspector.InspectPRRevisionSnapshots(
		context.Background(), 17, references,
	)
	if err != nil {
		t.Fatalf("InspectPRRevisionSnapshots() error = %v", err)
	}
	if got.ValidationCandidate !=
		(contracts.StableSnapshotRef{
			Type: candidate.Type, Digest: candidate.Digest,
		}) ||
		got.ValidationConclusion != "passed" ||
		!reflect.DeepEqual(got.Impact, impactBody) ||
		got.ResponseObservation !=
			(contracts.StableSnapshotRef{
				Type: observation.Type, Digest: observation.Digest,
			}) ||
		!reflect.DeepEqual(got.Response, responseBody) {
		t.Fatalf("revision snapshots = %#v", got)
	}
	if metadata.GetAuthorizedCallCount() != 3 {
		t.Fatalf(
			"authorized snapshot reads = %d, want 3",
			metadata.GetAuthorizedCallCount(),
		)
	}

	tampered := references
	tampered.Response.Digest = revisionDigest('f')
	if _, err := inspector.InspectPRRevisionSnapshots(
		context.Background(), 17, tampered,
	); err == nil {
		t.Fatal("substituted response digest was accepted")
	}
}

type prRevisionExecutorFixture struct {
	binding          Binding
	request          publisher.PRRevisionPublicationRequest
	bindings         *prRevisionBindingStore
	acceptedReview   *prRevisionAcceptedReviewResolver
	approvedBaseline *prRevisionApprovedBaselineResolver
	observations     *prRevisionObservationInspector
	snapshots        *prRevisionSnapshotInspector
	candidates       *prRevisionCandidateInspector
	evidence         *prRevisionEvidenceVerifier
	impact           *prRevisionImpactVerifier
	publications     *prRevisionPublicationService
}

func newPRRevisionExecutorFixture(
	t *testing.T,
	trigger contracts.PullRequestTrigger,
) *prRevisionExecutorFixture {
	t.Helper()
	sourceSHA := revisionObjectID('a')
	targetSHA := revisionObjectID('b')
	resultSHA := revisionObjectID('c')
	workflowRunID := snapshot.WorkflowRunID(91)
	observation := revisionSnapshotRef(501, "pull-request/v1", '1')
	candidate := revisionSnapshotRef(
		502, "repository-change/v1", '2',
	)
	validation := revisionSnapshotRef(503, "validation/v1", '3')
	impact := revisionSnapshotRef(504, "publish-impact/v1", '4')
	response := revisionSnapshotRef(
		505, "pull-request-response/v1", '5',
	)
	baseline := revisionSnapshotRef(401, "repository/v1", '6')
	review := revisionSnapshotRef(402, "review/v1", '7')
	acceptedValidation := revisionSnapshotRef(
		403, "validation/v1", '8',
	)
	acceptedEvidence := publisher.AcceptedReviewEvidence{
		Review: review, Candidate: baseline,
		Validation:          acceptedValidation,
		ReviewWorkflowRunID: 81,
		OutcomeRevision:     3,
		AcceptedBy:          "alice",
		AcceptedAt: time.Date(
			2026, time.July, 29, 12, 0, 0, 0, time.UTC,
		),
	}
	publicationEvidence := publisher.PublicationEvidence{
		Kind:           publisher.EvidenceAcceptedReview,
		AcceptedReview: &acceptedEvidence,
	}
	request := publisher.PRRevisionPublicationRequest{
		Authority: publisher.Authority{
			TeamID: 17, TeamName: "engineering", BuildID: 42,
			WorkflowRunID: workflowRunID, Actor: "alice",
		},
		BindingID:             19,
		ActionDigest:          "sha256:" + strings.Repeat("d", 64),
		Observation:           observation,
		Candidate:             candidate,
		Validation:            validation,
		Impact:                impact,
		Response:              response,
		SourceHead:            sourceSHA,
		TargetHead:            targetSHA,
		Destination:           "github.example/acme/widget",
		ApprovalPolicyVersion: "engineering/v3",
		AcceptedReview: &publisher.AcceptedReviewEvidenceRequest{
			Review: review, Candidate: baseline,
			Validation:          acceptedValidation,
			ReviewWorkflowRunID: 81,
			OutcomeRevision:     3,
		},
		Evidence: publicationEvidence,
	}
	originOccurrenceID := int64(704)
	binding := Binding{
		ID: request.BindingID, TeamID: request.Authority.TeamID,
		Locator: Locator{
			Provider: ProviderGitHub, Repository: "acme/widget",
			ExternalID: "42",
		},
		URL:                                     "https://github.example/acme/widget/pull/42",
		SourceRef:                               "refs/heads/agent/upgrade",
		TargetRef:                               "refs/heads/main",
		Destination:                             request.Destination,
		ApprovalPolicyVersion:                   request.ApprovalPolicyVersion,
		OriginatingPublicationOccurrence:        &originOccurrenceID,
		ApprovedBaselineRepositorySnapshotID:    baseline.ID,
		ApprovedBaselineValidationSnapshotID:    acceptedValidation.ID,
		ApprovedBaselinePublicationOccurrenceID: 704,
		State:                                   BindingActive,
		Revision:                                9,
		Active: &LaunchReservation{
			BindingID:             request.BindingID,
			BindingRevision:       8,
			BaseRevision:          7,
			ActionDigest:          request.ActionDigest,
			ObservationSnapshotID: observation.ID,
			Cursor:                "cursor-2",
			SourceSHA:             sourceSHA,
			TargetSHA:             targetSHA,
			Token:                 "reservation-1",
			WorkflowRunID:         &workflowRunID,
		},
	}
	body := contracts.PullRequestBody{
		Provider:     string(binding.Locator.Provider),
		Repository:   binding.Locator.Repository,
		ExternalID:   binding.Locator.ExternalID,
		URL:          binding.URL,
		State:        contracts.PullRequestActive,
		Mergeability: contracts.PullRequestMergeable,
		SourceRef:    binding.SourceRef,
		SourceSHA:    sourceSHA,
		TargetRef:    binding.TargetRef,
		TargetSHA:    targetSHA,
		Iteration:    "iteration-2",
		Trigger:      trigger,
	}
	responseBody := contracts.PullRequestResponseBody{
		Kind:    contracts.PullRequestResponseReviewResponse,
		BatchID: "batch-2",
		Summary: "Addressed the completed review.",
	}
	if trigger == contracts.PullRequestReviewBatchTrigger {
		body.ReviewBatches = []contracts.PullRequestReviewBatch{{
			ID:        responseBody.BatchID,
			ReviewID:  "review-2",
			CommitSHA: sourceSHA,
			Reviewer:  "reviewer",
			Ready:     true,
		}}
	}
	if trigger == contracts.PullRequestConflictTrigger {
		body.Mergeability = contracts.PullRequestConflicted
	}
	snapshots := PRRevisionSnapshots{
		ValidationCandidate: contracts.StableSnapshotRef{
			Type: candidate.Type, Digest: candidate.Digest,
		},
		ValidationConclusion: "passed",
		Impact: contracts.PublishImpactBody{
			BaselineDigest:  baseline.Digest.String(),
			CandidateDigest: candidate.Digest.String(),
			RuleResults: []contracts.PublishImpactRule{{
				ID: "default", Passed: true,
				Reason: "No escalation rule matched.",
			}},
		},
		ResponseObservation: contracts.StableSnapshotRef{
			Type: observation.Type, Digest: observation.Digest,
		},
		Response: responseBody,
	}
	acceptedAuthority, err := NewAcceptedReviewAuthority(
		AcceptedReviewAuthoritySpec{
			TeamID:                  request.Authority.TeamID,
			PublicationOccurrenceID: originOccurrenceID,
			Review:                  review,
			Candidate:               baseline,
			Validation:              acceptedValidation,
			ReviewWorkflowRunID:     81,
			OutcomeRevision:         3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	baselineAuthority, err := NewApprovedBaselineAuthority(
		ApprovedBaselineAuthoritySpec{
			TeamID:    request.Authority.TeamID,
			BindingID: binding.ID,
			PublicationOccurrenceID: binding.
				ApprovedBaselinePublicationOccurrenceID,
			Kind:       publisher.EvidenceAcceptedReview,
			Repository: baseline,
			Validation: acceptedValidation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &prRevisionExecutorFixture{
		binding: binding, request: request,
		bindings: &prRevisionBindingStore{binding: binding},
		acceptedReview: &prRevisionAcceptedReviewResolver{
			authority: acceptedAuthority,
			found:     true,
		},
		approvedBaseline: &prRevisionApprovedBaselineResolver{
			authority: baselineAuthority,
			found:     true,
		},
		observations: &prRevisionObservationInspector{body: body},
		snapshots:    &prRevisionSnapshotInspector{result: snapshots},
		candidates: &prRevisionCandidateInspector{
			change: publisher.RepositoryChange{
				BaseSHA: targetSHA, ResultSHA: resultSHA,
				MaterializedRoot: "/verified/change",
			},
		},
		evidence: &prRevisionEvidenceVerifier{
			accepted: publicationEvidence.Clone(),
		},
		impact: &prRevisionImpactVerifier{echoRequest: true},
		publications: &prRevisionPublicationService{
			branchStatus: publisher.StatusSucceeded,
		},
	}
}

func configureLaterApprovedBaseline(
	t *testing.T,
	fixture *prRevisionExecutorFixture,
) snapshot.SnapshotRef {
	t.Helper()
	repository := revisionSnapshotRef(411, "repository/v1", '9')
	validation := revisionSnapshotRef(413, "validation/v1", 'a')
	fixture.binding.ApprovedBaselineRepositorySnapshotID = repository.ID
	fixture.binding.ApprovedBaselineValidationSnapshotID = validation.ID
	fixture.binding.ApprovedBaselinePublicationOccurrenceID = 705
	fixture.bindings.binding = fixture.binding
	fixture.approvedBaseline.authority =
		mustPRRevisionApprovedBaselineAuthority(
			t,
			fixture.binding.TeamID,
			fixture.binding.ID,
			fixture.binding.ApprovedBaselinePublicationOccurrenceID,
			publisher.EvidenceHumanWait,
			repository,
			validation,
		)
	fixture.snapshots.result.Impact.BaselineDigest =
		repository.Digest.String()
	return repository
}

func mustPRRevisionApprovedBaselineAuthority(
	t *testing.T,
	teamID int,
	bindingID int64,
	occurrenceID int64,
	kind publisher.EvidenceKind,
	repository snapshot.SnapshotRef,
	validation snapshot.SnapshotRef,
) ApprovedBaselineAuthority {
	t.Helper()
	authority, err := NewApprovedBaselineAuthority(
		ApprovedBaselineAuthoritySpec{
			TeamID:                  teamID,
			BindingID:               bindingID,
			PublicationOccurrenceID: occurrenceID,
			Kind:                    kind,
			Repository:              repository,
			Validation:              validation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

type prRevisionBindingStore struct {
	binding Binding
	err     error
}

func (store *prRevisionBindingStore) Create(
	context.Context,
	CreateBinding,
) (Binding, bool, error) {
	panic("unexpected Create")
}

func (store *prRevisionBindingStore) Get(
	_ context.Context,
	teamID int,
	bindingID int64,
) (Binding, bool, error) {
	if store.err != nil {
		return Binding{}, false, store.err
	}
	if store.binding.TeamID != teamID || store.binding.ID != bindingID {
		return Binding{}, false, nil
	}
	return store.binding, true, nil
}

func (store *prRevisionBindingStore) GetByExternal(
	context.Context,
	int,
	Locator,
) (Binding, bool, error) {
	panic("unexpected GetByExternal")
}

func (store *prRevisionBindingStore) ReserveLaunch(
	context.Context,
	ReserveLaunch,
) (LaunchReservation, bool, error) {
	panic("unexpected ReserveLaunch")
}

func (store *prRevisionBindingStore) AttachRun(
	context.Context,
	AttachRun,
) (Binding, error) {
	panic("unexpected AttachRun")
}

func (store *prRevisionBindingStore) ReleaseLaunch(
	context.Context,
	ReleaseLaunch,
) (Binding, error) {
	panic("unexpected ReleaseLaunch")
}

func (store *prRevisionBindingStore) AcknowledgeAction(
	context.Context,
	AcknowledgeAction,
) (Binding, error) {
	panic("unexpected AcknowledgeAction")
}

func (store *prRevisionBindingStore) MarkAttention(
	context.Context,
	int,
	int64,
	string,
) (Binding, error) {
	panic("unexpected MarkAttention")
}

func (store *prRevisionBindingStore) MarkTerminal(
	context.Context,
	TerminalBinding,
) (Binding, error) {
	panic("unexpected MarkTerminal")
}

func (store *prRevisionBindingStore) MarkDirectTerminal(
	context.Context,
	DirectTerminalBinding,
) (Binding, error) {
	panic("unexpected MarkDirectTerminal")
}

func (store *prRevisionBindingStore) RequestObservation(
	context.Context,
	OperatorRequest,
) (Binding, error) {
	panic("unexpected RequestObservation")
}

func (store *prRevisionBindingStore) Pause(
	context.Context,
	OperatorRequest,
) (Binding, error) {
	panic("unexpected Pause")
}

func (store *prRevisionBindingStore) Resume(
	context.Context,
	OperatorRequest,
) (Binding, error) {
	panic("unexpected Resume")
}

func (store *prRevisionBindingStore) Terminate(
	context.Context,
	OperatorRequest,
) (Binding, error) {
	panic("unexpected Terminate")
}

func (store *prRevisionBindingStore) ListAudit(
	context.Context,
	int,
	int64,
	AuditFilter,
) ([]AuditEntry, error) {
	panic("unexpected ListAudit")
}

func (store *prRevisionBindingStore) ListActive(
	context.Context,
	int,
) ([]Binding, error) {
	panic("unexpected ListActive")
}

type prRevisionObservationInspector struct {
	body  contracts.PullRequestBody
	err   error
	calls int
}

func (inspector *prRevisionObservationInspector) InspectMonitorObservation(
	context.Context,
	int,
	snapshot.SnapshotRef,
) (contracts.PullRequestBody, error) {
	inspector.calls++
	return inspector.body, inspector.err
}

type prRevisionSnapshotInspector struct {
	result     PRRevisionSnapshots
	err        error
	teamID     int
	references PRRevisionSnapshotRefs
}

func (inspector *prRevisionSnapshotInspector) InspectPRRevisionSnapshots(
	_ context.Context,
	teamID int,
	references PRRevisionSnapshotRefs,
) (PRRevisionSnapshots, error) {
	inspector.teamID = teamID
	inspector.references = references
	return inspector.result, inspector.err
}

type prRevisionCandidateInspector struct {
	change    publisher.RepositoryChange
	err       error
	calls     int
	teamID    int
	reference snapshot.SnapshotRef
}

func (inspector *prRevisionCandidateInspector) InspectExactPRCandidate(
	_ context.Context,
	teamID int,
	reference snapshot.SnapshotRef,
) (publisher.RepositoryChange, error) {
	inspector.calls++
	inspector.teamID = teamID
	inspector.reference = reference
	return inspector.change, inspector.err
}

type prRevisionAcceptedReviewResolver struct {
	authority    AcceptedReviewAuthority
	found        bool
	err          error
	calls        int
	teamID       int
	occurrenceID int64
}

func (resolver *prRevisionAcceptedReviewResolver) ResolveAcceptedReviewAuthority(
	_ context.Context,
	teamID int,
	occurrenceID int64,
) (AcceptedReviewAuthority, bool, error) {
	resolver.calls++
	resolver.teamID = teamID
	resolver.occurrenceID = occurrenceID
	return resolver.authority, resolver.found, resolver.err
}

type prRevisionApprovedBaselineResolver struct {
	authority ApprovedBaselineAuthority
	found     bool
	err       error
	calls     int
	lookup    ApprovedBaselineAuthorityLookup
}

func (resolver *prRevisionApprovedBaselineResolver) ResolveApprovedBaselineAuthority(
	_ context.Context,
	lookup ApprovedBaselineAuthorityLookup,
) (ApprovedBaselineAuthority, bool, error) {
	resolver.calls++
	resolver.lookup = lookup
	return resolver.authority, resolver.found, resolver.err
}

type prRevisionEvidenceVerifier struct {
	accepted publisher.PublicationEvidence
	human    publisher.PublicationEvidence
	err      error
	requests []publisher.EvidenceRequest
}

func (verifier *prRevisionEvidenceVerifier) Verify(
	_ context.Context,
	request publisher.EvidenceRequest,
) (publisher.PublicationEvidence, error) {
	verifier.requests = append(verifier.requests, request)
	if verifier.err != nil {
		return publisher.PublicationEvidence{}, verifier.err
	}
	if request.AcceptedReview != nil {
		return verifier.accepted.Clone(), nil
	}
	if request.HumanWait != nil {
		return verifier.human.Clone(), nil
	}
	return publisher.PublicationEvidence{}, nil
}

type prRevisionImpactVerifier struct {
	echoRequest bool
	result      contracts.PublishImpactBody
	err         error
	requests    []publisher.PRImpactVerificationRequest
}

func (verifier *prRevisionImpactVerifier) VerifyPRImpact(
	_ context.Context,
	request publisher.PRImpactVerificationRequest,
) (contracts.PublishImpactBody, error) {
	verifier.requests = append(verifier.requests, request)
	if verifier.err != nil {
		return contracts.PublishImpactBody{}, verifier.err
	}
	if verifier.echoRequest {
		return request.Body, nil
	}
	return verifier.result, nil
}

type prRevisionPublicationService struct {
	calls        []publisher.OperationKind
	branch       publisher.BranchPublicationRequest
	status       publisher.StatusPublicationRequest
	response     publisher.ResponsePublicationRequest
	branchStatus publisher.Status
	err          error
}

func (service *prRevisionPublicationService) PublishBranch(
	_ context.Context,
	request publisher.BranchPublicationRequest,
) (publisher.Publication, error) {
	service.calls = append(
		service.calls, publisher.OperationPublishPRBranch,
	)
	service.branch = request
	if service.err != nil {
		return publisher.Publication{}, service.err
	}
	status := service.branchStatus
	result := publisher.Result{
		Status: status, HeadSHA: request.NewSourceSHA,
		BaseSHA: request.ExpectedTargetSHA,
	}
	if status == publisher.StatusSucceeded {
		result.ExternalID = request.SourceRef
	} else {
		result.Detail = "fresh reconciliation is required"
	}
	return revisionPublication(
		publisher.OperationPublishPRBranch,
		&publisher.PRAction{
			Kind:   publisher.OperationPublishPRBranch,
			Branch: &request,
		},
		status,
		result,
	), nil
}

func (service *prRevisionPublicationService) FindOrCreate(
	context.Context,
	publisher.PullRequestPublicationRequest,
) (publisher.Publication, error) {
	panic("unexpected FindOrCreate")
}

func (service *prRevisionPublicationService) PublishStatus(
	_ context.Context,
	request publisher.StatusPublicationRequest,
) (publisher.Publication, error) {
	service.calls = append(
		service.calls, publisher.OperationPublishPRStatus,
	)
	service.status = request
	if service.err != nil {
		return publisher.Publication{}, service.err
	}
	return revisionPublication(
		publisher.OperationPublishPRStatus,
		&publisher.PRAction{
			Kind:   publisher.OperationPublishPRStatus,
			Status: &request,
		},
		publisher.StatusSucceeded,
		publisher.Result{
			Status:     publisher.StatusSucceeded,
			ExternalID: "status-1",
			URL:        "https://github.example/status/1",
			HeadSHA:    request.SourceSHA,
		},
	), nil
}

func (service *prRevisionPublicationService) PublishResponse(
	_ context.Context,
	request publisher.ResponsePublicationRequest,
) (publisher.Publication, error) {
	service.calls = append(
		service.calls, publisher.OperationRespondToReview,
	)
	service.response = request
	if service.err != nil {
		return publisher.Publication{}, service.err
	}
	return revisionPublication(
		publisher.OperationRespondToReview,
		&publisher.PRAction{
			Kind:     publisher.OperationRespondToReview,
			Response: &request,
		},
		publisher.StatusSucceeded,
		publisher.Result{
			Status:     publisher.StatusSucceeded,
			ExternalID: "response-1",
			URL:        "https://github.example/response/1",
			HeadSHA:    request.Batch.CommitSHA,
		},
	), nil
}

func revisionPublication(
	kind publisher.OperationKind,
	action *publisher.PRAction,
	status publisher.Status,
	result publisher.Result,
) publisher.Publication {
	key, err := action.OperationKey()
	if err != nil {
		panic(err)
	}
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	return publisher.Publication{
		ID: 1, OperationKey: key, OperationKind: kind,
		PRAction: action, Status: status, Attempt: 1,
		Result: result, CreatedAt: now, UpdatedAt: now,
	}
}

func revisionSnapshotRef(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	fill byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: typ, Digest: revisionDigest(fill),
	}
}

func revisionDigest(fill byte) snapshot.Digest {
	return snapshot.Digest(
		"sha256:" + strings.Repeat(string(fill), 64),
	)
}

func revisionObjectID(fill byte) string {
	return strings.Repeat(string(fill), 40)
}

func revisionRecordSnapshot(
	t *testing.T,
	id snapshot.SnapshotID,
	record any,
	files map[string][]byte,
) (snapshot.Snapshot, []byte) {
	t.Helper()
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	writeRevisionTarFile(t, writer, "record.json", recordJSON)
	for name, content := range files {
		writeRevisionTarFile(t, writer, name, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := (snapshot.Canonicalizer{}).Capture(
		context.Background(), bytes.NewReader(raw.Bytes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		_ = tree.Close()
		t.Fatal(err)
	}
	typ := snapshot.TypeRef("")
	switch typed := record.(type) {
	case contracts.Record[contracts.ValidationBody]:
		typ = typed.Type
	case contracts.Record[contracts.PublishImpactBody]:
		typ = typed.Type
	case contracts.Record[contracts.PullRequestResponseBody]:
		typ = typed.Type
	default:
		_ = tree.Close()
		t.Fatalf("unsupported revision record %T", record)
	}
	manifest := snapshot.Snapshot{
		ID: id, Type: typ, Digest: tree.Digest,
		ByteSize: tree.ByteSize, FileCount: tree.FileCount,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt: time.Date(
			2026, time.July, 30, 6, 0, 0, 0, time.UTC,
		),
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	return manifest, archive
}

func writeRevisionTarFile(
	t *testing.T,
	writer *tar.Writer,
	name string,
	content []byte,
) {
	t.Helper()
	if err := writer.WriteHeader(&tar.Header{
		Name: name, Mode: 0600, Size: int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
}

var (
	_ BindingStore                      = (*prRevisionBindingStore)(nil)
	_ AcceptedReviewAuthorityResolver   = (*prRevisionAcceptedReviewResolver)(nil)
	_ ApprovedBaselineAuthorityResolver = (*prRevisionApprovedBaselineResolver)(nil)
	_ MonitorObservationInspector       = (*prRevisionObservationInspector)(nil)
	_ PRRevisionSnapshotInspector       = (*prRevisionSnapshotInspector)(nil)
	_ PRRevisionCandidateInspector      = (*prRevisionCandidateInspector)(nil)
	_ publisher.EvidenceVerifier        = (*prRevisionEvidenceVerifier)(nil)
	_ publisher.PRImpactVerifier        = (*prRevisionImpactVerifier)(nil)
	_ publisher.PRService               = (*prRevisionPublicationService)(nil)
)
