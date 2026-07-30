package pullrequest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestDecideImpactEnforcesModeAuthority(t *testing.T) {
	tests := []struct {
		name       string
		policy     pullrequest.ImpactPolicy
		assessment *contracts.AgentImpactAssessment
		required   bool
	}{
		{
			name:       "always requires approval for the changed candidate",
			policy:     pullrequest.ImpactPolicy{Mode: pullrequest.ImpactModeAlways},
			assessment: &contracts.AgentImpactAssessment{Rationale: "The change is routine."},
			required:   true,
		},
		{
			name: "rules permits an unchanged policy surface",
			policy: pullrequest.ImpactPolicy{
				Mode: pullrequest.ImpactModeRules, MaxChangedFiles: 4, MaxChangedLines: 20,
			},
			assessment: &contracts.AgentImpactAssessment{Rationale: "No semantic escalation is needed."},
			required:   false,
		},
		{
			name:       "agent decides may escalate",
			policy:     pullrequest.ImpactPolicy{Mode: pullrequest.ImpactModeAgentDecides},
			assessment: &contracts.AgentImpactAssessment{ReapprovalRequired: true, Rationale: "The public behavior changed."},
			required:   true,
		},
		{
			name:       "agent decides may waive absent deterministic escalation",
			policy:     pullrequest.ImpactPolicy{Mode: pullrequest.ImpactModeAgentDecides},
			assessment: &contracts.AgentImpactAssessment{Rationale: "Only generated documentation changed."},
			required:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := pullrequest.DecideImpact(test.policy, smallImpact(), test.assessment)
			if err != nil {
				t.Fatalf("DecideImpact() error = %v", err)
			}
			if body.ReapprovalRequired != test.required {
				t.Fatalf("ReapprovalRequired = %t, want %t (reasons %v)", body.ReapprovalRequired, test.required, body.Reasons)
			}
			if err := body.Validate(nil); err != nil {
				t.Fatalf("produced invalid publish-impact body: %v", err)
			}
		})
	}
}

func TestDecideImpactAgentCannotWaiveDeterministicEscalation(t *testing.T) {
	tests := []struct {
		name   string
		policy pullrequest.ImpactPolicy
		mutate func(*pullrequest.DeterministicImpact)
		reason string
	}{
		{
			name: "sensitive path",
			policy: pullrequest.ImpactPolicy{
				Mode: pullrequest.ImpactModeRules, SensitivePaths: []string{"security/**"},
			},
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.ChangedFiles = []contracts.PublishChangedFile{
					{Path: "security/policy/rules.go", AddedLines: 1},
				}
				impact.ChangedLines = 1
			},
			reason: "sensitive",
		},
		{
			name: "conflict resolution",
			policy: pullrequest.ImpactPolicy{
				Mode: pullrequest.ImpactModeRules, ConflictRequiresApproval: true,
			},
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.ConflictResolution = true
			},
			reason: "conflict",
		},
		{
			name: "validation changed",
			policy: pullrequest.ImpactPolicy{
				Mode: pullrequest.ImpactModeRules, ValidationChangeRequiresApproval: true,
			},
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.ValidationChanges = []string{"unit: success -> failure"}
			},
			reason: "validation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			impact := smallImpact()
			test.mutate(&impact)
			body, err := pullrequest.DecideImpact(
				test.policy,
				impact,
				&contracts.AgentImpactAssessment{
					ReapprovalRequired: false,
					Rationale:          "The deterministic escalation can be ignored.",
				},
			)
			if err != nil {
				t.Fatalf("DecideImpact() error = %v", err)
			}
			if !body.ReapprovalRequired {
				t.Fatal("ReapprovalRequired = false, want deterministic escalation to remain authoritative")
			}
			if !containsSubstring(body.Reasons, test.reason) {
				t.Fatalf("Reasons = %v, want a %q reason", body.Reasons, test.reason)
			}
		})
	}
}

func TestDecideImpactAssessmentFailureAndAmbiguityEscalate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pullrequest.DeterministicImpact)
	}{
		{
			name: "assessor error",
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.AssessmentError = errors.New("model unavailable")
			},
		},
		{
			name: "ambiguous impact",
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.Ambiguous = true
			},
		},
		{
			name: "ambiguous assessment",
			mutate: func(impact *pullrequest.DeterministicImpact) {
				impact.AssessmentAmbiguous = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			impact := smallImpact()
			test.mutate(&impact)
			body, err := pullrequest.DecideImpact(
				pullrequest.ImpactPolicy{Mode: pullrequest.ImpactModeRules},
				impact,
				&contracts.AgentImpactAssessment{Rationale: "No escalation requested."},
			)
			if err != nil {
				t.Fatalf("DecideImpact() error = %v", err)
			}
			if !body.ReapprovalRequired {
				t.Fatal("ReapprovalRequired = false, want fail-closed escalation")
			}
			if body.AgentAssessment != nil && body.AgentAssessment.Rationale == "" {
				t.Fatal("invalid assessment was retained in the sealed body")
			}
		})
	}
}

func TestDecideImpactNoRuleMatchStillRequiresAnAssessment(t *testing.T) {
	body, err := pullrequest.DecideImpact(
		pullrequest.ImpactPolicy{
			Mode:                             pullrequest.ImpactModeRules,
			SensitivePaths:                   []string{"security/**"},
			MaxChangedFiles:                  10,
			MaxChangedLines:                  100,
			ConflictRequiresApproval:         true,
			ValidationChangeRequiresApproval: true,
		},
		smallImpact(),
		nil,
	)
	if err != nil {
		t.Fatalf("DecideImpact() error = %v", err)
	}
	if !body.ReapprovalRequired || !containsSubstring(body.Reasons, "unavailable") {
		t.Fatalf("missing-assessment decision = required %t reasons %v", body.ReapprovalRequired, body.Reasons)
	}
	for _, rule := range body.RuleResults {
		if !rule.Passed {
			t.Fatalf("RuleResults = %#v, want every configured rule to pass", body.RuleResults)
		}
	}
}

func TestDecideImpactNoRuleMatchAndExplicitAssessmentIsNoOp(t *testing.T) {
	body, err := pullrequest.DecideImpact(
		pullrequest.ImpactPolicy{
			Mode:                             pullrequest.ImpactModeRules,
			SensitivePaths:                   []string{"security/**"},
			MaxChangedFiles:                  10,
			MaxChangedLines:                  100,
			ConflictRequiresApproval:         true,
			ValidationChangeRequiresApproval: true,
		},
		smallImpact(),
		&contracts.AgentImpactAssessment{Rationale: "No semantic escalation is needed."},
	)
	if err != nil {
		t.Fatalf("DecideImpact() error = %v", err)
	}
	if body.ReapprovalRequired || len(body.Reasons) != 0 {
		t.Fatalf("no-op decision = required %t reasons %v", body.ReapprovalRequired, body.Reasons)
	}
}

func TestDecideImpactMissingOrInvalidAssessmentFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		assessment *contracts.AgentImpactAssessment
		reason     string
	}{
		"missing": {reason: "unavailable"},
		"invalid": {assessment: &contracts.AgentImpactAssessment{Rationale: ""}, reason: "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := pullrequest.DecideImpact(
				pullrequest.ImpactPolicy{Mode: pullrequest.ImpactModeRules},
				smallImpact(),
				test.assessment,
			)
			if err != nil {
				t.Fatalf("DecideImpact() error = %v", err)
			}
			if !body.ReapprovalRequired || !containsSubstring(body.Reasons, test.reason) {
				t.Fatalf("decision = required %t reasons %v, want %s fail-closed reason", body.ReapprovalRequired, body.Reasons, test.reason)
			}
		})
	}
}

func TestDecideImpactRejectsOptionalRulesInAgentDecidesMode(t *testing.T) {
	_, err := pullrequest.DecideImpact(
		pullrequest.ImpactPolicy{
			Mode: pullrequest.ImpactModeAgentDecides, SensitivePaths: []string{"security/**"},
		},
		smallImpact(),
		&contracts.AgentImpactAssessment{Rationale: "No escalation requested."},
	)
	if err == nil {
		t.Fatal("DecideImpact() error = nil, want agent-decides policy configuration rejection")
	}
}

func TestImpactVerifierRecomputesPolicyAndBindsAcceptedBaseline(t *testing.T) {
	policy := pullrequest.ImpactPolicy{
		Mode: pullrequest.ImpactModeRules, MaxChangedFiles: 5, MaxChangedLines: 20,
	}
	deterministic := smallImpact()
	assessment := &contracts.AgentImpactAssessment{Rationale: "No semantic escalation is needed."}
	body, err := pullrequest.DecideImpact(policy, deterministic, assessment)
	if err != nil {
		t.Fatal(err)
	}
	baseline := snapshot.SnapshotRef{
		ID: 41, Type: "repository/v1", Digest: snapshot.Digest(deterministic.BaselineDigest),
	}
	candidate := snapshot.SnapshotRef{
		ID: 42, Type: "repository-change/v1", Digest: snapshot.Digest(deterministic.CandidateDigest),
	}
	impact := snapshot.SnapshotRef{
		ID: 43, Type: "publish-impact/v1",
		Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
	}
	accepted := publisher.AcceptedReviewEvidence{
		Review: snapshot.SnapshotRef{
			ID: 44, Type: "review/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("d", 64)),
		},
		Candidate: baseline,
		Validation: snapshot.SnapshotRef{
			ID: 45, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("e", 64)),
		},
		ReviewWorkflowRunID: 46, OutcomeRevision: 2, AcceptedBy: "reviewer",
		AcceptedAt: time.Date(2026, time.July, 30, 7, 8, 9, 0, time.UTC),
	}
	request := publisher.PRImpactVerificationRequest{
		TeamID: 17, BindingID: 18,
		ActionDigest:  snapshot.Digest("sha256:" + strings.Repeat("1", 64)),
		PolicyVersion: "engineering/v3",
		Observation: snapshot.SnapshotRef{
			ID: 39, Type: "pull-request/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("2", 64)),
		},
		Baseline: baseline, Candidate: candidate,
		Validation: snapshot.SnapshotRef{
			ID: 48, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("3", 64)),
		},
		Impact: impact,
		Response: snapshot.SnapshotRef{
			ID: 49, Type: "pull-request-response/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64)),
		},
		AcceptedReview: publisher.PublicationEvidence{
			Kind: publisher.EvidenceAcceptedReview, AcceptedReview: &accepted,
		},
		Body: body,
	}
	policies := &impactPolicyResolverStub{policy: policy, found: true}
	evaluator := &impactEvaluatorStub{evaluation: pullrequest.AuthoritativeImpactEvaluation{
		Deterministic: deterministic, AgentAssessment: assessment,
	}}
	verifier, err := pullrequest.NewImpactVerifier(policies, evaluator)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.VerifyPRImpact(context.Background(), request)
	if err != nil {
		t.Fatalf("VerifyPRImpact() error = %v", err)
	}
	if verified.ReapprovalRequired {
		t.Fatalf("verified decision = %#v, want explicit no-op", verified)
	}
	if policies.calls != 1 || evaluator.calls != 1 {
		t.Fatalf("resolver calls = %d evaluator calls = %d, want one each", policies.calls, evaluator.calls)
	}

	t.Run("missing assessment cannot waive", func(t *testing.T) {
		changed := request
		changed.Body = body
		changed.Body.AgentAssessment = nil
		if _, err := verifier.VerifyPRImpact(context.Background(), changed); err == nil {
			t.Fatal("VerifyPRImpact() accepted no-reapproval without an assessment")
		}
	})
	t.Run("accepted candidate must be baseline", func(t *testing.T) {
		changed := request
		evidence := accepted
		evidence.Candidate = snapshot.SnapshotRef{
			ID: 47, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("f", 64)),
		}
		changed.AcceptedReview.AcceptedReview = &evidence
		if _, err := verifier.VerifyPRImpact(context.Background(), changed); err == nil {
			t.Fatal("VerifyPRImpact() accepted evidence for another baseline")
		}
	})
	t.Run("sealed deterministic facts must match recomputation", func(t *testing.T) {
		changedEvaluator := &impactEvaluatorStub{evaluation: pullrequest.AuthoritativeImpactEvaluation{
			Deterministic: deterministic, AgentAssessment: assessment,
		}}
		changedEvaluator.evaluation.Deterministic.ChangedFiles = []contracts.PublishChangedFile{
			{Path: "docs/guide.md", AddedLines: 3, RemovedLines: 1},
		}
		changedEvaluator.evaluation.Deterministic.ChangedLines = 4
		changedVerifier, buildErr := pullrequest.NewImpactVerifier(policies, changedEvaluator)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		if _, err := changedVerifier.VerifyPRImpact(context.Background(), request); err == nil {
			t.Fatal("VerifyPRImpact() accepted stale deterministic impact facts")
		}
	})
}

func TestImpactVerificationRequestRejectsMissingOrMismatchedExactInputs(t *testing.T) {
	request := validImpactVerificationRequest(t)

	tests := []struct {
		name   string
		mutate func(*publisher.PRImpactVerificationRequest)
	}{
		{
			name: "missing binding",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.BindingID = 0
			},
		},
		{
			name: "missing action",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.ActionDigest = ""
			},
		},
		{
			name: "missing observation",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Observation = snapshot.SnapshotRef{}
			},
		},
		{
			name: "mismatched observation type",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Observation.Type = snapshot.TypeRef("repository/v1")
			},
		},
		{
			name: "missing final validation",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Validation = snapshot.SnapshotRef{}
			},
		},
		{
			name: "mismatched final validation type",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Validation.Type = snapshot.TypeRef("review/v1")
			},
		},
		{
			name: "missing response",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Response = snapshot.SnapshotRef{}
			},
		},
		{
			name: "mismatched response type",
			mutate: func(request *publisher.PRImpactVerificationRequest) {
				request.Response.Type = snapshot.TypeRef("pull-request/v1")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want exact authority rejection")
			}
		})
	}
}

func TestImpactVerifierRejectsForgedNoReapprovalAssessment(t *testing.T) {
	request := validImpactVerificationRequest(t)
	policy := pullrequest.ImpactPolicy{
		Mode: pullrequest.ImpactModeRules, MaxChangedFiles: 5, MaxChangedLines: 20,
	}
	deterministic := smallImpact()
	for name, assessment := range map[string]*contracts.AgentImpactAssessment{
		"authoritative assessment unavailable": nil,
		"authoritative assessment escalates": {
			ReapprovalRequired: true,
			Rationale:          "The final revision changes public behavior.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, err := pullrequest.NewImpactVerifier(
				&impactPolicyResolverStub{policy: policy, found: true},
				&impactEvaluatorStub{evaluation: pullrequest.AuthoritativeImpactEvaluation{
					Deterministic: deterministic, AgentAssessment: assessment,
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.VerifyPRImpact(context.Background(), request); err == nil {
				t.Fatal("VerifyPRImpact() authorized forged no-reapproval assessment")
			}
		})
	}
}

func validImpactVerificationRequest(t *testing.T) publisher.PRImpactVerificationRequest {
	t.Helper()
	policy := pullrequest.ImpactPolicy{
		Mode: pullrequest.ImpactModeRules, MaxChangedFiles: 5, MaxChangedLines: 20,
	}
	deterministic := smallImpact()
	body, err := pullrequest.DecideImpact(
		policy,
		deterministic,
		&contracts.AgentImpactAssessment{Rationale: "No semantic escalation is needed."},
	)
	if err != nil {
		t.Fatal(err)
	}
	baseline := snapshot.SnapshotRef{
		ID: 41, Type: "repository/v1", Digest: snapshot.Digest(deterministic.BaselineDigest),
	}
	return publisher.PRImpactVerificationRequest{
		TeamID: 17, BindingID: 18,
		ActionDigest:  snapshot.Digest("sha256:" + strings.Repeat("1", 64)),
		PolicyVersion: "engineering/v3",
		Observation: snapshot.SnapshotRef{
			ID: 39, Type: "pull-request/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("2", 64)),
		},
		Baseline: baseline,
		Candidate: snapshot.SnapshotRef{
			ID: 42, Type: "repository-change/v1", Digest: snapshot.Digest(deterministic.CandidateDigest),
		},
		Validation: snapshot.SnapshotRef{
			ID: 48, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("3", 64)),
		},
		Impact: snapshot.SnapshotRef{
			ID: 43, Type: "publish-impact/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
		},
		Response: snapshot.SnapshotRef{
			ID: 49, Type: "pull-request-response/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64)),
		},
		AcceptedReview: publisher.PublicationEvidence{
			Kind: publisher.EvidenceAcceptedReview,
			AcceptedReview: &publisher.AcceptedReviewEvidence{
				Review: snapshot.SnapshotRef{
					ID: 44, Type: "review/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("d", 64)),
				},
				Candidate: baseline,
				Validation: snapshot.SnapshotRef{
					ID: 45, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("e", 64)),
				},
				ReviewWorkflowRunID: 46, OutcomeRevision: 2, AcceptedBy: "reviewer",
				AcceptedAt: time.Date(2026, time.July, 30, 7, 8, 9, 0, time.UTC),
			},
		},
		Body: body,
	}
}

func smallImpact() pullrequest.DeterministicImpact {
	return pullrequest.DeterministicImpact{
		BaselineDigest:  "sha256:" + strings.Repeat("a", 64),
		CandidateDigest: "sha256:" + strings.Repeat("b", 64),
		ChangedFiles: []contracts.PublishChangedFile{
			{Path: "docs/guide.md", AddedLines: 2, RemovedLines: 1},
		},
		ChangedLines: 3,
	}
}

func containsSubstring(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), strings.ToLower(wanted)) {
			return true
		}
	}
	return false
}

type impactPolicyResolverStub struct {
	policy pullrequest.ImpactPolicy
	found  bool
	err    error
	calls  int
}

func (stub *impactPolicyResolverStub) ResolveImpactPolicy(
	context.Context,
	int,
	string,
) (pullrequest.ImpactPolicy, bool, error) {
	stub.calls++
	return stub.policy, stub.found, stub.err
}

type impactEvaluatorStub struct {
	evaluation pullrequest.AuthoritativeImpactEvaluation
	err        error
	calls      int
}

func (stub *impactEvaluatorStub) EvaluateAuthoritativeImpact(
	context.Context,
	publisher.PRImpactVerificationRequest,
) (pullrequest.AuthoritativeImpactEvaluation, error) {
	stub.calls++
	return stub.evaluation, stub.err
}
