package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline run roles", func() {
	It("uses member access for creation and viewer access for durable history", func() {
		// This fails if a new run route falls back to the blank, admin-only role.
		matched := 0
		for action, role := range accessor.DefaultRoles {
			switch action {
			case atc.CreatePipelineRun:
				matched++
				Expect(role).To(Equal(accessor.MemberRole))
			case atc.ListPipelineRuns, atc.GetPipelineRun:
				matched++
				Expect(role).To(Equal(accessor.ViewerRole))
			}
		}
		Expect(matched).To(Equal(3))
	})

	Describe("ValidateCustomRoles", func() {
		It("accepts the stock roles", func() {
			Expect(accessor.ValidateCustomRoles(nil)).To(Succeed())
			Expect(accessor.ValidateCustomRoles(map[string]string{})).To(Succeed())
		})

		It("accepts a run creation role equal to or stronger than set-pipeline", func() {
			Expect(accessor.ValidateCustomRoles(map[string]string{
				atc.CreatePipelineRun: accessor.MemberRole,
			})).To(Succeed())
			Expect(accessor.ValidateCustomRoles(map[string]string{
				atc.CreatePipelineRun: accessor.OwnerRole,
			})).To(Succeed())
			Expect(accessor.ValidateCustomRoles(map[string]string{
				atc.CreatePipelineRun: accessor.OperatorRole,
				atc.SaveConfig:        accessor.OperatorRole,
			})).To(Succeed())
		})

		DescribeTable("refuses a run creation role weaker than set-pipeline",
			func(customRoles map[string]string, runRole, saveRole string) {
				err := accessor.ValidateCustomRoles(customRoles)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(atc.CreatePipelineRun))
				Expect(err.Error()).To(ContainSubstring(atc.SaveConfig))
				Expect(err.Error()).To(ContainSubstring(runRole))
				Expect(err.Error()).To(ContainSubstring(saveRole))
			},
			Entry("operator run creation against the default member set-pipeline",
				map[string]string{atc.CreatePipelineRun: accessor.OperatorRole},
				accessor.OperatorRole, accessor.MemberRole),
			Entry("viewer run creation against the default member set-pipeline",
				map[string]string{atc.CreatePipelineRun: accessor.ViewerRole},
				accessor.ViewerRole, accessor.MemberRole),
			Entry("default member run creation against a raised owner set-pipeline",
				map[string]string{atc.SaveConfig: accessor.OwnerRole},
				accessor.MemberRole, accessor.OwnerRole),
		)
	})
})
