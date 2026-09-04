package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	concourse "github.com/concourse/concourse/go-concourse/concourse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

// The gate is proven here, against a real booted ATC over HTTP on real
// Postgres, because it is a property of the running server -- the refusal, the
// row that is not written, and the capability the same server reports -- and
// none of those survive being asserted against a handler in isolation.

var runTemplateRef = atc.PipelineRef{Name: "template"}

// A template config the save path accepts: one declared parameter and one
// entry job with no `passed` constraint, which is what
// configvalidate.ValidateTemplateConfig requires.
var runTemplateConfig = []byte(`
---
template: true
params:
- name: environment
  type: string
  required: true
jobs:
- name: entry
`)

var ordinaryPipelineConfig = []byte(`
---
jobs:
- name: entry
`)

var runVars = map[string]any{"environment": "staging"}

var _ = Describe("public run creation gate", func() {
	var (
		memberClient concourse.Client
		member       *http.Client
	)

	JustBeforeEach(func() {
		givenARunnableTemplate()
		memberClient = login(atcURL, "m-user", "m-user")
		member = authedHTTPClient(atcURL, "m-user", "m-user")
	})

	It("refuses creation with a typed conflict, and writes nothing", func() {
		_, err := memberClient.Team("run-team").CreatePipelineRun(runTemplateRef.Name, runVars)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal(atc.ErrPipelineRunCreationDisabled.Error()))

		response := postCreateRun(member, "run-team", runTemplateRef.Name, runVars)
		Expect(response.Status).To(Equal(http.StatusConflict))
		Expect(response.ContentType).To(Equal("application/json"))
		Expect(errorsFrom(response)).To(ConsistOf(atc.ErrPipelineRunCreationDisabled.Error()))

		// No row, no number, no payload pipeline. Refusing before the
		// transaction opens is what makes all four observable at once.
		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
		Expect(countRows("SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL")).To(Equal(0))
	})

	It("phrases the hold so a caller can tell it apart from every other refusal", func() {
		gated := errorsFrom(postCreateRun(member, "run-team", runTemplateRef.Name, runVars))[0]

		// It names the hold, not the operator's switch.
		for _, spelling := range []string{
			"--",
			"enable-pipeline-run-creation",
			"EnablePipelineRunCreation",
			"enablePipelineRunCreation",
			"pipeline_run_creation",
		} {
			Expect(gated).NotTo(ContainSubstring(spelling))
		}

		// Distinguishable from "you mistyped the URL".
		missing := postCreateRun(member, "run-team", "no-such-pipeline", runVars)
		Expect(missing.Status).To(Equal(http.StatusNotFound))
		Expect(string(missing.Body)).NotTo(ContainSubstring(gated))

		// Distinguishable from "you lack the role".
		viewer := postCreateRun(authedHTTPClient(atcURL, "v-user", "v-user"), "run-team", runTemplateRef.Name, runVars)
		Expect(viewer.Status).To(BeElementOf(http.StatusUnauthorized, http.StatusForbidden))
		Expect(string(viewer.Body)).NotTo(ContainSubstring(gated))

		// Distinguishable from each template-shape refusal the create path can
		// still produce. Compared against the constants themselves: with the
		// gate off none of those paths is reachable, so a "differs from"
		// written against gate-off fixtures would compare the gate's text with
		// itself. The gate-on spec below is what proves these constants are
		// the strings the server really sends.
		for _, shape := range []string{
			db.ErrPipelineRunPaused.Error(),
			db.ErrPipelineRunNotTemplate.Error(),
			db.ErrPipelineRunInstanced.Error(),
			"invalid pipeline template: ",
		} {
			Expect(gated).NotTo(ContainSubstring(shape))
		}

		// Archived is the exception: RejectArchivedWrappa answers ahead of the
		// handler with a text/plain body and no errors array, so the gate never
		// runs and the comparison is by status plus Content-Type.
		owner := login(atcURL, "test", "test")
		_, err := owner.Team("run-team").ArchivePipeline(runTemplateRef)
		Expect(err).NotTo(HaveOccurred())

		archived := postCreateRun(member, "run-team", runTemplateRef.Name, runVars)
		Expect(archived.Status).To(Equal(http.StatusConflict))
		Expect(archived.ContentType).To(HavePrefix("text/plain"))
	})

	It("reports a capability that agrees with its own admission decision", func() {
		assertCapabilityAgreesWithAdmission(member)
	})

	Context("when the operator has enabled run creation", func() {
		BeforeEach(func() {
			cmd.EnablePipelineRunCreation = true
		})

		It("admits the identical request and records the run", func() {
			run, err := memberClient.Team("run-team").CreatePipelineRun(runTemplateRef.Name, runVars)
			Expect(err).NotTo(HaveOccurred())
			Expect(run.Number).To(Equal(1))
			Expect(run.InstanceRef).NotTo(BeNil())
			Expect(run.InstanceRef.TeamName).To(Equal("run-team"))
			Expect(run.InstanceRef.PipelineName).To(Equal(runTemplateRef.Name))

			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
		})

		It("reports a capability that agrees with its own admission decision", func() {
			assertCapabilityAgreesWithAdmission(member)
		})

		It("still answers each template-shape refusal in its own words", func() {
			// This is what keeps the gate-off comparison above honest: it
			// proves the constants compared against are the strings this
			// server really sends, not dead values in a header file.
			created, err := memberClient.Team("run-team").CreatePipelineRun(runTemplateRef.Name, runVars)
			Expect(err).NotTo(HaveOccurred())
			Expect(created.InstanceRef).NotTo(BeNil())

			By("an instanced pipeline reference")
			instanced := postCreateRunAt(
				member,
				runsPath("run-team", runTemplateRef.Name)+"?"+atc.PipelineRef{InstanceVars: created.InstanceRef.InstanceVars}.QueryParams().Encode(),
				runVars,
			)
			Expect(instanced.Status).To(Equal(http.StatusConflict))
			Expect(errorsFrom(instanced)).To(ConsistOf(db.ErrPipelineRunInstanced.Error()))

			By("a pipeline that is not a template")
			owner := login(atcURL, "test", "test")
			ordinary := atc.PipelineRef{Name: "ordinary"}
			_, _, _, err = owner.Team("run-team").CreateOrUpdatePipelineConfig(ordinary, "0", ordinaryPipelineConfig, false)
			Expect(err).NotTo(HaveOccurred())
			_, err = owner.Team("run-team").UnpausePipeline(ordinary)
			Expect(err).NotTo(HaveOccurred())

			notTemplate := postCreateRun(member, "run-team", ordinary.Name, runVars)
			Expect(notTemplate.Status).To(Equal(http.StatusConflict))
			Expect(errorsFrom(notTemplate)).To(ConsistOf(db.ErrPipelineRunNotTemplate.Error()))

			By("a paused template")
			_, err = owner.Team("run-team").PausePipeline(runTemplateRef)
			Expect(err).NotTo(HaveOccurred())

			paused := postCreateRun(member, "run-team", runTemplateRef.Name, runVars)
			Expect(paused.Status).To(Equal(http.StatusConflict))
			Expect(errorsFrom(paused)).To(ConsistOf(db.ErrPipelineRunPaused.Error()))
		})
	})
})

// givenARunnableTemplate creates the team and the one template every check in
// this file uses, and leaves it unpaused. CreateOrUpdatePipelineConfig saves
// initially paused, and a paused template already answers 409-with-envelope
// and writes no row -- so a fixture left paused makes every gate check here
// vacuous. The Paused assertion is the guard against that.
func givenARunnableTemplate() {
	GinkgoHelper()

	setupTeam(atcURL, atc.Team{
		Name: "run-team",
		Auth: atc.TeamAuth{
			"viewer": map[string][]string{"users": {"local:v-user"}, "groups": {}},
			"member": map[string][]string{"users": {"local:m-user"}, "groups": {}},
			"owner":  map[string][]string{"users": {"local:test"}, "groups": {}},
		},
	})

	owner := login(atcURL, "test", "test")
	_, _, _, err := owner.Team("run-team").CreateOrUpdatePipelineConfig(runTemplateRef, "0", runTemplateConfig, false)
	Expect(err).NotTo(HaveOccurred())

	_, err = owner.Team("run-team").UnpausePipeline(runTemplateRef)
	Expect(err).NotTo(HaveOccurred())

	pipeline, found, err := owner.Team("run-team").Pipeline(runTemplateRef)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(pipeline.Paused).To(BeFalse(), "the fixture template must be unpaused, or every gate check in this file is vacuous")
	Expect(pipeline.Template).NotTo(BeNil())
	Expect(*pipeline.Template).To(BeTrue())
}

// capturedResponse is a whole HTTP answer, read out so that status, headers
// and body can all be asserted -- the client alone would only carry the error.
type capturedResponse struct {
	Status      int
	ContentType string
	Header      http.Header
	Body        []byte
}

func authedHTTPClient(atcURL, username, password string) *http.Client {
	GinkgoHelper()

	oauth2Config := oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: atcURL + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "federated:id"},
	}

	ctx := context.Background()
	oauthToken, err := oauth2Config.PasswordCredentialsToken(ctx, username, password)
	Expect(err).NotTo(HaveOccurred())

	return oauth2.NewClient(ctx, oauth2.StaticTokenSource(oauthToken))
}

func runsPath(teamName, pipelineName string) string {
	GinkgoHelper()
	path, err := atc.Routes.CreatePathForRoute(atc.CreatePipelineRun, rata.Params{
		"team_name":     teamName,
		"pipeline_name": pipelineName,
	})
	Expect(err).NotTo(HaveOccurred())
	return atcURL + path
}

// postCreateRun issues the one admitted request every check in this track
// shares. A7 is vacuous unless the sweep issues exactly this request.
func postCreateRun(httpClient *http.Client, teamName, pipelineName string, vars map[string]any) capturedResponse {
	GinkgoHelper()
	return postCreateRunAt(httpClient, runsPath(teamName, pipelineName), vars)
}

func postCreateRunAt(httpClient *http.Client, url string, vars map[string]any) capturedResponse {
	GinkgoHelper()

	body, err := json.Marshal(atc.CreatePipelineRunRequest{Vars: vars})
	Expect(err).NotTo(HaveOccurred())

	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("Content-Type", "application/json")

	return capture(httpClient, request)
}

func capture(httpClient *http.Client, request *http.Request) capturedResponse {
	GinkgoHelper()

	response, err := httpClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())

	return capturedResponse{
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Header:      response.Header.Clone(),
		Body:        body,
	}
}

func errorsFrom(response capturedResponse) []string {
	GinkgoHelper()

	var envelope atc.SaveConfigResponse
	Expect(json.Unmarshal(response.Body, &envelope)).To(Succeed(), "expected an {\"errors\":[…]} envelope, got: "+string(response.Body))
	Expect(envelope.Errors).NotTo(BeEmpty())
	return envelope.Errors
}

func countRows(query string) int {
	GinkgoHelper()

	conn := postgresRunner.OpenSingleton()
	defer conn.Close()

	var count int
	Expect(conn.QueryRow(query).Scan(&count)).To(Succeed())
	return count
}

// assertCapabilityAgreesWithAdmission is the one clause no presenter-level
// test can carry: whatever this server tells an authorized caller about
// can_create_run, on each of the three payloads that emit it, must be what the
// same server does with that caller's request, in the same boot.
//
// It must be authenticated. canCreatePipelineRun answers false for an
// unauthenticated caller before it ever reaches the gate, so an anonymous
// comparison agrees in both states and would hold even if the presenter
// ignored the gate entirely.
func assertCapabilityAgreesWithAdmission(httpClient *http.Client) {
	GinkgoHelper()

	reported := runTemplateCapability(httpClient)
	admitted := postCreateRun(httpClient, "run-team", runTemplateRef.Name, runVars).Status == http.StatusCreated

	for route, canCreate := range reported {
		Expect(canCreate).To(Equal(admitted), "can_create_run on "+route+" disagrees with what the same server did with the request")
	}
}

// runTemplateCapability reads can_create_run for the fixture template from
// each of the three routes that emit it, keyed by route name.
func runTemplateCapability(httpClient *http.Client) map[string]bool {
	GinkgoHelper()

	reported := map[string]bool{}

	detailPath, err := atc.Routes.CreatePathForRoute(atc.GetPipeline, rata.Params{
		"team_name":     "run-team",
		"pipeline_name": runTemplateRef.Name,
	})
	Expect(err).NotTo(HaveOccurred())

	var detail atc.Pipeline
	Expect(json.Unmarshal(getCaptured(httpClient, atcURL+detailPath).Body, &detail)).To(Succeed())
	Expect(detail.CanCreateRun).NotTo(BeNil())
	reported[atc.GetPipeline] = *detail.CanCreateRun

	for _, collection := range []struct {
		name   string
		params rata.Params
	}{
		{name: atc.ListPipelines, params: rata.Params{"team_name": "run-team"}},
		{name: atc.ListAllPipelines, params: rata.Params{}},
	} {
		path, err := atc.Routes.CreatePathForRoute(collection.name, collection.params)
		Expect(err).NotTo(HaveOccurred())

		var pipelines []atc.Pipeline
		Expect(json.Unmarshal(getCaptured(httpClient, atcURL+path).Body, &pipelines)).To(Succeed())

		found := false
		for _, pipeline := range pipelines {
			// Skip a run's payload pipeline, which shares the template's name.
			if pipeline.Name != runTemplateRef.Name || pipeline.TeamName != "run-team" || len(pipeline.InstanceVars) > 0 {
				continue
			}
			Expect(pipeline.CanCreateRun).NotTo(BeNil())
			reported[collection.name] = *pipeline.CanCreateRun
			found = true
		}
		Expect(found).To(BeTrue(), "the fixture template is absent from "+collection.name+"; the comparison would be vacuous")
	}

	return reported
}

func getCaptured(httpClient *http.Client, url string) capturedResponse {
	GinkgoHelper()

	request, err := http.NewRequest(http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())

	response := capture(httpClient, request)
	Expect(response.Status).To(Equal(http.StatusOK), url)
	return response
}
