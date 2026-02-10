package confidence_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/confidence"
)

var _ = Describe("ScoreActionability", func() {
	It("returns 0.0 for empty plan", func() {
		plan := &adapter.PlanOutput{PlanMarkdown: "empty"}
		report := confidence.ScoreActionability(plan)
		Expect(report.Score).To(BeNumerically("~", 0.0, 0.01))
	})

	It("returns base 0.6 for plan with phases and tasks", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{{Description: "task"}}},
			},
		}
		report := confidence.ScoreActionability(plan)
		Expect(report.Score).To(BeNumerically("~", 0.6, 0.01))
	})

	It("adds up to 0.3 for tasks with file references", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{
					{Description: "task1", Files: []string{"a.go"}},
					{Description: "task2", Files: []string{"b.go"}},
				}},
			},
		}
		report := confidence.ScoreActionability(plan)
		// base 0.6 + file ratio 1.0*0.3 + all deep 0.1 = 1.0
		Expect(report.Score).To(BeNumerically("~", 1.0, 0.01))
	})

	It("adds 0.1 for non-empty key files", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{{Description: "task"}}},
			},
			KeyFiles: []adapter.KeyFile{{Path: "main.go", Change: "NEW"}},
		}
		report := confidence.ScoreActionability(plan)
		Expect(report.Score).To(BeNumerically("~", 0.7, 0.01))
	})

	It("adds 0.1 when all phases have >= 2 tasks", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{
					{Description: "task1"},
					{Description: "task2"},
				}},
			},
		}
		report := confidence.ScoreActionability(plan)
		// base 0.6 + all deep 0.1 = 0.7
		Expect(report.Score).To(BeNumerically("~", 0.7, 0.01))
	})

	It("does not add depth bonus if any phase has < 2 tasks", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{
					{Description: "task1"},
					{Description: "task2"},
				}},
				{Name: "P2", Tasks: []adapter.Task{
					{Description: "task1"},
				}},
			},
		}
		report := confidence.ScoreActionability(plan)
		Expect(report.Score).To(BeNumerically("~", 0.6, 0.01))
	})

	It("caps score at 1.0", func() {
		plan := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases: []adapter.Phase{
				{Name: "P1", Tasks: []adapter.Task{
					{Description: "t1", Files: []string{"a.go"}},
					{Description: "t2", Files: []string{"b.go"}},
				}},
			},
			KeyFiles: []adapter.KeyFile{{Path: "a.go", Change: "NEW"}},
		}
		report := confidence.ScoreActionability(plan)
		Expect(report.Score).To(BeNumerically("<=", 1.0))
	})
})
