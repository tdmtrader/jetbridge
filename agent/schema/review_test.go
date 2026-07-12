package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/schema"
)

func validReview() schema.ReviewOutput {
	return schema.ReviewOutput{
		SchemaVersion: "1.0.0",
		Metadata: schema.Metadata{
			Repo:           "github.com/org/repo",
			Commit:         "abc123",
			Branch:         "main",
			Timestamp:      "2026-02-09T17:00:00Z",
			DurationSec:    45,
			AgentCLI:       "claude-code",
			AgentModel:     "claude-opus-4-6",
			FilesReviewed:  24,
			TestsGenerated: 8,
			TestsFailing:   3,
		},
		Score: schema.Score{
			Value:     7.5,
			Max:       10.0,
			Pass:      true,
			Threshold: 7.0,
			Deductions: []schema.ScoreDeduction{
				{IssueID: "001", Severity: schema.SeverityHigh, Points: -1.5},
			},
		},
		ProvenIssues: []schema.ProvenIssue{
			{
				ID:          "001",
				Severity:    schema.SeverityHigh,
				Title:       "Nil pointer on empty config",
				Description: "LoadConfig returns nil without error.",
				File:        "config/loader.go",
				Line:        42,
				EndLine:     48,
				TestFile:    "review/tests/001_test.go",
				TestName:    "TestLoadConfig_EmptyFile",
				TestOutput:  "panic: nil pointer",
				Category:    schema.CategoryCorrectness,
			},
		},
		Observations: []schema.Observation{
			{
				ID:          "OBS-001",
				Title:       "High cyclomatic complexity",
				Description: "Function has 15 branches.",
				File:        "service/orders.go",
				Line:        88,
				Category:    schema.CategoryMaintainability,
			},
		},
		TestSummary: schema.TestSummary{
			TotalGenerated: 8,
			Passing:        5,
			Failing:        3,
			Error:          0,
		},
		Summary: "24 files reviewed. Score: 7.5/10.",
	}
}

func TestReviewOutputJSONRoundTrip(t *testing.T) {
	t.Run("marshals and unmarshals a full ReviewOutput", func(t *testing.T) {
		original := validReview()

		data, err := json.Marshal(original)
		requireNoErr(t, err)

		var decoded schema.ReviewOutput
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.SchemaVersion, "1.0.0")
		requireEqual(t, decoded.Metadata.Repo, "github.com/org/repo")
		requireEqual(t, decoded.Score.Value, 7.5)
		requireLen(t, decoded.ProvenIssues, 1)
		requireLen(t, decoded.Observations, 1)
		requireEqual(t, decoded.TestSummary.TotalGenerated, 8)
	})

	t.Run("uses correct JSON field names", func(t *testing.T) {
		r := validReview()
		data, err := json.Marshal(r)
		requireNoErr(t, err)

		var raw map[string]interface{}
		requireNoErr(t, json.Unmarshal(data, &raw))

		for _, key := range []string{
			"schema_version", "metadata", "score", "proven_issues",
			"observations", "test_summary", "summary",
		} {
			requireHasKey(t, raw, key)
		}
	})
}

func TestReviewOutputValidate(t *testing.T) {
	t.Run("accepts a valid ReviewOutput", func(t *testing.T) {
		r := validReview()
		requireNoErr(t, r.Validate())
	})

	t.Run("rejects empty schema_version", func(t *testing.T) {
		r := validReview()
		r.SchemaVersion = ""
		requireErrContains(t, r.Validate(), "schema_version")
	})

	t.Run("rejects empty summary", func(t *testing.T) {
		r := validReview()
		r.Summary = ""
		requireErrContains(t, r.Validate(), "summary")
	})
}

func TestProvenIssueValidate(t *testing.T) {
	t.Run("requires id, severity, title, file, line, test_file, test_name", func(t *testing.T) {
		issue := schema.ProvenIssue{}
		err := issue.Validate()
		requireErr(t, err)
		requireContains(t, err.Error(), "id")
	})

	t.Run("validates all required fields", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*schema.ProvenIssue)
			field string
		}{
			{"missing id", func(p *schema.ProvenIssue) { p.ID = "" }, "id"},
			{"missing severity", func(p *schema.ProvenIssue) { p.Severity = "" }, "severity"},
			{"missing title", func(p *schema.ProvenIssue) { p.Title = "" }, "title"},
			{"missing file", func(p *schema.ProvenIssue) { p.File = "" }, "file"},
			{"missing line", func(p *schema.ProvenIssue) { p.Line = 0 }, "line"},
			{"missing test_file", func(p *schema.ProvenIssue) { p.TestFile = "" }, "test_file"},
			{"missing test_name", func(p *schema.ProvenIssue) { p.TestName = "" }, "test_name"},
		} {
			issue := schema.ProvenIssue{
				ID:       "001",
				Severity: schema.SeverityHigh,
				Title:    "Test issue",
				File:     "main.go",
				Line:     10,
				TestFile: "test.go",
				TestName: "TestFoo",
				Category: schema.CategoryCorrectness,
			}
			tc.setup(&issue)
			requireErrContains(t, issue.Validate(), tc.field, "case: %s", tc.name)
		}
	})
}

func TestObservationValidate(t *testing.T) {
	t.Run("requires id, title, file, line, category", func(t *testing.T) {
		obs := schema.Observation{}
		requireErr(t, obs.Validate())
	})

	t.Run("validates all required fields", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*schema.Observation)
			field string
		}{
			{"missing id", func(o *schema.Observation) { o.ID = "" }, "id"},
			{"missing title", func(o *schema.Observation) { o.Title = "" }, "title"},
			{"missing file", func(o *schema.Observation) { o.File = "" }, "file"},
			{"missing line", func(o *schema.Observation) { o.Line = 0 }, "line"},
			{"missing category", func(o *schema.Observation) { o.Category = "" }, "category"},
		} {
			obs := schema.Observation{
				ID:       "OBS-001",
				Title:    "Test observation",
				File:     "main.go",
				Line:     10,
				Category: schema.CategoryMaintainability,
			}
			tc.setup(&obs)
			requireErrContains(t, obs.Validate(), tc.field, "case: %s", tc.name)
		}
	})
}

func TestScorePassesThreshold(t *testing.T) {
	s := schema.Score{Value: 7.5, Max: 10.0, Threshold: 7.0}
	requireTrue(t, s.PassesThreshold())

	s.Value = 6.9
	requireFalse(t, s.PassesThreshold())

	s.Value = 7.0
	requireTrue(t, s.PassesThreshold())
}

func TestTestSummaryIsConsistent(t *testing.T) {
	ts := schema.TestSummary{TotalGenerated: 10, Passing: 5, Failing: 3, Error: 2}
	requireTrue(t, ts.IsConsistent())

	ts.Passing = 6
	requireFalse(t, ts.IsConsistent())
}

func TestSeverityValidate(t *testing.T) {
	t.Run("validates known values", func(t *testing.T) {
		for _, s := range []schema.Severity{
			schema.SeverityCritical,
			schema.SeverityHigh,
			schema.SeverityMedium,
			schema.SeverityLow,
		} {
			requireNoErr(t, s.Validate(), "severity %q should be valid", s)
		}
	})

	t.Run("rejects invalid severity", func(t *testing.T) {
		s := schema.Severity("extreme")
		requireErrContains(t, s.Validate(), "severity")
	})
}

func TestCategoryValidate(t *testing.T) {
	t.Run("validates known values", func(t *testing.T) {
		for _, c := range []schema.Category{
			schema.CategorySecurity,
			schema.CategoryCorrectness,
			schema.CategoryPerformance,
			schema.CategoryMaintainability,
			schema.CategoryTesting,
		} {
			requireNoErr(t, c.Validate(), "category %q should be valid", c)
		}
	})

	t.Run("rejects invalid category", func(t *testing.T) {
		c := schema.Category("aesthetics")
		requireErrContains(t, c.Validate(), "category")
	})
}
