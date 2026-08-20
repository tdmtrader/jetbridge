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
})
