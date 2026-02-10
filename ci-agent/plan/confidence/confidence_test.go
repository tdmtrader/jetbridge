package confidence_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/confidence"
)

var _ = Describe("ComputeConfidence", func() {
	defaults := confidence.DefaultWeights()

	It("returns 1.0 when all sub-scores are 1.0", func() {
		report := confidence.ComputeConfidence(1.0, 1.0, 1.0, defaults)
		Expect(report.Score).To(BeNumerically("~", 1.0, 0.01))
	})

	It("returns 0.0 when all sub-scores are 0.0", func() {
		report := confidence.ComputeConfidence(0.0, 0.0, 0.0, defaults)
		Expect(report.Score).To(BeNumerically("~", 0.0, 0.01))
	})

	It("computes weighted average with default weights", func() {
		// 0.5*0.25 + 0.8*0.35 + 0.6*0.40 = 0.125 + 0.28 + 0.24 = 0.645
		report := confidence.ComputeConfidence(0.5, 0.8, 0.6, defaults)
		Expect(report.Score).To(BeNumerically("~", 0.645, 0.01))
	})

	It("uses configurable weights", func() {
		weights := confidence.ConfidenceWeights{
			Completeness:  0.5,
			Coverage:      0.3,
			Actionability: 0.2,
		}
		// 1.0*0.5 + 0.0*0.3 + 0.0*0.2 = 0.5
		report := confidence.ComputeConfidence(1.0, 0.0, 0.0, weights)
		Expect(report.Score).To(BeNumerically("~", 0.5, 0.01))
	})

	It("includes sub-scores in report", func() {
		report := confidence.ComputeConfidence(0.3, 0.7, 0.9, defaults)
		Expect(report.SubScores).To(HaveKeyWithValue("completeness", 0.3))
		Expect(report.SubScores).To(HaveKeyWithValue("coverage", 0.7))
		Expect(report.SubScores).To(HaveKeyWithValue("actionability", 0.9))
	})

	Describe("PassesThreshold", func() {
		It("returns true when score >= threshold", func() {
			report := confidence.ComputeConfidence(1.0, 1.0, 1.0, defaults)
			Expect(report.PassesThreshold(0.6)).To(BeTrue())
		})

		It("returns false when score < threshold", func() {
			report := confidence.ComputeConfidence(0.0, 0.0, 0.0, defaults)
			Expect(report.PassesThreshold(0.6)).To(BeFalse())
		})

		It("returns true when score exactly equals threshold", func() {
			report := &confidence.ConfidenceReport{Score: 0.6}
			Expect(report.PassesThreshold(0.6)).To(BeTrue())
		})
	})
})

var _ = Describe("DefaultWeights", func() {
	It("sums to 1.0", func() {
		w := confidence.DefaultWeights()
		Expect(w.Completeness + w.Coverage + w.Actionability).To(BeNumerically("~", 1.0, 0.001))
	})
})
