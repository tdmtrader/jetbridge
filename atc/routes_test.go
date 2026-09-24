package atc_test

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline run routes", func() {
	It("registers the collection and durable detail endpoints", func() {
		// This fails if the API contract is absent or a generic pipeline route shadows it.
		matches := map[string]int{}
		for _, route := range atc.Routes {
			switch {
			case route.Name == atc.CreatePipelineRunV2 && route.Path == "/api/v2/teams/:team_name/pipelines/:pipeline_name/runs" && route.Method == http.MethodPost:
				matches[atc.CreatePipelineRunV2]++
			case route.Method == http.MethodPost && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs":
				matches["retired v1 create"]++
			case route.Name == atc.ListPipelineRuns && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs" && route.Method == http.MethodGet:
				matches[atc.ListPipelineRuns]++
			case route.Name == atc.GetPipelineRun && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs/:number" && route.Method == http.MethodGet:
				matches[atc.GetPipelineRun]++
			}
		}
		Expect(matches).To(Equal(map[string]int{
			atc.CreatePipelineRunV2: 1,
			atc.ListPipelineRuns:    1,
			atc.GetPipelineRun:      1,
		}))
	})
})
