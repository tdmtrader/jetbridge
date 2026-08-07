package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline Runs API", func() {
	var (
		realdb *realDB
		server *httptest.Server
		team   db.Team
	)

	templateConfig := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "greeting", Type: "string", Default: "hello"},
		},
	}

	savePipeline := func(config atc.Config) db.Pipeline {
		GinkgoHelper()

		pipeline, created, err := team.SavePipeline(
			atc.PipelineRef{Name: "a-pipeline"},
			config,
			db.ConfigVersion(0),
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return pipeline
	}

	createRun := func(template db.Pipeline, params map[string]any) db.PipelineRun {
		GinkgoHelper()

		run, err := realdb.Deps.pipelineRunFactory.CreateRun(template.ID(), params, "fixture-user")
		Expect(err).NotTo(HaveOccurred())
		return run
	}

	BeforeEach(func() {
		realdb = useRealDB()
		server = realdb.Serve()

		var err error
		team, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
		Expect(err).NotTo(HaveOccurred())

		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
	})

	Describe("POST /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
		post := func(body string) *http.Response {
			GinkgoHelper()

			req, err := http.NewRequest("POST",
				server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/runs",
				bytes.NewBufferString(body))
			Expect(err).NotTo(HaveOccurred())
			resp, err := client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			return resp
		}

		It("creates a persisted run and returns 201 with the run body", func() {
			template := savePipeline(templateConfig)

			response := post(`{"params":{"greeting":"hi"}}`)
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusCreated))

			var presented atc.PipelineRun
			Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
			Expect(presented.Number).To(Equal(1))
			Expect(presented.Status).To(Equal("running"))
			Expect(presented.Params).To(Equal(map[string]any{"greeting": "hi"}))
			Expect(presented.CreatedBy).To(Equal("api-user"))

			persisted, found, err := realdb.Deps.pipelineRunFactory.GetRun(template.ID(), presented.Number)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persisted.ID()).To(Equal(presented.ID))
			Expect(persisted.Status()).To(Equal(db.PipelineRunRunning))
			Expect(persisted.Params()).To(Equal(map[string]any{"greeting": "hi"}))
			Expect(persisted.CreatedBy()).To(Equal("api-user"))

			instance, found, err := persisted.InstancePipeline()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(instance.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(1)}))
		})

		It("rejects unknown params with 400 without persisting a run", func() {
			template := savePipeline(templateConfig)

			response := post(`{"params":{"bogus":"x"}}`)
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`unknown param "bogus"`))

			runs, err := realdb.Deps.pipelineRunFactory.ListRuns(template.ID(), 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(BeEmpty())
		})

		It("rejects persisted non-template pipelines with 409", func() {
			savePipeline(atc.Config{})

			response := post(`{}`)
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
		})

		It("returns 409 when the persisted template is owned by durable workflow execution", func() {
			owned, created, err := db.NewWorkflowRunTemplateFactory(realdb.Conn, realdb.LockFactory).SaveWorkflowRunTemplate(
				context.Background(),
				team.ID(),
				atc.PipelineRef{Name: "a-pipeline"},
				templateConfig,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())

			response := post(`{}`)
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("durable workflow run"))

			runs, err := realdb.Deps.pipelineRunFactory.ListRuns(owned.ID(), 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(BeEmpty())
		})
	})

	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs", func() {
		It("lists persisted runs newest first", func() {
			template := savePipeline(templateConfig)
			first := createRun(template, map[string]any{"greeting": "first"})
			second := createRun(template, map[string]any{"greeting": "second"})
			Expect(second.Finish(db.PipelineRunSucceeded)).To(Succeed())

			response, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var runs []atc.PipelineRun
			Expect(json.NewDecoder(response.Body).Decode(&runs)).To(Succeed())
			Expect(runs).To(HaveLen(2))
			Expect([]int{runs[0].ID, runs[1].ID}).To(Equal([]int{second.ID(), first.ID()}))
			Expect([]int{runs[0].Number, runs[1].Number}).To(Equal([]int{2, 1}))
			Expect([]string{runs[0].Status, runs[1].Status}).To(Equal([]string{"succeeded", "running"}))
			Expect([]map[string]any{runs[0].Params, runs[1].Params}).To(Equal([]map[string]any{
				{"greeting": "second"},
				{"greeting": "first"},
			}))
		})
	})

	Describe("GET /api/v1/teams/a-team/pipelines/a-pipeline/runs/:run_number", func() {
		It("returns a persisted run or 404", func() {
			template := savePipeline(templateConfig)
			created := createRun(template, map[string]any{"greeting": "seven"})

			response, err := client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/1")
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusOK))

			var presented atc.PipelineRun
			Expect(json.NewDecoder(response.Body).Decode(&presented)).To(Succeed())
			Expect(response.Body.Close()).To(Succeed())
			Expect(presented.ID).To(Equal(created.ID()))
			Expect(presented.Number).To(Equal(1))
			Expect(presented.Params).To(Equal(map[string]any{"greeting": "seven"}))

			response, err = client.Get(server.URL + "/api/v1/teams/a-team/pipelines/a-pipeline/runs/2")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(response.Body.Close)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
