package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
)

var _ = Describe("SpecOutput", func() {
	Describe("Validate", func() {
		It("passes for valid spec output", func() {
			s := &adapter.SpecOutput{
				SpecMarkdown: "# Spec\nContent here",
			}
			Expect(s.Validate()).To(Succeed())
		})

		It("rejects empty spec_markdown", func() {
			s := &adapter.SpecOutput{}
			Expect(s.Validate()).To(MatchError(ContainSubstring("spec_markdown")))
		})
	})
})

var _ = Describe("PlanOutput", func() {
	Describe("Validate", func() {
		It("passes for valid plan output", func() {
			p := &adapter.PlanOutput{
				PlanMarkdown: "# Plan\nContent",
				Phases: []adapter.Phase{
					{Name: "Phase 1", Tasks: []adapter.Task{{Description: "Do thing"}}},
				},
			}
			Expect(p.Validate()).To(Succeed())
		})

		It("rejects empty plan_markdown", func() {
			p := &adapter.PlanOutput{
				Phases: []adapter.Phase{{Name: "P1"}},
			}
			Expect(p.Validate()).To(MatchError(ContainSubstring("plan_markdown")))
		})

		It("rejects empty phases", func() {
			p := &adapter.PlanOutput{
				PlanMarkdown: "# Plan",
			}
			Expect(p.Validate()).To(MatchError(ContainSubstring("phases")))
		})
	})
})
