package atc_test

import (
	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Sensitive request bodies", func() {
	It("names only actions the route table serves", func() {
		routed := map[string]bool{}
		for _, route := range atc.Routes {
			routed[route.Name] = true
		}
		Expect(atc.SensitiveBodyActions()).NotTo(BeEmpty())
		for _, action := range atc.SensitiveBodyActions() {
			Expect(routed).To(HaveKey(action), "%s is declared sensitive but no route serves it", action)
			Expect(atc.RequestBodyIsSensitive(action)).To(BeTrue())
		}
	})

	It("covers every Run route whose body is a credential, bearer, upload or operational text", func() {
		Expect(atc.SensitiveBodyActions()).To(ConsistOf(
			atc.HandoffPipelineRunCredentials,
			atc.CreatePipelineRunV2,
			atc.UploadPipelineRunInput,
			atc.CancelPipelineRun,
		))
		Expect(atc.RequestBodyIsSensitive(atc.SaveConfig)).To(BeFalse())
	})

	It("names each action's declared route parameters", func() {
		Expect(atc.RouteParamNames(atc.HandoffPipelineRunCredentials)).To(Equal(
			[]string{"team_name", "pipeline_name", "number", "result_name"}))
		Expect(atc.RouteParamNames(atc.UploadPipelineRunInput)).To(Equal(
			[]string{"team_name", "pipeline_name", "input_name"}))
		Expect(atc.RouteParamNames("NoSuchAction")).To(BeEmpty())
	})
})
