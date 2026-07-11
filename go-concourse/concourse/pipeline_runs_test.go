package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Pipeline Runs", func() {
	Describe("CreatePipelineRun", func() {
		It("POSTs params and returns the created run", func() {
			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs"
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", expectedURL),
					ghttp.VerifyJSON(`{"params":{"ref":"abc"}}`),
					ghttp.RespondWithJSONEncoded(http.StatusCreated,
						atc.PipelineRun{ID: 1, Number: 42, Status: "running"}),
				),
			)

			run, err := team.CreatePipelineRun(
				atc.PipelineRef{Name: "regression-suite"},
				map[string]any{"ref": "abc"},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(run.Number).To(Equal(42))
		})
	})

	Describe("ListPipelineRuns", func() {
		It("GETs the runs list", func() {
			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs"
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", expectedURL),
					ghttp.RespondWithJSONEncoded(http.StatusOK,
						[]atc.PipelineRun{{Number: 2, Status: "succeeded"}, {Number: 1, Status: "failed"}}),
				),
			)

			runs, err := team.ListPipelineRuns(atc.PipelineRef{Name: "regression-suite"})
			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(HaveLen(2))
			Expect(runs[0].Number).To(Equal(2))
		})
	})

	Describe("GetPipelineRun", func() {
		It("GETs a single run and reports absence", func() {
			expectedURL := "/api/v1/teams/some-team/pipelines/regression-suite/runs/7"
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", expectedURL),
					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.PipelineRun{Number: 7}),
				),
			)

			run, found, err := team.GetPipelineRun(atc.PipelineRef{Name: "regression-suite"}, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(run.Number).To(Equal(7))

			atcServer.AppendHandlers(
				ghttp.RespondWith(http.StatusNotFound, ""),
			)
			_, found, err = team.GetPipelineRun(atc.PipelineRef{Name: "regression-suite"}, 8)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})
})
