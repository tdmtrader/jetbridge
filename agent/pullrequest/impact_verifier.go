package pullrequest

import (
	"context"
	"fmt"
	"reflect"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// ImpactPolicyResolver resolves a deployment-owned policy identity. Workflow
// authors may name the identity but cannot supply or weaken its rules.
type ImpactPolicyResolver interface {
	ResolveImpactPolicy(context.Context, int, string) (ImpactPolicy, bool, error)
}

// AuthoritativeImpactEvaluation contains the independently derived facts used
// to make the impact decision. AgentAssessment is authority returned by the
// server-owned evaluator; the assessment embedded in the sealed impact body is
// only an assertion to compare against this result.
type AuthoritativeImpactEvaluation struct {
	Deterministic   DeterministicImpact
	AgentAssessment *contracts.AgentImpactAssessment
}

// AuthoritativeImpactEvaluator reopens every exact snapshot carried by the
// request, verifies its provenance and binding/action ownership, and derives
// both the deterministic delta and agent-assessment status from those
// immutable inputs. An implementation must never copy any fact, including the
// assessment, from the untrusted request Body.
type AuthoritativeImpactEvaluator interface {
	EvaluateAuthoritativeImpact(
		context.Context,
		publisher.PRImpactVerificationRequest,
	) (AuthoritativeImpactEvaluation, error)
}

type exactImpactVerifier struct {
	policies  ImpactPolicyResolver
	evaluator AuthoritativeImpactEvaluator
}

func NewImpactVerifier(
	policies ImpactPolicyResolver,
	evaluator AuthoritativeImpactEvaluator,
) (publisher.PRImpactVerifier, error) {
	if policies == nil || evaluator == nil {
		return nil, fmt.Errorf("pullrequest: impact policy resolver and evaluator are required")
	}
	return &exactImpactVerifier{policies: policies, evaluator: evaluator}, nil
}

func (verifier *exactImpactVerifier) VerifyPRImpact(
	ctx context.Context,
	request publisher.PRImpactVerificationRequest,
) (contracts.PublishImpactBody, error) {
	if verifier == nil || verifier.policies == nil || verifier.evaluator == nil || ctx == nil {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: impact verifier and context are required")
	}
	if err := ctx.Err(); err != nil {
		return contracts.PublishImpactBody{}, err
	}
	if err := request.Validate(); err != nil {
		return contracts.PublishImpactBody{}, err
	}
	policy, found, err := verifier.policies.ResolveImpactPolicy(
		ctx, request.TeamID, request.PolicyVersion,
	)
	if err != nil {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: resolve impact policy: %w", err)
	}
	if !found {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: exact impact policy was not found")
	}
	evaluation, err := verifier.evaluator.EvaluateAuthoritativeImpact(ctx, request)
	if err != nil {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: derive authoritative impact: %w", err)
	}
	deterministic := evaluation.Deterministic
	if deterministic.BaselineDigest != request.Baseline.Digest.String() ||
		deterministic.CandidateDigest != request.Candidate.Digest.String() {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: deterministic impact does not bind exact inputs")
	}
	expected, err := DecideImpact(policy, deterministic, evaluation.AgentAssessment)
	if err != nil {
		return contracts.PublishImpactBody{}, err
	}
	if !reflect.DeepEqual(expected, request.Body) {
		return contracts.PublishImpactBody{}, fmt.Errorf("pullrequest: sealed impact does not match the server-derived decision")
	}
	return expected, nil
}

var _ publisher.PRImpactVerifier = (*exactImpactVerifier)(nil)
