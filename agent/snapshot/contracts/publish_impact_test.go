package contracts_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
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

func TestPublishImpactRejectsOversizedCollectionsAndPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*contracts.PublishImpactBody)
		want  string
	}{
		{"changed files", func(body *contracts.PublishImpactBody) {
			body.ChangedFiles = nil
			body.ChangedLines = 0
			for index := 0; index < 1025; index++ {
				body.ChangedFiles = append(body.ChangedFiles, contracts.PublishChangedFile{Path: fmt.Sprintf("file-%04d", index)})
			}
		}, "changed files"},
		{"rules", func(body *contracts.PublishImpactBody) {
			body.RuleResults = nil
			for index := 0; index < 513; index++ {
				body.RuleResults = append(body.RuleResults, contracts.PublishImpactRule{ID: fmt.Sprintf("rule-%03d", index), Passed: true, Reason: "No deterministic policy matched."})
			}
		}, "rule results"},
		{"validation changes", func(body *contracts.PublishImpactBody) {
			body.ValidationChanges = nil
			for index := 0; index < 513; index++ {
				body.ValidationChanges = append(body.ValidationChanges, fmt.Sprintf("change-%03d", index))
			}
		}, "validation changes"},
		{"reasons", func(body *contracts.PublishImpactBody) {
			body.ReapprovalRequired = true
			body.Reasons = nil
			for index := 0; index < 513; index++ {
				body.Reasons = append(body.Reasons, fmt.Sprintf("reason-%03d", index))
			}
		}, "reasons"},
		{"path", func(body *contracts.PublishImpactBody) { body.ChangedFiles[0].Path = strings.Repeat("a", 1025) }, "changed file path"},
		{"rule id", func(body *contracts.PublishImpactBody) { body.RuleResults[0].ID = strings.Repeat("a", 257) }, "rule id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validPublishImpactBody()
			tc.setup(&body)
			if err := body.Validate(nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPublishImpactRev2RetainsPreBoundRulesWhileCurrentRejectsThem(t *testing.T) {
	body := validPublishImpactBody()
	body.RuleResults = nil
	for index := 0; index < 513; index++ {
		body.RuleResults = append(body.RuleResults, contracts.PublishImpactRule{ID: fmt.Sprintf("rule-%03d", index), Passed: true, Reason: "No deterministic policy matched."})
	}
	if err := body.Validate(nil); err == nil {
		t.Fatal("current validation accepted over-cap rules")
	}
	ref := snapshot.TypeRef("publish-impact/v1")
	record, err := contracts.NewRecord(ref, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	rev2, _ := contracts.SchemaDigestForRevision(ref, 2)
	record.Schema = rev2
	if _, err := revalidateSealedFiles(t, "publish-impact/v1", map[string][]byte{"record.json": marshalRecord(t, record)}, emptyValidationContext(t)); err != nil {
		t.Fatalf("rev2 read rejected legacy rules: %v", err)
	}
}
