package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/schema"
)

func TestReviewContractStrictlyValidatesReviewJSON(t *testing.T) {
	valid := validReviewDocument()
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}

	result, err := validateFiles(t, "review/v1", map[string][]byte{"review.json": encoded}, emptyValidationContext(t))
	if err != nil {
		t.Fatalf("valid review validation error = %v", err)
	}
	if len(result.IntrinsicMetadata) != 0 {
		t.Fatalf("review intrinsic metadata = %s, want empty", result.IntrinsicMetadata)
	}

	valid.Score.Pass = true
	valid.Score.Value = 1
	encoded, err = json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal invalid review: %v", err)
	}
	if _, err := validateFiles(t, "review/v1", map[string][]byte{"review.json": encoded}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "pass") {
		t.Fatalf("below-threshold passing review error = %v, want strict pass error", err)
	}
}

func TestReviewContractRejectsUnknownAndTrailingJSON(t *testing.T) {
	encoded, err := json.Marshal(validReviewDocument())
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	withUnknown := append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
	for name, document := range map[string][]byte{
		"unknown field": withUnknown,
		"trailing JSON": append(encoded, []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateFiles(t, "review/v1", map[string][]byte{"review.json": document}, emptyValidationContext(t)); err == nil {
				t.Fatal("validation succeeded, want strict JSON error")
			}
		})
	}
}

func TestReviewContractUsesBoundedAnchoredRegularFileRead(t *testing.T) {
	valid, err := json.Marshal(validReviewDocument())
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}

	t.Run("rejects symlink", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, valid, 0644); err != nil {
			t.Fatalf("write outside document: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "review.json")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := validateDirectory(t, "review/v1", dir, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("symlink validation error = %v, want regular-file error", err)
		}
	})

	t.Run("rejects oversized document", func(t *testing.T) {
		oversized := []byte(`{"schema_version":"1.0.0","padding":"` + strings.Repeat("x", 1<<20) + `"}`)
		if _, err := validateFiles(t, "review/v1", map[string][]byte{"review.json": oversized}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("oversized validation error = %v, want size error", err)
		}
	})
}

func validReviewDocument() schema.ReviewOutput {
	return schema.ReviewOutput{
		SchemaVersion: "1.0.0",
		Metadata: schema.Metadata{
			Repo: "repo", Commit: "abc123", Branch: "main",
			Timestamp: "2026-07-22T12:00:00Z", DurationSec: 1,
			AgentCLI: "codex", AgentModel: "gpt", FilesReviewed: 1,
			TestsGenerated: 1, TestsFailing: 0,
		},
		Score: schema.Score{
			Value: 9, Max: 10, Pass: true, Threshold: 7,
			Deductions: []schema.ScoreDeduction{{IssueID: "issue-1", Severity: schema.SeverityLow, Points: -1}},
		},
		ProvenIssues: []schema.ProvenIssue{{
			ID: "issue-1", Severity: schema.SeverityLow, Title: "issue",
			File: "src/main.go", Line: 1, TestFile: "src/main_test.go",
			TestName: "TestMain", Category: schema.CategoryCorrectness,
		}},
		Observations: []schema.Observation{{
			ID: "observation-1", Title: "observation", File: "README.md",
			Line: 1, Category: schema.CategoryMaintainability,
		}},
		TestSummary: schema.TestSummary{TotalGenerated: 1, Passing: 1},
		Summary:     "reviewed",
	}
}
