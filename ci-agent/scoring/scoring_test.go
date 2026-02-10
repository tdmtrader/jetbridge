package scoring_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/scoring"
)

var _ = Describe("Scoring", func() {
	var weights config.SeverityWeights

	BeforeEach(func() {
		weights = config.DefaultConfig().SeverityWeights
	})

	Describe("ComputeScore", func() {
		It("returns 10.0 for zero proven issues", func() {
			score := scoring.ComputeScore(nil, weights)
			Expect(score.Value).To(Equal(10.0))
			Expect(score.Max).To(Equal(10.0))
			Expect(score.Deductions).To(BeEmpty())
		})

		It("deducts 3.0 for one critical issue", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
			}
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(7.0))
			Expect(score.Deductions).To(HaveLen(1))
			Expect(score.Deductions[0].Points).To(Equal(3.0))
			Expect(score.Deductions[0].Severity).To(Equal(schema.SeverityCritical))
			Expect(score.Deductions[0].IssueID).To(Equal("ISS-1"))
		})

		It("deducts 1.5 for one high issue", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityHigh, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
			}
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(8.5))
		})

		It("deducts 1.0 for one medium issue", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityMedium, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
			}
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(9.0))
		})

		It("deducts 0.5 for one low issue", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityLow, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
			}
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(9.5))
		})

		It("additively deducts for multiple issues", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
				{ID: "ISS-2", Severity: schema.SeverityHigh, Title: "t", File: "f.go", Line: 2, TestFile: "f_test.go", TestName: "TestY"},
				{ID: "ISS-3", Severity: schema.SeverityLow, Title: "t", File: "f.go", Line: 3, TestFile: "f_test.go", TestName: "TestZ"},
			}
			// 10 - 3.0 - 1.5 - 0.5 = 5.0
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(5.0))
			Expect(score.Deductions).To(HaveLen(3))
		})

		It("floors score at 0.0", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "T1"},
				{ID: "ISS-2", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 2, TestFile: "f_test.go", TestName: "T2"},
				{ID: "ISS-3", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 3, TestFile: "f_test.go", TestName: "T3"},
				{ID: "ISS-4", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 4, TestFile: "f_test.go", TestName: "T4"},
			}
			// 10 - 3*4 = -2 → floored to 0.0
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Value).To(Equal(0.0))
		})

		It("uses custom severity weights", func() {
			customWeights := config.SeverityWeights{
				Critical: 5.0,
				High:     3.0,
				Medium:   2.0,
				Low:      1.0,
			}
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityCritical, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "TestX"},
			}
			score := scoring.ComputeScore(issues, customWeights)
			Expect(score.Value).To(Equal(5.0))
			Expect(score.Deductions[0].Points).To(Equal(5.0))
		})

		It("includes deduction for each proven issue", func() {
			issues := []schema.ProvenIssue{
				{ID: "ISS-1", Severity: schema.SeverityHigh, Title: "t", File: "f.go", Line: 1, TestFile: "f_test.go", TestName: "T1"},
				{ID: "ISS-2", Severity: schema.SeverityMedium, Title: "t", File: "f.go", Line: 2, TestFile: "f_test.go", TestName: "T2"},
			}
			score := scoring.ComputeScore(issues, weights)
			Expect(score.Deductions).To(HaveLen(2))
			Expect(score.Deductions[0].IssueID).To(Equal("ISS-1"))
			Expect(score.Deductions[1].IssueID).To(Equal("ISS-2"))
		})
	})

	Describe("EvaluatePass", func() {
		It("passes when score >= threshold", func() {
			s := schema.Score{Value: 8.0, Max: 10.0, Threshold: 7.0}
			Expect(scoring.EvaluatePass(s, 7.0, false)).To(BeTrue())
		})

		It("passes when score equals threshold", func() {
			s := schema.Score{Value: 7.0, Max: 10.0, Threshold: 7.0}
			Expect(scoring.EvaluatePass(s, 7.0, false)).To(BeTrue())
		})

		It("fails when score < threshold", func() {
			s := schema.Score{Value: 6.9, Max: 10.0, Threshold: 7.0}
			Expect(scoring.EvaluatePass(s, 7.0, false)).To(BeFalse())
		})

		It("fails on critical when failOnCritical is true", func() {
			s := schema.Score{
				Value: 9.0, Max: 10.0, Threshold: 7.0,
				Deductions: []schema.ScoreDeduction{
					{IssueID: "ISS-1", Severity: schema.SeverityCritical, Points: 3.0},
				},
			}
			Expect(scoring.EvaluatePass(s, 7.0, true)).To(BeFalse())
		})

		It("passes with critical when failOnCritical is false", func() {
			s := schema.Score{
				Value: 9.0, Max: 10.0, Threshold: 7.0,
				Deductions: []schema.ScoreDeduction{
					{IssueID: "ISS-1", Severity: schema.SeverityCritical, Points: 1.0},
				},
			}
			Expect(scoring.EvaluatePass(s, 7.0, false)).To(BeTrue())
		})
	})
})
