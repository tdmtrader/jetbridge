package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline Runs API", func() {
	var (
		database *realDB
		template db.Pipeline
	)

	BeforeEach(func() {
		database = useRealDB()
		template = database.SavePipeline(database.Main, "release", atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		})
		fakeAccess.IsAuthenticatedReturns(true)
		fakeAccess.IsAuthorizedReturns(true)
		fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
		server = database.Serve()
	})

	create := func(vars map[string]any) *http.Response {
		GinkgoHelper()
		body, err := json.Marshal(atc.CreatePipelineRunRequest{Vars: vars})
		Expect(err).NotTo(HaveOccurred())
		request, err := http.NewRequest(http.MethodPost, pipelineRunsURL(server, template.Name()), bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		return response
	}

	decodeRun := func(response *http.Response) atc.PipelineRun {
		GinkgoHelper()
		defer response.Body.Close()
		var run atc.PipelineRun
		Expect(json.NewDecoder(response.Body).Decode(&run)).To(Succeed())
		return run
	}

	It("creates a run and returns its committed actual payload reference", func() {
		// This fails if creation emits a synthetic {run:N} reference or responds before the child commits.
		response := create(map[string]any{"environment": "production"})
		Expect(response.StatusCode).To(Equal(http.StatusCreated))
		run := decodeRun(response)
		Expect(run.Number).To(Equal(1))
		Expect(run.CreatedBy).To(Equal("api-user"))
		Expect(run.InstanceRef).NotTo(BeNil())
		Expect(run.InstanceRef.TeamName).To(Equal("main"))
		Expect(run.InstanceRef.PipelineName).To(Equal("release"))

		payload, found, err := database.Main.Pipeline(atc.PipelineRef{Name: run.InstanceRef.PipelineName, InstanceVars: run.InstanceRef.InstanceVars})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		runID, hasRunID := payload.PipelineRunID()
		Expect(hasRunID).To(BeTrue())
		Expect(runID).To(Equal(run.ID))
	})

	It("lists durable runs newest first with a default limit of fifty", func() {
		// This fails if the collection uses generic build pagination defaults or returns oldest-first history.
		for number := 1; number <= 51; number++ {
			response := create(map[string]any{"environment": "env-" + strconv.Itoa(number)})
			Expect(response.StatusCode).To(Equal(http.StatusCreated))
			Expect(response.Body.Close()).To(Succeed())
		}

		response, err := client.Get(pipelineRunsURL(server, template.Name()))
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var runs []atc.PipelineRun
		Expect(json.NewDecoder(response.Body).Decode(&runs)).To(Succeed())
		Expect(runs).To(HaveLen(50))
		Expect(runs[0].Number).To(Equal(51))
		Expect(runs[49].Number).To(Equal(2))
		Expect(response.Header.Get("Link")).To(ContainSubstring("to=1&limit=50"))

		response, err = client.Get(pipelineRunsURL(server, template.Name()) + "?from=3&to=5&limit=2")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(json.NewDecoder(response.Body).Decode(&runs)).To(Succeed())
		Expect(runs).To(HaveLen(2))
		Expect(runs[0].Number).To(Equal(4))
		Expect(runs[1].Number).To(Equal(3))
	})

	It("rejects a reversed pagination range before querying durable history", func() {
		response, err := client.Get(pipelineRunsURL(server, template.Name()) + "?from=5&to=3")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
	})

	It("returns a durable detail and leaves a missing number as not found", func() {
		// This fails if detail looks up a disposable payload first or treats a durable miss as an internal error.
		created := decodeRun(create(map[string]any{"environment": "production"}))
		response, err := client.Get(pipelineRunsURL(server, template.Name()) + "/" + strconv.Itoa(created.Number))
		Expect(err).NotTo(HaveOccurred())
		got := decodeRun(response)
		Expect(got.ID).To(Equal(created.ID))

		response, err = client.Get(pipelineRunsURL(server, template.Name()) + "/404")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("maps invalid params and template creation holds to actionable client errors", func() {
		// This fails if expected request failures are collapsed into a 500 without a useful body.
		response := create(map[string]any{})
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(string(body)).To(ContainSubstring("environment"))

		Expect(template.Pause("api-user")).To(Succeed())
		response = create(map[string]any{"environment": "production"})
		body, err = io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusConflict))
		Expect(string(body)).To(ContainSubstring("paused"))
	})

	It("allows public durable history while redacting params and an inaccessible child", func() {
		// This fails if template publicity leaks params or makes a private payload enterable.
		Expect(template.Expose()).To(Succeed())
		created := decodeRun(create(map[string]any{"environment": "production"}))
		Expect(created.InstanceRef).NotTo(BeNil())

		fakeAccess.IsAuthenticatedReturns(false)
		fakeAccess.IsAuthorizedReturns(false)
		response, err := client.Get(pipelineRunsURL(server, template.Name()))
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var runs []atc.PipelineRun
		Expect(json.NewDecoder(response.Body).Decode(&runs)).To(Succeed())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].Params).To(BeNil())
		Expect(runs[0].ConfigHash).To(BeNil())
		Expect(runs[0].InstanceRef).To(BeNil())
	})

	It("denies a private template's durable history to anonymous viewers", func() {
		// This fails if durable history bypasses base-template pipeline access.
		fakeAccess.IsAuthenticatedReturns(false)
		fakeAccess.IsAuthorizedReturns(false)
		response, err := client.Get(pipelineRunsURL(server, template.Name()))
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("shows an independently public child reference without granting base params", func() {
		Expect(template.Expose()).To(Succeed())
		created := decodeRun(create(map[string]any{"environment": "production"}))
		payload, found, err := database.Main.Pipeline(atc.PipelineRef{
			Name:         created.InstanceRef.PipelineName,
			InstanceVars: created.InstanceRef.InstanceVars,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = database.Conn.Exec("UPDATE pipelines SET public = true WHERE id = $1", payload.ID())
		Expect(err).NotTo(HaveOccurred())

		fakeAccess.IsAuthenticatedReturns(false)
		fakeAccess.IsAuthorizedReturns(false)
		response, err := client.Get(pipelineRunsURL(server, template.Name()) + "/" + strconv.Itoa(created.Number))
		Expect(err).NotTo(HaveOccurred())
		run := decodeRun(response)
		Expect(run.Params).To(BeNil())
		Expect(run.ConfigHash).To(BeNil())
		Expect(run.InstanceRef).NotTo(BeNil())
	})

	It("keeps archived history readable while refusing another run", func() {
		// This fails if archive rejection is applied to history or skipped for creation.
		decodeRun(create(map[string]any{"environment": "production"}))
		Expect(template.Archive()).To(Succeed())

		response, err := client.Get(pipelineRunsURL(server, template.Name()))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(response.Body.Close()).To(Succeed())

		response = create(map[string]any{"environment": "production"})
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusConflict))
		Expect(string(body)).To(ContainSubstring("archived"))
	})

	It("retains durable detail after its payload is reclaimed", func() {
		// This fails if detail treats a reclaimed child as a missing durable run or reconstructs an instance reference.
		created := decodeRun(create(map[string]any{"environment": "production"}))
		_, err := database.Conn.Exec(`UPDATE pipelines SET run_retention_ttl_days = 1 WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Conn.Exec(`UPDATE builds SET status = 'failed', end_time = now() WHERE pipeline_run_id = $1`, created.ID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Conn.Exec(`UPDATE pipeline_runs SET status = 'failed', completed_at = now() - interval '2 days' WHERE id = $1`, created.ID)
		Expect(err).NotTo(HaveOccurred())
		destroyed, err := db.NewPipelineRunReclaimLifecycle(database.Conn).DestroyReclaimableRun(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())

		response, err := client.Get(pipelineRunsURL(server, template.Name()) + "/" + strconv.Itoa(created.Number))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		run := decodeRun(response)
		Expect(run.Reclaimed).To(BeTrue())
		Expect(run.InstanceRef).To(BeNil())
	})
})

func pipelineRunsURL(server *httptest.Server, pipeline string) string {
	return server.URL + "/api/v1/teams/main/pipelines/" + pipeline + "/runs"
}
