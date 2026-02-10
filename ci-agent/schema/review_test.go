package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ReviewOutput", func() {
	var validReview func() schema.ReviewOutput

	BeforeEach(func() {
		validReview = func() schema.ReviewOutput {
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
	})

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals a full ReviewOutput", func() {
			original := validReview()

			data, err := json.Marshal(original)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.ReviewOutput
			err = json.Unmarshal(data, &decoded)
			Expect(err).NotTo(HaveOccurred())

			Expect(decoded.SchemaVersion).To(Equal("1.0.0"))
			Expect(decoded.Metadata.Repo).To(Equal("github.com/org/repo"))
			Expect(decoded.Score.Value).To(Equal(7.5))
			Expect(decoded.ProvenIssues).To(HaveLen(1))
			Expect(decoded.Observations).To(HaveLen(1))
			Expect(decoded.TestSummary.TotalGenerated).To(Equal(8))
		})

		It("uses correct JSON field names", func() {
			r := validReview()
			data, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())

			var raw map[string]interface{}
			Expect(json.Unmarshal(data, &raw)).To(Succeed())

			Expect(raw).To(HaveKey("schema_version"))
			Expect(raw).To(HaveKey("metadata"))
			Expect(raw).To(HaveKey("score"))
			Expect(raw).To(HaveKey("proven_issues"))
			Expect(raw).To(HaveKey("observations"))
			Expect(raw).To(HaveKey("test_summary"))
			Expect(raw).To(HaveKey("summary"))
		})
	})

	Describe("Validate", func() {
		It("accepts a valid ReviewOutput", func() {
			r := validReview()
			Expect(r.Validate()).To(Succeed())
		})

		It("rejects empty schema_version", func() {
			r := validReview()
			r.SchemaVersion = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("schema_version")))
		})

		It("rejects empty summary", func() {
			r := validReview()
			r.Summary = ""
			Expect(r.Validate()).To(MatchError(ContainSubstring("summary")))
		})
	})
})

var _ = Describe("ProvenIssue", func() {
	It("requires id, severity, title, file, line, test_file, test_name", func() {
		issue := schema.ProvenIssue{}
		err := issue.Validate()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("id"))
	})

	It("validates all required fields", func() {
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
			Expect(issue.Validate()).To(MatchError(ContainSubstring(tc.field)), "case: %s", tc.name)
		}
	})
})

var _ = Describe("Observation", func() {
	It("requires id, title, file, line, category", func() {
		obs := schema.Observation{}
		err := obs.Validate()
		Expect(err).To(HaveOccurred())
	})

	It("validates all required fields", func() {
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
			Expect(obs.Validate()).To(MatchError(ContainSubstring(tc.field)), "case: %s", tc.name)
		}
	})
})

var _ = Describe("Score", func() {
	It("computes pass correctly from value vs threshold", func() {
		s := schema.Score{Value: 7.5, Max: 10.0, Threshold: 7.0}
		Expect(s.PassesThreshold()).To(BeTrue())

		s.Value = 6.9
		Expect(s.PassesThreshold()).To(BeFalse())

		s.Value = 7.0
		Expect(s.PassesThreshold()).To(BeTrue())
	})
})

var _ = Describe("TestSummary", func() {
	It("has consistent counts (total = passing + failing + error)", func() {
		ts := schema.TestSummary{TotalGenerated: 10, Passing: 5, Failing: 3, Error: 2}
		Expect(ts.IsConsistent()).To(BeTrue())

		ts.Passing = 6
		Expect(ts.IsConsistent()).To(BeFalse())
	})
})

var _ = Describe("Severity", func() {
	It("validates known values", func() {
		for _, s := range []schema.Severity{
			schema.SeverityCritical,
			schema.SeverityHigh,
			schema.SeverityMedium,
			schema.SeverityLow,
		} {
			Expect(s.Validate()).To(Succeed(), "severity %q should be valid", s)
		}
	})

	It("rejects invalid severity", func() {
		s := schema.Severity("extreme")
		Expect(s.Validate()).To(MatchError(ContainSubstring("severity")))
	})
})

var _ = Describe("Category", func() {
	It("validates known values", func() {
		for _, c := range []schema.Category{
			schema.CategorySecurity,
			schema.CategoryCorrectness,
			schema.CategoryPerformance,
			schema.CategoryMaintainability,
			schema.CategoryTesting,
		} {
			Expect(c.Validate()).To(Succeed(), "category %q should be valid", c)
		}
	})

	It("rejects invalid category", func() {
		c := schema.Category("aesthetics")
		Expect(c.Validate()).To(MatchError(ContainSubstring("category")))
	})
})
