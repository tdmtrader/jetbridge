package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("QAOutput", func() {
	validQA := func() schema.QAOutput {
		return schema.QAOutput{
			SchemaVersion: "1.0.0",
			Results: []schema.RequirementResult{
				{
					ID:             "R1",
					Text:           "System authenticates users",
					Status:         schema.CoverageCovered,
					CoveragePoints: 1.0,
				},
				{
					ID:             "R2",
					Text:           "Sessions expire",
					Status:         schema.CoveragePartial,
					CoveragePoints: 0.5,
				},
			},
			Score: schema.QAScore{
				Value:     7.5,
				Max:       10.0,
				Pass:      true,
				Threshold: 7.0,
			},
			Gaps: []schema.Gap{
				{RequirementID: "R2", Severity: "medium", Description: "Partial coverage"},
			},
			Metadata: schema.QAMetadata{
				SpecFile:            "spec.md",
				RequirementsTotal:   2,
				RequirementsCovered: 1,
			},
		}
	}

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			qa := validQA()
			data, err := json.Marshal(qa)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.QAOutput
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.SchemaVersion).To(Equal("1.0.0"))
			Expect(decoded.Results).To(HaveLen(2))
			Expect(decoded.Score.Value).To(BeNumerically("~", 7.5, 0.01))
			Expect(decoded.Gaps).To(HaveLen(1))
		})
	})

	Describe("Validate", func() {
		It("passes for valid output", func() {
			qa := validQA()
			Expect(qa.Validate()).To(Succeed())
		})

		It("defaults schema_version to 1.0.0", func() {
			qa := validQA()
			qa.SchemaVersion = ""
			Expect(qa.Validate()).To(Succeed())
			Expect(qa.SchemaVersion).To(Equal("1.0.0"))
		})

		It("requires result id", func() {
			qa := validQA()
			qa.Results[0].ID = ""
			Expect(qa.Validate()).To(MatchError(ContainSubstring("id")))
		})

		It("requires result text", func() {
			qa := validQA()
			qa.Results[0].Text = ""
			Expect(qa.Validate()).To(MatchError(ContainSubstring("text")))
		})

		It("rejects invalid coverage status", func() {
			qa := validQA()
			qa.Results[0].Status = "unknown"
			Expect(qa.Validate()).To(MatchError(ContainSubstring("invalid status")))
		})

		It("requires score.max > 0", func() {
			qa := validQA()
			qa.Score.Max = 0
			Expect(qa.Validate()).To(MatchError(ContainSubstring("score.max")))
		})

		It("accepts all valid coverage statuses", func() {
			for _, s := range []schema.CoverageStatus{
				schema.CoverageCovered,
				schema.CoveragePartial,
				schema.CoverageUncoveredImplemented,
				schema.CoverageUncoveredBroken,
				schema.CoverageFailing,
			} {
				qa := validQA()
				qa.Results[0].Status = s
				Expect(qa.Validate()).To(Succeed())
			}
		})
	})

	Describe("CoveragePoints", func() {
		It("returns 1.0 for covered", func() {
			Expect(schema.CoverageCovered.CoveragePoints()).To(Equal(1.0))
		})
		It("returns 0.5 for partial", func() {
			Expect(schema.CoveragePartial.CoveragePoints()).To(Equal(0.5))
		})
		It("returns 0.75 for uncovered_implemented", func() {
			Expect(schema.CoverageUncoveredImplemented.CoveragePoints()).To(Equal(0.75))
		})
		It("returns 0.0 for uncovered_broken", func() {
			Expect(schema.CoverageUncoveredBroken.CoveragePoints()).To(Equal(0.0))
		})
		It("returns 0.0 for failing", func() {
			Expect(schema.CoverageFailing.CoveragePoints()).To(Equal(0.0))
		})
	})
})
