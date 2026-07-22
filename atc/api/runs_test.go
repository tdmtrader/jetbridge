package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline Runs API", func() {
	var response *http.Response

	BeforeEach(func() {
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		dbTeamFactory.FindTeamReturns(dbTeam, true, nil)
		dbTeam.PipelineReturns(fakePipeline, true, nil)
		fakePipeline.IDReturns(17)
		fakePipeline.TemplateReturns(true)
		fakePipeline.InstanceVarsReturns(nil)
		fakePipeline.ParamsSchemaReturns([]atc.ParamSchema{
			{Name: "greeting", Type: "string", Default: "hello"},
		})
	})

	Describe("POST /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
		post := func(body string) *http.Response {
			req, err := http.NewRequest("POST",
				server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/runs",
				bytes.NewBufferString(body))
			Expect(err).NotTo(HaveOccurred())
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			return resp
		}

		It("creates a run and returns 201 with the run body", func() {
			fakeRun := new(dbfakes.FakePipelineRun)
			fakeRun.IDReturns(3)
			fakeRun.NumberReturns(42)
			fakeRun.StatusReturns(db.PipelineRunRunning)
			fakeRun.ParamsReturns(map[string]any{"greeting": "hi"})
			fakePipelineRunFactory.CreateRunReturns(fakeRun, nil)

			response = post(`{"params":{"greeting":"hi"}}`)
			Expect(response.StatusCode).To(Equal(http.StatusCreated))

			templateID, params, _ := fakePipelineRunFactory.CreateRunArgsForCall(0)
			Expect(templateID).To(Equal(17))
			Expect(params).To(Equal(map[string]any{"greeting": "hi"}))

			var run atc.PipelineRun
			Expect(json.NewDecoder(response.Body).Decode(&run)).To(Succeed())
			Expect(run.Number).To(Equal(42))
			Expect(run.Status).To(Equal("running"))
		})

		It("rejects unknown params with 400", func() {
			response = post(`{"params":{"bogus":"x"}}`)
			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			body, err := io.ReadAll(response.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`unknown param "bogus"`))
			Expect(fakePipelineRunFactory.CreateRunCallCount()).To(Equal(0))
		})

		It("rejects non-template pipelines with 409", func() {
			fakePipeline.TemplateReturns(false)
			response = post(`{}`)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
		})

		It("returns 409 when the template is owned by durable workflow execution", func() {
			fakePipelineRunFactory.CreateRunReturns(nil, db.ErrWorkflowRunOwnedPipeline)

			response = post(`{}`)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("durable workflow run"))
		})
	})

	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
		It("lists runs newest first", func() {
			fakeRun := new(dbfakes.FakePipelineRun)
			fakeRun.NumberReturns(2)
			fakeRun.StatusReturns(db.PipelineRunSucceeded)
			fakePipelineRunFactory.ListRunsReturns([]db.PipelineRun{fakeRun}, nil)

			resp, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var runs []atc.PipelineRun
			Expect(json.NewDecoder(resp.Body).Decode(&runs)).To(Succeed())
			Expect(runs).To(HaveLen(1))
			Expect(runs[0].Number).To(Equal(2))
		})
	})

	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs/:run_number", func() {
		It("returns the run or 404", func() {
			fakeRun := new(dbfakes.FakePipelineRun)
			fakeRun.NumberReturns(7)
			fakePipelineRunFactory.GetRunReturns(fakeRun, true, nil)

			resp, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/7")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			fakePipelineRunFactory.GetRunReturns(nil, false, nil)
			resp, err = client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/8")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
