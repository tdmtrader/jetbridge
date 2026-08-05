package publisher_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

func TestBuildPRApprovalContextBindsExactRevisionIntent(t *testing.T) {
	request := prApprovalRequest()
	contextEnvelope, err := publisher.BuildPRApprovalContext(request)
	if err != nil {
		t.Fatalf("BuildPRApprovalContext() error = %v", err)
	}

	if contextEnvelope.SchemaVersion != "1.0.0" ||
		contextEnvelope.TeamID != 17 ||
		contextEnvelope.WorkflowRunID != 19 ||
		contextEnvelope.BuildID != 21 ||
		contextEnvelope.BindingID != 41 ||
		contextEnvelope.ActionDigest != string(approvalDigest('1')) ||
		contextEnvelope.ObservationSnapshotID != 22 ||
		contextEnvelope.ObservationDigest != approvalDigest('1') ||
		contextEnvelope.CandidateSnapshotID != 23 ||
		contextEnvelope.CandidateDigest != approvalDigest('2') ||
		contextEnvelope.SourceHead != strings.Repeat("a", 40) ||
		contextEnvelope.TargetHead != strings.Repeat("b", 40) ||
		contextEnvelope.Destination != "github.example/acme/widget" ||
		contextEnvelope.ResponseSnapshotID != 27 ||
		contextEnvelope.ResponseDigest != approvalDigest('3') ||
		contextEnvelope.ValidationSnapshotID != 29 ||
		contextEnvelope.ValidationDigest != approvalDigest('4') ||
		contextEnvelope.ImpactSnapshotID != 31 ||
		contextEnvelope.ImpactDigest != approvalDigest('5') ||
		contextEnvelope.ApprovalPolicyVersion != "engineering/v3" {
		t.Fatalf("context envelope did not bind the exact PR revision: %#v", contextEnvelope)
	}
	if !strings.HasPrefix(contextEnvelope.IntentDigest, "sha256:") || len(contextEnvelope.IntentDigest) != 71 {
		t.Fatalf("IntentDigest = %q, want sha256 identity", contextEnvelope.IntentDigest)
	}
}

func TestBuildPRApprovalContextChangesForEveryBoundIntentField(t *testing.T) {
	baseline, err := publisher.BuildPRApprovalContext(prApprovalRequest())
	if err != nil {
		t.Fatalf("BuildPRApprovalContext() error = %v", err)
	}
	baselineJSON, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*publisher.PRApprovalRequest){
		"team":         func(request *publisher.PRApprovalRequest) { request.TeamID = 18 },
		"workflow run": func(request *publisher.PRApprovalRequest) { request.WorkflowRunID = 20 },
		"build":        func(request *publisher.PRApprovalRequest) { request.BuildID = 22 },
		"binding":      func(request *publisher.PRApprovalRequest) { request.BindingID = 42 },
		"action":       func(request *publisher.PRApprovalRequest) { request.ActionDigest = string(approvalDigest('6')) },
		"observation": func(request *publisher.PRApprovalRequest) {
			request.Observation = approvalRef(25, "pull-request/v1", '6')
		},
		"candidate": func(request *publisher.PRApprovalRequest) {
			request.Candidate = approvalRef(24, "repository-change/v1", '7')
		},
		"source head": func(request *publisher.PRApprovalRequest) { request.SourceHead = strings.Repeat("c", 40) },
		"target head": func(request *publisher.PRApprovalRequest) { request.TargetHead = strings.Repeat("d", 40) },
		"destination": func(request *publisher.PRApprovalRequest) { request.Destination = "other-forge.example/acme/widget" },
		"response": func(request *publisher.PRApprovalRequest) {
			request.Response = approvalRef(28, "pull-request-response/v1", '8')
		},
		"validation": func(request *publisher.PRApprovalRequest) {
			request.Validation = approvalRef(30, "validation/v1", '9')
		},
		"impact": func(request *publisher.PRApprovalRequest) {
			request.Impact = approvalRef(32, "publish-impact/v1", '0')
		},
		"policy": func(request *publisher.PRApprovalRequest) { request.ApprovalPolicyVersion = "engineering/v4" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := prApprovalRequest()
			mutate(&request)
			changed, err := publisher.BuildPRApprovalContext(request)
			if err != nil {
				t.Fatalf("BuildPRApprovalContext() error = %v", err)
			}
			changedJSON, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if string(changedJSON) == string(baselineJSON) {
				t.Fatalf("context remained unchanged after mutating %s", name)
			}
		})
	}
}

func TestPRApprovalVerifierDelegatesExactCanonicalContext(t *testing.T) {
	request := prApprovalRequest()
	request.Approval = approvalRef(37, "human-answer/v1", '6')
	resolvedAt := time.Date(2026, time.July, 30, 4, 5, 6, 0, time.UTC)
	evidence := publisher.ApprovalEvidence{
		WaitID:     workflowwait.ID(43),
		Question:   approvalRef(36, "question/v1", '7'),
		Answer:     request.Approval,
		ResolvedBy: "reviewer",
		ResolvedAt: resolvedAt,
	}
	exact := &exactApprovalRecorder{evidence: evidence}
	verifier, err := publisher.NewPRApprovalVerifier(exact)
	if err != nil {
		t.Fatalf("NewPRApprovalVerifier() error = %v", err)
	}

	got, err := verifier.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got != evidence {
		t.Fatalf("Verify() = %#v, want %#v", got, evidence)
	}
	if exact.calls != 1 {
		t.Fatalf("VerifyExact calls = %d, want 1", exact.calls)
	}
	if exact.request.TeamID != request.TeamID ||
		exact.request.WorkflowRunID != request.WorkflowRunID ||
		exact.request.BuildID != request.BuildID ||
		exact.request.Approval != request.Approval {
		t.Fatalf("durable request identity = %#v, want exact request binding", exact.request)
	}
	contextEnvelope, err := publisher.BuildPRApprovalContext(request)
	if err != nil {
		t.Fatal(err)
	}
	expectedContext, err := json.Marshal(contextEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(exact.request.ExpectedContext) != string(expectedContext) {
		t.Fatalf("ExpectedContext = %s, want %s", exact.request.ExpectedContext, expectedContext)
	}
}

func TestPRApprovalVerifierRejectsInvalidIntentBeforeDurableLookup(t *testing.T) {
	request := prApprovalRequest()
	request.Approval = approvalRef(37, "human-answer/v1", '6')
	request.ActionDigest = "not-a-digest"
	exact := &exactApprovalRecorder{}
	verifier, err := publisher.NewPRApprovalVerifier(exact)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("Verify() error = nil, want invalid action rejection")
	}
	if exact.calls != 0 {
		t.Fatalf("VerifyExact calls = %d, want no durable lookup for invalid intent", exact.calls)
	}
}

func TestBuildPRRevisionPublicationRequestCarriesExactHumanWaitAuthority(t *testing.T) {
	approval := prApprovalRequest()
	approval.Approval = approvalRef(37, "human-answer/v1", '6')
	resolvedAt := time.Date(2026, time.July, 30, 4, 5, 6, 0, time.UTC)
	humanWait := publisher.ApprovalEvidence{
		WaitID:     workflowwait.ID(43),
		Question:   approvalRef(36, "question/v1", '7'),
		Answer:     approval.Approval,
		ResolvedBy: "reviewer",
		ResolvedAt: resolvedAt,
	}
	authority := publisher.Authority{
		TeamID: approval.TeamID, TeamName: "main", BuildID: approval.BuildID,
		WorkflowRunID: approval.WorkflowRunID, Actor: "build:21",
	}
	observation := approval.Observation
	response := approval.Response

	request, err := publisher.BuildPRRevisionPublicationRequest(
		authority, observation, response, approval, humanWait,
	)
	if err != nil {
		t.Fatalf("BuildPRRevisionPublicationRequest() error = %v", err)
	}
	if request.Authority != authority ||
		request.BindingID != approval.BindingID ||
		request.ActionDigest != approval.ActionDigest ||
		request.Observation != observation ||
		request.Candidate != approval.Candidate ||
		request.Validation != approval.Validation ||
		request.Impact != approval.Impact ||
		request.Response != response ||
		request.SourceHead != approval.SourceHead ||
		request.TargetHead != approval.TargetHead ||
		request.Destination != approval.Destination ||
		request.ApprovalPolicyVersion != approval.ApprovalPolicyVersion {
		t.Fatalf("publication request did not retain exact revision authority: %#v", request)
	}
	if request.Evidence.Kind != publisher.EvidenceHumanWait ||
		request.Evidence.HumanWait == nil ||
		*request.Evidence.HumanWait != humanWait ||
		request.Evidence.AcceptedReview != nil {
		t.Fatalf("publication evidence = %#v, want exact exclusive human wait", request.Evidence)
	}
	if request.ApprovalContext == nil || request.AcceptedReview != nil {
		t.Fatalf("human-wait authority envelope = context %#v accepted %#v", request.ApprovalContext, request.AcceptedReview)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("publication request is invalid: %v", err)
	}
	if _, err := publisher.BuildPRRevisionPublicationRequest(
		authority,
		observation,
		approvalRef(35, "pull-request-response/v1", '3'),
		approval,
		humanWait,
	); err == nil {
		t.Fatal("BuildPRRevisionPublicationRequest() accepted a response snapshot ID not bound by the approval")
	}

	for name, mutate := range map[string]func(*publisher.PRRevisionPublicationRequest){
		"team":         func(value *publisher.PRRevisionPublicationRequest) { value.Authority.TeamID++ },
		"workflow run": func(value *publisher.PRRevisionPublicationRequest) { value.Authority.WorkflowRunID++ },
		"build":        func(value *publisher.PRRevisionPublicationRequest) { value.Authority.BuildID++ },
		"binding":      func(value *publisher.PRRevisionPublicationRequest) { value.BindingID++ },
		"action": func(value *publisher.PRRevisionPublicationRequest) {
			value.ActionDigest = string(approvalDigest('9'))
		},
		"observation": func(value *publisher.PRRevisionPublicationRequest) {
			value.Observation = approvalRef(40, "pull-request/v1", '9')
		},
		"candidate": func(value *publisher.PRRevisionPublicationRequest) {
			value.Candidate = approvalRef(41, "repository-change/v1", '9')
		},
		"validation": func(value *publisher.PRRevisionPublicationRequest) {
			value.Validation = approvalRef(42, "validation/v1", '9')
		},
		"impact": func(value *publisher.PRRevisionPublicationRequest) {
			value.Impact = approvalRef(43, "publish-impact/v1", '9')
		},
		"response": func(value *publisher.PRRevisionPublicationRequest) {
			value.Response = approvalRef(44, "pull-request-response/v1", '9')
		},
		"source head": func(value *publisher.PRRevisionPublicationRequest) {
			value.SourceHead = strings.Repeat("c", 40)
		},
		"target head": func(value *publisher.PRRevisionPublicationRequest) {
			value.TargetHead = strings.Repeat("d", 40)
		},
		"destination": func(value *publisher.PRRevisionPublicationRequest) {
			value.Destination = "other-forge.example/acme/other"
		},
		"policy": func(value *publisher.PRRevisionPublicationRequest) {
			value.ApprovalPolicyVersion = "engineering/v4"
		},
		"approval context": func(value *publisher.PRRevisionPublicationRequest) {
			value.ApprovalContext.IntentDigest = string(approvalDigest('9'))
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatalf("Validate() error = nil after changing %s", name)
			}
		})
	}
}

func TestBuildAcceptedPRRevisionPublicationRequestCarriesExactNoWaitAuthority(t *testing.T) {
	intent := prApprovalRequest()
	acceptedRequest := publisher.AcceptedReviewEvidenceRequest{
		Review:              approvalRef(51, "review/v1", '6'),
		Candidate:           approvalRef(52, "repository/v1", '7'),
		Validation:          approvalRef(53, "validation/v1", '8'),
		ReviewWorkflowRunID: 54,
		OutcomeRevision:     3,
	}
	accepted := publisher.AcceptedReviewEvidence{
		Review:              acceptedRequest.Review,
		Candidate:           acceptedRequest.Candidate,
		Validation:          acceptedRequest.Validation,
		ReviewWorkflowRunID: acceptedRequest.ReviewWorkflowRunID,
		OutcomeRevision:     acceptedRequest.OutcomeRevision,
		AcceptedBy:          "reviewer",
		AcceptedAt:          time.Date(2026, time.July, 30, 5, 6, 7, 0, time.UTC),
	}
	evidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview, AcceptedReview: &accepted,
	}
	authority := publisher.Authority{
		TeamID: intent.TeamID, TeamName: "main", BuildID: intent.BuildID,
		WorkflowRunID: intent.WorkflowRunID, Actor: "build:21",
	}

	request, err := publisher.BuildAcceptedPRRevisionPublicationRequest(
		authority, intent.Observation, intent.Response, intent, acceptedRequest, evidence,
	)
	if err != nil {
		t.Fatalf("BuildAcceptedPRRevisionPublicationRequest() error = %v", err)
	}
	if request.ApprovalContext != nil || request.AcceptedReview == nil ||
		*request.AcceptedReview != acceptedRequest ||
		request.Evidence.Kind != publisher.EvidenceAcceptedReview ||
		request.Evidence.AcceptedReview == nil ||
		*request.Evidence.AcceptedReview != accepted {
		t.Fatalf("accepted-review publication authority = %#v", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("publication request is invalid: %v", err)
	}

	for name, mutate := range map[string]func(*publisher.PRRevisionPublicationRequest){
		"accepted review request": func(value *publisher.PRRevisionPublicationRequest) {
			value.AcceptedReview.OutcomeRevision++
		},
		"resolved review": func(value *publisher.PRRevisionPublicationRequest) {
			value.Evidence.AcceptedReview.Review = approvalRef(55, "review/v1", '9')
		},
		"human context": func(value *publisher.PRRevisionPublicationRequest) {
			contextEnvelope, contextErr := publisher.BuildPRApprovalContext(intent)
			if contextErr != nil {
				t.Fatal(contextErr)
			}
			value.ApprovalContext = &contextEnvelope
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			acceptedRequestCopy := *request.AcceptedReview
			acceptedEvidenceCopy := *request.Evidence.AcceptedReview
			changed.AcceptedReview = &acceptedRequestCopy
			changed.Evidence = request.Evidence.Clone()
			changed.Evidence.AcceptedReview = &acceptedEvidenceCopy
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatalf("Validate() error = nil after changing %s", name)
			}
		})
	}
}

func prApprovalRequest() publisher.PRApprovalRequest {
	return publisher.PRApprovalRequest{
		TeamID:                17,
		WorkflowRunID:         19,
		BuildID:               21,
		BindingID:             41,
		ActionDigest:          string(approvalDigest('1')),
		Observation:           approvalRef(22, "pull-request/v1", '1'),
		Candidate:             approvalRef(23, "repository-change/v1", '2'),
		SourceHead:            strings.Repeat("a", 40),
		TargetHead:            strings.Repeat("b", 40),
		Destination:           "github.example/acme/widget",
		Response:              approvalRef(27, "pull-request-response/v1", '3'),
		Validation:            approvalRef(29, "validation/v1", '4'),
		Impact:                approvalRef(31, "publish-impact/v1", '5'),
		ApprovalPolicyVersion: "engineering/v3",
	}
}

func approvalRef(id snapshot.SnapshotID, typ snapshot.TypeRef, fill byte) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: id, Type: typ, Digest: approvalDigest(fill)}
}

func approvalDigest(fill byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(fill), 64))
}

type exactApprovalRecorder struct {
	request  publisher.DurableApprovalRequest
	evidence publisher.ApprovalEvidence
	calls    int
}

func (recorder *exactApprovalRecorder) VerifyExact(
	_ context.Context,
	request publisher.DurableApprovalRequest,
) (publisher.ApprovalEvidence, error) {
	recorder.calls++
	recorder.request = request
	return recorder.evidence, nil
}
