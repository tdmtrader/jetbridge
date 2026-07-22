package schema_test

import (
	"encoding/json"
	"math"
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

func TestReviewOutputValidateRemainsCompatibilityOriented(t *testing.T) {
	r := validReview()
	r.SchemaVersion = "0.9.0"
	r.Score.Value = math.NaN()
	r.ProvenIssues[0].Severity = schema.Severity("legacy-custom")

	requireNoErr(t, r.Validate())
}

func TestReviewOutputValidateSnapshotV1(t *testing.T) {
	t.Run("accepts a valid strict snapshot review", func(t *testing.T) {
		r := validReview()
		requireNoErr(t, r.ValidateSnapshotV1())
	})

	t.Run("allows a failed review above threshold for critical-issue policy", func(t *testing.T) {
		r := validReview()
		r.Score.Pass = false
		requireNoErr(t, r.ValidateSnapshotV1())
	})

	t.Run("allows either sign for finite deductions", func(t *testing.T) {
		r := validReview()
		r.Score.Deductions[0].Points = 1.5
		requireNoErr(t, r.ValidateSnapshotV1())
	})

	t.Run("rejects invalid top-level and score fields", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*schema.ReviewOutput)
			want  string
		}{
			{"wrong schema version", func(r *schema.ReviewOutput) { r.SchemaVersion = "1.0.1" }, "1.0.0"},
			{"blank summary", func(r *schema.ReviewOutput) { r.Summary = " \t" }, "summary"},
			{"non-finite value", func(r *schema.ReviewOutput) { r.Score.Value = math.NaN() }, "value"},
			{"non-finite max", func(r *schema.ReviewOutput) { r.Score.Max = math.Inf(1) }, "max"},
			{"non-positive max", func(r *schema.ReviewOutput) { r.Score.Max = 0 }, "max"},
			{"value below zero", func(r *schema.ReviewOutput) { r.Score.Value = -0.1 }, "value"},
			{"value above max", func(r *schema.ReviewOutput) { r.Score.Value = 10.1 }, "value"},
			{"threshold below zero", func(r *schema.ReviewOutput) { r.Score.Threshold = -0.1 }, "threshold"},
			{"threshold above max", func(r *schema.ReviewOutput) { r.Score.Threshold = 10.1 }, "threshold"},
			{"pass below threshold", func(r *schema.ReviewOutput) { r.Score.Value = 6.9 }, "pass"},
			{"non-finite deduction", func(r *schema.ReviewOutput) { r.Score.Deductions[0].Points = math.Inf(-1) }, "points"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := validReview()
				tc.setup(&r)
				requireErrContains(t, r.ValidateSnapshotV1(), tc.want)
			})
		}
	})

	t.Run("validates metadata and test totals", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*schema.ReviewOutput)
			want  string
		}{
			{"blank repo", func(r *schema.ReviewOutput) { r.Metadata.Repo = " " }, "repo"},
			{"invalid timestamp", func(r *schema.ReviewOutput) { r.Metadata.Timestamp = "yesterday" }, "timestamp"},
			{"negative duration", func(r *schema.ReviewOutput) { r.Metadata.DurationSec = -1 }, "duration"},
			{"negative test total", func(r *schema.ReviewOutput) { r.TestSummary.Passing = -1 }, "passing"},
			{"inconsistent summary", func(r *schema.ReviewOutput) { r.TestSummary.TotalGenerated++ }, "total_generated"},
			{"metadata total mismatch", func(r *schema.ReviewOutput) { r.Metadata.TestsGenerated++ }, "tests_generated"},
			{"metadata failure mismatch", func(r *schema.ReviewOutput) { r.Metadata.TestsFailing++ }, "tests_failing"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := validReview()
				tc.setup(&r)
				requireErrContains(t, r.ValidateSnapshotV1(), tc.want)
			})
		}
	})

	t.Run("validates nested findings and globally unique IDs", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*schema.ReviewOutput)
			want  string
		}{
			{"invalid issue severity", func(r *schema.ReviewOutput) { r.ProvenIssues[0].Severity = "urgent" }, "severity"},
			{"invalid issue category", func(r *schema.ReviewOutput) { r.ProvenIssues[0].Category = "style" }, "category"},
			{"unsafe issue path", func(r *schema.ReviewOutput) { r.ProvenIssues[0].File = "../secret" }, "file"},
			{"noncanonical issue path", func(r *schema.ReviewOutput) { r.ProvenIssues[0].File = "src//main.go" }, "file"},
			{"unsafe test path", func(r *schema.ReviewOutput) { r.ProvenIssues[0].TestFile = "/tmp/test.go" }, "test_file"},
			{"negative line", func(r *schema.ReviewOutput) { r.ProvenIssues[0].Line = -1 }, "line"},
			{"end before start", func(r *schema.ReviewOutput) { r.ProvenIssues[0].EndLine = 41 }, "end_line"},
			{"invalid observation category", func(r *schema.ReviewOutput) { r.Observations[0].Category = "style" }, "category"},
			{"duplicate issue IDs", func(r *schema.ReviewOutput) { r.Observations[0].ID = "001" }, "duplicate"},
			{"unknown deduction reference", func(r *schema.ReviewOutput) { r.Score.Deductions[0].IssueID = "missing" }, "issue_id"},
			{"deduction severity mismatch", func(r *schema.ReviewOutput) { r.Score.Deductions[0].Severity = schema.SeverityLow }, "severity"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := validReview()
				tc.setup(&r)
				requireErrContains(t, r.ValidateSnapshotV1(), tc.want)
			})
		}
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

func TestGateAndJudgeCategoriesAreValid(t *testing.T) {
	if err := schema.CategoryGate.Validate(); err != nil {
		t.Fatalf("gate category: %v", err)
	}
	if err := schema.CategoryJudge.Validate(); err != nil {
		t.Fatalf("judge category: %v", err)
	}
}
