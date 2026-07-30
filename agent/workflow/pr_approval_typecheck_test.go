package workflow

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestTypeCheckPRReapprovalBindsExactTypedIntent(t *testing.T) {
	wait := func() atc.Step {
		return atc.Step{Config: &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
			Name: "reapproval",
			PRApproval: &atc.PRApprovalIntent{
				BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
				Observation: "pull-request", Candidate: "candidate",
				Impact: "publish-impact", Response: "response",
				Destination:           "github.example/team/repo",
				ApprovalPolicyVersion: "engineering/v3", Prompt: "Approve this exact revision?",
				AcceptedReview: testPRAcceptedReviewIntent(),
			},
			Validation: "validation",
			Type:       snapshot.TypeRef("human-answer/v1"),
			OnTimeout:  atc.AwaitSnapshotOnTimeoutFail,
		}}}
	}
	publish := func() *atc.PublishSnapshotStep {
		return &atc.PublishSnapshotStep{
			Name: "publish-revision", Publisher: publisher.GitPublisher,
			Input: "candidate", InputType: repositoryChangeV1,
			Destination: "github.example/team/repo", Mode: publisher.ModePullRequest,
			Parameters:            map[string]string{"source_branch": "feature", "target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v3",
			Approval:              "reapproval",
			Validation:            "validation",
			PRApproval: &atc.PRApprovalPublicationIntent{
				BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
				Observation: "pull-request", Impact: "publish-impact", Response: "response",
				AcceptedReview: testPRAcceptedReviewIntent(),
			},
		}
	}
	inputs := []snapshot.Port{
		{Name: "pull-request", Type: snapshot.TypeRef("pull-request/v1")},
		{Name: "candidate", Type: repositoryChangeV1},
		{Name: "publish-impact", Type: snapshot.TypeRef("publish-impact/v1")},
		{Name: "response", Type: snapshot.TypeRef("pull-request-response/v1")},
		{Name: "accepted-review", Type: snapshot.TypeRef("review/v1")},
		{Name: "accepted-candidate", Type: snapshot.TypeRef("repository/v1")},
		{Name: "accepted-validation", Type: snapshot.TypeRef("validation/v1")},
	}
	function := func(waitStep atc.Step, publishStep *atc.PublishSnapshotStep) *FunctionConfig {
		return &FunctionConfig{
			SignatureVersion: 1,
			Inputs:           inputs,
			Plan: []atc.Step{
				exactValidationStep("candidate"),
				waitStep,
				{Config: publishStep},
			},
			DevValidationProfiles:       validationProfilesFor("candidate"),
			DevValidationProvenanceHash: validationProvenanceFor("candidate"),
		}
	}

	if err := TypeCheckFunction(function(wait(), publish())); err != nil {
		t.Fatalf("valid PR reapproval flow: %v", err)
	}

	for name, mutate := range map[string]func(*atc.PublishSnapshotStep){
		"binding": func(step *atc.PublishSnapshotStep) { step.PRApproval.BindingID++ },
		"action": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.ActionDigest = "sha256:" + strings.Repeat("2", 64)
		},
		"observation": func(step *atc.PublishSnapshotStep) { step.PRApproval.Observation = "other-observation" },
		"impact":      func(step *atc.PublishSnapshotStep) { step.PRApproval.Impact = "other-impact" },
		"response":    func(step *atc.PublishSnapshotStep) { step.PRApproval.Response = "other-response" },
		"destination": func(step *atc.PublishSnapshotStep) { step.Destination = "github.example/team/other" },
		"candidate":   func(step *atc.PublishSnapshotStep) { step.Input = "other-candidate" },
		"validation":  func(step *atc.PublishSnapshotStep) { step.Validation = "other-validation" },
		"policy":      func(step *atc.PublishSnapshotStep) { step.ApprovalPolicyVersion = "engineering/v4" },
		"accepted review": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.AcceptedReview.Review = "other-review"
		},
		"accepted candidate": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.AcceptedReview.Candidate = "other-accepted-candidate"
		},
		"accepted validation": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.AcceptedReview.Validation = "other-accepted-validation"
		},
		"accepted run": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.AcceptedReview.ReviewWorkflowRunID = "8"
		},
		"accepted outcome": func(step *atc.PublishSnapshotStep) {
			step.PRApproval.AcceptedReview.OutcomeRevision = 3
		},
	} {
		t.Run("mismatched "+name, func(t *testing.T) {
			publication := publish()
			mutate(publication)
			err := TypeCheckFunction(function(wait(), publication))
			if err == nil || !strings.Contains(err.Error(), "exact PR reapproval") {
				t.Fatalf("error = %v, want exact PR reapproval mismatch", err)
			}
		})
	}
}

func TestTypeCheckPRReapprovalRejectsGenericQuestionAndWrongTypedInputs(t *testing.T) {
	publication := &atc.PublishSnapshotStep{
		Name: "publish-revision", Publisher: publisher.GitPublisher,
		Input: "candidate", InputType: repositoryChangeV1,
		Destination: "github.example/team/repo", Mode: publisher.ModePullRequest,
		Parameters:            map[string]string{"source_branch": "feature", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v3", Approval: "reapproval", Validation: "validation",
		PRApproval: &atc.PRApprovalPublicationIntent{
			BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
			Observation: "pull-request", Impact: "publish-impact", Response: "response",
			AcceptedReview: testPRAcceptedReviewIntent(),
		},
	}
	ordinary := atc.Step{Config: &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
		Name: "reapproval", Question: "question", Type: snapshot.TypeRef("human-answer/v1"),
		OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
	}}}
	err := TypeCheckFunction(&FunctionConfig{
		SignatureVersion: 1,
		Inputs: []snapshot.Port{
			{Name: "question", Type: snapshot.TypeRef("question/v1")},
			{Name: "candidate", Type: repositoryChangeV1},
		},
		Plan:                  []atc.Step{exactValidationStep("candidate"), ordinary, {Config: publication}},
		DevValidationProfiles: validationProfilesFor("candidate"), DevValidationProvenanceHash: validationProvenanceFor("candidate"),
	})
	if err == nil || !strings.Contains(err.Error(), "server-bound pr_approval") {
		t.Fatalf("error = %v, want authored question refusal", err)
	}

	wait := atc.Step{Config: &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
		Name: "reapproval",
		PRApproval: &atc.PRApprovalIntent{
			BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
			Observation: "pull-request", Candidate: "candidate",
			Impact: "publish-impact", Response: "response",
			Destination:           "github.example/team/repo",
			ApprovalPolicyVersion: "engineering/v3", Prompt: "Approve?",
			AcceptedReview: testPRAcceptedReviewIntent(),
		},
		Validation: "validation", Type: snapshot.TypeRef("human-answer/v1"),
		OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
	}}}
	err = TypeCheckFunction(&FunctionConfig{
		SignatureVersion: 1,
		Inputs: []snapshot.Port{
			{Name: "pull-request", Type: snapshot.TypeRef("repository/v1")},
			{Name: "candidate", Type: repositoryChangeV1},
			{Name: "publish-impact", Type: snapshot.TypeRef("publish-impact/v1")},
			{Name: "response", Type: snapshot.TypeRef("pull-request-response/v1")},
		},
		Plan:                  []atc.Step{exactValidationStep("candidate"), wait},
		DevValidationProfiles: validationProfilesFor("candidate"), DevValidationProvenanceHash: validationProvenanceFor("candidate"),
	})
	if err == nil || !strings.Contains(err.Error(), "pull-request/v1") {
		t.Fatalf("error = %v, want exact observation type rejection", err)
	}
}

func testPRAcceptedReviewIntent() *atc.PRAcceptedReviewIntent {
	return &atc.PRAcceptedReviewIntent{
		Review: "accepted-review", Candidate: "accepted-candidate", Validation: "accepted-validation",
		ReviewWorkflowRunID: "7", OutcomeRevision: 2,
	}
}

func TestPRApprovalSourceRejectsUnknownNestedFields(t *testing.T) {
	valid := map[string]any{
		"await_snapshot": "reapproval",
		"pr_approval": map[string]any{
			"binding_id": 41, "action_digest": "sha256:" + strings.Repeat("1", 64),
			"observation": "pull-request", "candidate": "candidate",
			"impact": "publish-impact", "response": "response",
			"destination":             "github.example/team/repo",
			"approval_policy_version": "engineering/v3", "prompt": "Approve?",
			"accepted_review": map[string]any{
				"review": "accepted-review", "candidate": "accepted-candidate",
				"validation": "accepted-validation", "review_workflow_run_id": "7",
				"outcome_revision": 2,
			},
		},
	}
	if err := validateFunctionStepSource(valid, "workflow.plan[0]"); err != nil {
		t.Fatalf("valid PR approval source: %v", err)
	}
	valid["pr_approval"].(map[string]any)["Binding_ID"] = 42
	if err := validateFunctionStepSource(valid, "workflow.plan[0]"); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown nested field rejection", err)
	}
}

func TestRenderValidationRequirementsBindsPRApprovalCandidate(t *testing.T) {
	wait := &atc.AwaitSnapshotStep{
		Name: "reapproval",
		PRApproval: &atc.PRApprovalIntent{
			BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
			Observation: "pull-request", Candidate: "candidate",
			Impact: "publish-impact", Response: "response",
			Destination:           "github.example/team/repo",
			ApprovalPolicyVersion: "engineering/v3", Prompt: "Approve?",
			AcceptedReview: testPRAcceptedReviewIntent(),
		},
		Validation: "validation", Type: snapshot.TypeRef("human-answer/v1"),
		OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
	}
	function := &FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			renderValidationStep("candidate"),
			{Config: &atc.TimeoutStep{Duration: "1h", Step: wait}},
		},
	}
	if err := renderValidationRequirements(function); err != nil {
		t.Fatalf("renderValidationRequirements() error = %v", err)
	}
	if wait.PRApprovalValidation == nil ||
		wait.PRApprovalValidation.Candidate != "candidate" ||
		wait.PRApprovalValidation.Validation != "validation" ||
		wait.PRApprovalValidation.Authority == nil {
		t.Fatalf("PRApprovalValidation = %#v, want exact private authority", wait.PRApprovalValidation)
	}
}
