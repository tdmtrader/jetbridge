package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func validPublishImpactBody() contracts.PublishImpactBody {
	return contracts.PublishImpactBody{
		BaselineDigest:  "sha256:" + strings.Repeat("a", 64),
		CandidateDigest: "sha256:" + strings.Repeat("b", 64),
		ChangedFiles: []contracts.PublishChangedFile{{
			Path: "main.go", AddedLines: 2, RemovedLines: 1,
		}},
		ChangedLines: 3,
		RuleResults: []contracts.PublishImpactRule{{
			ID: "policy-1", Passed: true, Reason: "No reapproval policy matched.",
		}},
	}
}

func TestPublishImpactRejectsAssessmentThatWaivesFailedDeterministicRule(t *testing.T) {
	body := validPublishImpactBody()
	body.RuleResults[0].Passed = false
	body.RuleResults[0].Reason = "Reapproval is mandatory."
	body.AgentAssessment = &contracts.AgentImpactAssessment{
		ReapprovalRequired: false,
		Rationale:          "The failed deterministic rule is harmless.",
	}
	body.ReapprovalRequired = false
	if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), "failed deterministic") {
		t.Fatalf("Validate() error = %v, want failed deterministic rule rejection", err)
	}
}
