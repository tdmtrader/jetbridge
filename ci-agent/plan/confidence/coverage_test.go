package confidence_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/confidence"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ScoreCoverage", func() {
	It("returns 0.8 when no acceptance criteria in input", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
		}
		spec := &adapter.SpecOutput{SpecMarkdown: "Some spec content"}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically("~", 0.8, 0.01))
	})

	It("returns 1.0 when spec addresses all acceptance criteria", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			AcceptanceCriteria: []string{
				"Users can authenticate with password",
				"Tokens expire after hours",
			},
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Users will authenticate using password credentials. Tokens expire after 24 hours.",
		}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically("~", 1.0, 0.01))
	})

	It("returns 0.5 when spec addresses half of acceptance criteria", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			AcceptanceCriteria: []string{
				"Users can authenticate with password",
				"Rate limiting is enforced",
			},
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Users will authenticate using password credentials.",
		}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically("~", 0.5, 0.01))
	})

	It("applies -0.1 penalty for 1-2 unresolved questions", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			AcceptanceCriteria: []string{
				"Users can authenticate with password",
			},
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown:        "Users will authenticate using password credentials.",
			UnresolvedQuestions: []string{"What auth provider?"},
		}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically("~", 0.9, 0.01))
	})

	It("applies -0.2 penalty for 3+ unresolved questions", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			AcceptanceCriteria: []string{
				"Users can authenticate with password",
			},
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Users will authenticate using password credentials.",
			UnresolvedQuestions: []string{
				"What auth provider?",
				"What token format?",
				"What expiry policy?",
			},
		}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically("~", 0.8, 0.01))
	})

	It("clamps score to [0.0, 1.0]", func() {
		input := &schema.PlanningInput{
			Title:              "Task",
			Description:        "Desc",
			AcceptanceCriteria: []string{"Something very specific and unique"},
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Totally unrelated content.",
			UnresolvedQuestions: []string{
				"Q1", "Q2", "Q3",
			},
		}
		report := confidence.ScoreCoverage(input, spec)
		Expect(report.Score).To(BeNumerically(">=", 0.0))
		Expect(report.Score).To(BeNumerically("<=", 1.0))
	})
})
