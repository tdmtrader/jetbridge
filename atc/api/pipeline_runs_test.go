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
		// These specs are about the run route and the run lifecycle, not about
		// the operator gate: every one of them needs a run to exist. The gate
		// itself is proven in atc/integration, against a real booted ATC in
		// both states.
		atc.EnablePipelineRunCreation = true
		DeferCleanup(func() { atc.EnablePipelineRunCreation = false })

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

	assertMaterializationConflict := func(config atc.Config, expectedBody string) {
		GinkgoHelper()
		updated, _, err := database.Main.SavePipeline(template.PipelineRef(), config, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		template = updated

		response := create(map[string]any{"environment": "prod"})
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(string(body)).To(ContainSubstring(expectedBody))

		var runCount int
		Expect(database.Conn.QueryRow("SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1", template.ID()).Scan(&runCount)).To(Succeed())
		Expect(runCount).To(BeZero())
		found, err := template.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(template.LastRunNumber()).To(BeZero())

		updated, _, err = database.Main.SavePipeline(template.PipelineRef(), atc.Config{
			Template: true,
			Params:   config.Params,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		template = updated
		Expect(decodeRun(create(map[string]any{"environment": "prod"})).Number).To(Equal(1))
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

	It("writes a raw Unix-second created_at value in the server response", func() {
		response := create(map[string]any{"environment": "production"})
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusCreated))

		var wire struct {
			CreatedAt json.RawMessage `json:"created_at"`
		}
		Expect(json.Unmarshal(body, &wire)).To(Succeed())
		Expect(string(wire.CreatedAt)).To(MatchRegexp(`^[0-9]+$`))
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

	It("answers every client error with the same JSON envelope the rest of the API uses", func() {
		// This fails if a malformed request is refused with http.Error, which
		// stamps text/plain on handlers whose every other response is JSON, so
		// a client that decodes atc.SaveConfigResponse cannot read the reason.
		for _, badRequest := range []struct {
			description string
			request     func() *http.Request
			reason      string
		}{
			{
				description: "malformed create body",
				request: func() *http.Request {
					request, err := http.NewRequest(http.MethodPost, pipelineRunsURL(server, template.Name()), bytes.NewBufferString("not json"))
					Expect(err).NotTo(HaveOccurred())
					request.Header.Set("Content-Type", "application/json")
					return request
				},
				reason: "invalid pipeline run request",
			},
			{
				description: "malformed pagination limit",
				request: func() *http.Request {
					request, err := http.NewRequest(http.MethodGet, pipelineRunsURL(server, template.Name())+"?limit=banana", nil)
					Expect(err).NotTo(HaveOccurred())
					return request
				},
				reason: "invalid limit pagination value",
			},
			{
				description: "reversed pagination range",
				request: func() *http.Request {
					request, err := http.NewRequest(http.MethodGet, pipelineRunsURL(server, template.Name())+"?from=5&to=3", nil)
					Expect(err).NotTo(HaveOccurred())
					return request
				},
				reason: "invalid range boundaries",
			},
			{
				description: "malformed run number",
				request: func() *http.Request {
					request, err := http.NewRequest(http.MethodGet, pipelineRunsURL(server, template.Name())+"/not-a-number", nil)
					Expect(err).NotTo(HaveOccurred())
					return request
				},
				reason: "invalid pipeline run number",
			},
		} {
			By(badRequest.description)
			response, err := client.Do(badRequest.request())
			Expect(err).NotTo(HaveOccurred())
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Body.Close()).To(Succeed())

			Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			var envelope atc.SaveConfigResponse
			Expect(json.Unmarshal(body, &envelope)).To(Succeed())
			Expect(envelope.Errors).To(ConsistOf(badRequest.reason))
		}
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

	It("names the defect when a stored template no longer validates", func() {
		// This fails if a template row that predates save-time validation (an
		// upgrade, a direct DB edit) answers a bare 500 instead of saying why
		// it cannot produce a run. Saved through the DB layer, which is the
		// only way such a row exists once the save route validates.
		updated, _, err := database.Main.SavePipeline(template.PipelineRef(), atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
		}, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		template = updated

		response := create(map[string]any{"environment": "production"})
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		Expect(response.StatusCode).To(Equal(http.StatusConflict))
		Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(string(body)).To(ContainSubstring("template must contain at least one entry job"))
	})

	It("rejects materialized job-name collisions as invalid run parameters", func() {
		assertMaterializationConflict(atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Jobs: atc.JobConfigs{
				{Name: "deploy-((environment))"},
				{Name: "deploy-prod"},
			},
		}, "duplicate job name deploy-prod")
	})

	It("rejects materialized resource-name collisions as invalid run parameters", func() {
		assertMaterializationConflict(atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Resources: atc.ResourceConfigs{
				{Name: "source-((environment))", Type: "git", Source: atc.Source{"uri": "https://example.com/one"}},
				{Name: "source-prod", Type: "git", Source: atc.Source{"uri": "https://example.com/two"}},
			},
			Jobs: atc.JobConfigs{{Name: "entry"}},
		}, "same name ('source-prod')")
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

	It("rejects instanced pipeline references at each run route boundary", func() {
		created := decodeRun(create(map[string]any{"environment": "production"}))
		Expect(created.InstanceRef).NotTo(BeNil())
		instanceQuery := atc.PipelineRef{InstanceVars: created.InstanceRef.InstanceVars}.QueryParams().Encode()
		instanceRunsPath := pipelineRunsURL(server, template.Name())
		instanceRunsURL := instanceRunsPath + "?" + instanceQuery

		for _, request := range []struct {
			method string
			url    string
			body   io.Reader
		}{
			{method: http.MethodGet, url: instanceRunsURL},
			{method: http.MethodGet, url: instanceRunsPath + "/" + strconv.Itoa(created.Number) + "?" + instanceQuery},
			{method: http.MethodPost, url: instanceRunsURL, body: bytes.NewBufferString(`{"vars":{}}`)},
		} {
			httpRequest, err := http.NewRequest(request.method, request.url, request.body)
			Expect(err).NotTo(HaveOccurred())
			if request.body != nil {
				httpRequest.Header.Set("Content-Type", "application/json")
			}
			response, err := client.Do(httpRequest)
			Expect(err).NotTo(HaveOccurred())
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Body.Close()).To(Succeed())
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			Expect(string(body)).To(ContainSubstring(db.ErrPipelineRunInstanced.Error()))
		}
	})

	It("maps payload visibility changes to actionable conflicts", func() {
		created := decodeRun(create(map[string]any{"environment": "production"}))
		Expect(created.InstanceRef).NotTo(BeNil())
		instanceQuery := atc.PipelineRef{InstanceVars: created.InstanceRef.InstanceVars}.QueryParams().Encode()
		baseURL := server.URL + "/api/v1/teams/main/pipelines/" + template.Name()

		for _, action := range []string{"expose", "hide"} {
			request, err := http.NewRequest(http.MethodPut, baseURL+"/"+action+"?"+instanceQuery, nil)
			Expect(err).NotTo(HaveOccurred())
			response, err := client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Body.Close()).To(Succeed())
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			Expect(string(body)).To(ContainSubstring(db.ErrPipelineRunPayloadMutation.Error()))
		}
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
	It("lists a reclaimed run beside live ones without misattributing payloads", func() {
		// This fails if the batched payload lookup drops a run, attributes one
		// run's payload to another, or presents a reclaimed run as enterable.
		first := decodeRun(create(map[string]any{"environment": "one"}))
		second := decodeRun(create(map[string]any{"environment": "two"}))
		third := decodeRun(create(map[string]any{"environment": "three"}))

		_, err := database.Conn.Exec(`UPDATE pipelines SET run_retention_ttl_days = 1 WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Conn.Exec(`UPDATE builds SET status = 'failed', end_time = now() WHERE pipeline_run_id = $1`, second.ID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Conn.Exec(`UPDATE pipeline_runs SET status = 'failed', completed_at = now() - interval '2 days' WHERE id = $1`, second.ID)
		Expect(err).NotTo(HaveOccurred())
		destroyed, err := db.NewPipelineRunReclaimLifecycle(database.Conn).DestroyReclaimableRun(second.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())

		response, err := client.Get(pipelineRunsURL(server, template.Name()))
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var runs []atc.PipelineRun
		Expect(json.NewDecoder(response.Body).Decode(&runs)).To(Succeed())
		Expect(runs).To(HaveLen(3))

		byNumber := map[int]atc.PipelineRun{}
		for _, listed := range runs {
			byNumber[listed.Number] = listed
		}
		Expect(byNumber[second.Number].ID).To(Equal(second.ID))
		Expect(byNumber[second.Number].Reclaimed).To(BeTrue())
		Expect(byNumber[second.Number].InstanceRef).To(BeNil())
		for _, live := range []atc.PipelineRun{first, third} {
			listed := byNumber[live.Number]
			Expect(listed.ID).To(Equal(live.ID))
			Expect(listed.Reclaimed).To(BeFalse())
			Expect(listed.InstanceRef).NotTo(BeNil())
			Expect(listed.InstanceRef.PipelineName).To(Equal(template.Name()))
			Expect(listed.InstanceRef.InstanceVars).To(Equal(atc.InstanceVars{"run": float64(live.Number)}))
		}
	})
})

func pipelineRunsURL(server *httptest.Server, pipeline string) string {
	return server.URL + "/api/v1/teams/main/pipelines/" + pipeline + "/runs"
}
