package scoring_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/scoring"
)

var _ = Describe("ComputeQAScore", func() {
	It("returns 10.0 when all covered", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Text: "Req 1", Status: schema.CoverageCovered, CoveragePoints: 1.0},
			{ID: "R2", Text: "Req 2", Status: schema.CoverageCovered, CoveragePoints: 1.0},
		}
		score := scoring.ComputeQAScore(results, 7.0)
		Expect(score.Value).To(BeNumerically("~", 10.0, 0.01))
		Expect(score.Pass).To(BeTrue())
	})

	It("returns 0.0 when all uncovered_broken", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Text: "Req 1", Status: schema.CoverageUncoveredBroken, CoveragePoints: 0.0},
			{ID: "R2", Text: "Req 2", Status: schema.CoverageUncoveredBroken, CoveragePoints: 0.0},
		}
		score := scoring.ComputeQAScore(results, 7.0)
		Expect(score.Value).To(BeNumerically("~", 0.0, 0.01))
		Expect(score.Pass).To(BeFalse())
	})

	It("computes mixed coverage correctly", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Status: schema.CoverageCovered, CoveragePoints: 1.0},
			{ID: "R2", Status: schema.CoveragePartial, CoveragePoints: 0.5},
			{ID: "R3", Status: schema.CoverageUncoveredImplemented, CoveragePoints: 0.75},
			{ID: "R4", Status: schema.CoverageUncoveredBroken, CoveragePoints: 0.0},
		}
		// (1.0 + 0.5 + 0.75 + 0.0) / 4 * 10 = 5.625
		score := scoring.ComputeQAScore(results, 7.0)
		Expect(score.Value).To(BeNumerically("~", 5.625, 0.01))
		Expect(score.Pass).To(BeFalse())
	})

	It("pass/fail based on threshold", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Status: schema.CoverageCovered, CoveragePoints: 1.0},
			{ID: "R2", Status: schema.CoverageCovered, CoveragePoints: 1.0},
		}
		score := scoring.ComputeQAScore(results, 10.0)
		Expect(score.Pass).To(BeTrue())

		score2 := scoring.ComputeQAScore(results, 10.1)
		Expect(score2.Pass).To(BeFalse())
	})

	It("handles empty results", func() {
		score := scoring.ComputeQAScore(nil, 7.0)
		Expect(score.Value).To(BeNumerically("~", 0.0, 0.01))
		Expect(score.Pass).To(BeFalse())
	})
})

var _ = Describe("ExtractGaps", func() {
	It("returns gaps for uncovered_broken and failing", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Text: "OK", Status: schema.CoverageCovered},
			{ID: "R2", Text: "Broken", Status: schema.CoverageUncoveredBroken},
			{ID: "R3", Text: "Failing", Status: schema.CoverageFailing},
			{ID: "R4", Text: "Partial", Status: schema.CoveragePartial},
		}
		gaps := scoring.ExtractGaps(results)
		Expect(gaps).To(HaveLen(2))
		Expect(gaps[0].RequirementID).To(Equal("R2"))
		Expect(gaps[1].RequirementID).To(Equal("R3"))
	})

	It("returns empty for all covered", func() {
		results := []schema.RequirementResult{
			{ID: "R1", Status: schema.CoverageCovered},
		}
		gaps := scoring.ExtractGaps(results)
		Expect(gaps).To(BeEmpty())
	})
})
