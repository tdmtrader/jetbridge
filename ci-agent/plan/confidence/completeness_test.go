package confidence_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/confidence"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Completeness", func() {
	Describe("ScoreCompleteness", func() {
		It("scores title + description only at 0.3", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Short description",
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.3, 0.01))
		})

		It("adds 0.05 for type", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Type:        schema.StoryFeature,
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.35, 0.01))
		})

		It("adds 0.05 for priority", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Priority:    schema.PriorityHigh,
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.35, 0.01))
		})

		It("adds 0.05 for non-empty labels", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Labels:      []string{"api"},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.35, 0.01))
		})

		It("adds 0.2 for non-empty acceptance criteria", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				AcceptanceCriteria: []string{
					"Users can log in",
					"JWT tokens expire",
				},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.5, 0.01))
		})

		It("adds 0.05 for context.repo", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Context: &schema.PlanningContext{
					Repo: "https://github.com/org/repo",
				},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.35, 0.01))
		})

		It("adds 0.05 for context.language", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Context: &schema.PlanningContext{
					Language: "go",
				},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.35, 0.01))
		})

		It("adds 0.15 for non-empty context.related_files", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Context: &schema.PlanningContext{
					RelatedFiles: []string{"main.go"},
				},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.45, 0.01))
		})

		It("adds 0.1 for description > 200 chars", func() {
			longDesc := ""
			for i := 0; i < 210; i++ {
				longDesc += "x"
			}
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: longDesc,
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("~", 0.4, 0.01))
		})

		It("caps score at 1.0 for fully populated input", func() {
			longDesc := ""
			for i := 0; i < 210; i++ {
				longDesc += "x"
			}
			input := &schema.PlanningInput{
				Title:       "Full feature",
				Description: longDesc,
				Type:        schema.StoryFeature,
				Priority:    schema.PriorityHigh,
				Labels:      []string{"api", "security"},
				AcceptanceCriteria: []string{
					"Criterion 1",
					"Criterion 2",
				},
				Context: &schema.PlanningContext{
					Repo:         "https://github.com/org/repo",
					Language:     "go",
					RelatedFiles: []string{"main.go", "handler.go"},
				},
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Score).To(BeNumerically("<=", 1.0))
			Expect(report.Score).To(BeNumerically("~", 1.0, 0.01))
		})

		It("returns breakdown of contributing fields", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Type:        schema.StoryFeature,
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Breakdown).To(HaveKey("base"))
			Expect(report.Breakdown).To(HaveKey("type"))
			Expect(report.Breakdown["base"]).To(BeNumerically("~", 0.3, 0.01))
			Expect(report.Breakdown["type"]).To(BeNumerically("~", 0.05, 0.01))
		})

		It("returns list of missing fields", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Missing).To(ContainElement("type"))
			Expect(report.Missing).To(ContainElement("priority"))
			Expect(report.Missing).To(ContainElement("labels"))
			Expect(report.Missing).To(ContainElement("acceptance_criteria"))
			Expect(report.Missing).To(ContainElement("context.repo"))
			Expect(report.Missing).To(ContainElement("context.language"))
			Expect(report.Missing).To(ContainElement("context.related_files"))
		})

		It("does not list present fields as missing", func() {
			input := &schema.PlanningInput{
				Title:       "A task",
				Description: "Description",
				Type:        schema.StoryFeature,
				Priority:    schema.PriorityHigh,
			}
			report := confidence.ScoreCompleteness(input)
			Expect(report.Missing).NotTo(ContainElement("type"))
			Expect(report.Missing).NotTo(ContainElement("priority"))
		})
	})
})
