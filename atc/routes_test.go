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
			case route.Name == atc.CreatePipelineRun && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs" && route.Method == http.MethodPost:
				matches[atc.CreatePipelineRun]++
			case route.Name == atc.ListPipelineRuns && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs" && route.Method == http.MethodGet:
				matches[atc.ListPipelineRuns]++
			case route.Name == atc.GetPipelineRun && route.Path == "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs/:number" && route.Method == http.MethodGet:
				matches[atc.GetPipelineRun]++
			}
		}
		Expect(matches).To(Equal(map[string]int{
			atc.CreatePipelineRun: 1,
			atc.ListPipelineRuns:  1,
			atc.GetPipelineRun:    1,
		}))
	})
})
