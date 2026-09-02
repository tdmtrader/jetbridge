package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type buildsAPITeamState struct {
	mu                    sync.Mutex
	createStartedBuildErr error
}

func (state *buildsAPITeamState) setCreateStartedBuildError(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.createStartedBuildErr = err
}

type buildsAPITeam struct {
	db.Team
	state *buildsAPITeamState
}

func (team *buildsAPITeam) CreateStartedBuild(plan atc.Plan) (db.Build, error) {
	team.state.mu.Lock()
	err := team.state.createStartedBuildErr
	team.state.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return team.Team.CreateStartedBuild(plan)
}

type buildsAPITeamFactory struct {
	db.TeamFactory
	teamName string
	state    *buildsAPITeamState
}

func (factory buildsAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	team, found, err := factory.TeamFactory.FindTeam(name)
	if err != nil || !found || team.Name() != factory.teamName {
		return team, found, err
	}
	return &buildsAPITeam{Team: team, state: factory.state}, true, nil
}

func buildsAPIRequireTeamBuilds(team db.Team) []db.BuildForAPI {
	GinkgoHelper()

	builds, _, err := team.Builds(db.Page{Limit: 100})
	Expect(err).NotTo(HaveOccurred())
	return builds
}

type buildsAPIVisibleBuildsCall struct {
	teamNames []string
	page      db.Page
}

type buildsAPIBuildFactoryState struct {
	mu sync.Mutex

	visibleBuildsErr error
	buildForAPIErr   error
	buildWrapper     func(db.BuildForAPI) db.BuildForAPI
	visibleCalls     []buildsAPIVisibleBuildsCall
	allCalls         []db.Page
	buildCalls       []int
}

func cloneBuildsAPIPage(page db.Page) db.Page {
	cloned := page
	if page.From != nil {
		cloned.From = db.NewIntPtr(*page.From)
	}
	if page.To != nil {
		cloned.To = db.NewIntPtr(*page.To)
	}
	return cloned
}

func (state *buildsAPIBuildFactoryState) setVisibleBuildsError(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.visibleBuildsErr = err
}

func (state *buildsAPIBuildFactoryState) setBuildForAPIError(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.buildForAPIErr = err
}

func (state *buildsAPIBuildFactoryState) setBuildWrapper(wrapper func(db.BuildForAPI) db.BuildForAPI) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.buildWrapper = wrapper
}

func (state *buildsAPIBuildFactoryState) visibleBuildsCalls() []buildsAPIVisibleBuildsCall {
	state.mu.Lock()
	defer state.mu.Unlock()

	calls := make([]buildsAPIVisibleBuildsCall, len(state.visibleCalls))
	for i, call := range state.visibleCalls {
		calls[i] = buildsAPIVisibleBuildsCall{
			teamNames: append([]string(nil), call.teamNames...),
			page:      cloneBuildsAPIPage(call.page),
		}
	}
	return calls
}

func (state *buildsAPIBuildFactoryState) allBuildsCalls() []db.Page {
	state.mu.Lock()
	defer state.mu.Unlock()

	calls := make([]db.Page, len(state.allCalls))
	for i, call := range state.allCalls {
		calls[i] = cloneBuildsAPIPage(call)
	}
	return calls
}

func (state *buildsAPIBuildFactoryState) buildForAPICalls() []int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]int(nil), state.buildCalls...)
}

type buildsAPIBuildFactory struct {
	db.BuildFactory
	state *buildsAPIBuildFactoryState
}

func (factory *buildsAPIBuildFactory) VisibleBuilds(teamNames []string, page db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	factory.state.mu.Lock()
	factory.state.visibleCalls = append(factory.state.visibleCalls, buildsAPIVisibleBuildsCall{
		teamNames: append([]string(nil), teamNames...),
		page:      cloneBuildsAPIPage(page),
	})
	err := factory.state.visibleBuildsErr
	factory.state.mu.Unlock()

	if err != nil {
		return nil, db.Pagination{}, err
	}
	return factory.BuildFactory.VisibleBuilds(teamNames, page)
}

func (factory *buildsAPIBuildFactory) AllBuilds(page db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	factory.state.mu.Lock()
	factory.state.allCalls = append(factory.state.allCalls, cloneBuildsAPIPage(page))
	factory.state.mu.Unlock()

	return factory.BuildFactory.AllBuilds(page)
}

func (factory *buildsAPIBuildFactory) BuildForAPI(buildID int) (db.BuildForAPI, bool, error) {
	factory.state.mu.Lock()
	factory.state.buildCalls = append(factory.state.buildCalls, buildID)
	err := factory.state.buildForAPIErr
	wrapper := factory.state.buildWrapper
	factory.state.mu.Unlock()

	if err != nil {
		return nil, false, err
	}

	build, found, err := factory.BuildFactory.BuildForAPI(buildID)
	if err != nil || !found || wrapper == nil {
		return build, found, err
	}
	return wrapper(build), true, nil
}

type buildsAPIBuildState struct {
	mu sync.Mutex

	pipelineOverride bool
	pipeline         db.Pipeline
	pipelineFound    bool
	pipelineErr      error
	pipelineWrapper  func(db.Pipeline) db.Pipeline
	resourcesErr     error
	markAsAbortedErr error
	preparationSet   bool
	preparation      db.BuildPreparation
	preparationFound bool
	preparationErr   error
}

func (state *buildsAPIBuildState) setPipelineResult(pipeline db.Pipeline, found bool, err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pipelineOverride = true
	state.pipeline = pipeline
	state.pipelineFound = found
	state.pipelineErr = err
}

func (state *buildsAPIBuildState) setResourcesError(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.resourcesErr = err
}

func (state *buildsAPIBuildState) setPipelineWrapper(wrapper func(db.Pipeline) db.Pipeline) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pipelineWrapper = wrapper
}

func (state *buildsAPIBuildState) setMarkAsAbortedError(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.markAsAbortedErr = err
}

func (state *buildsAPIBuildState) setPreparationResult(
	preparation db.BuildPreparation,
	found bool,
	err error,
) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.preparationSet = true
	state.preparation = preparation
	state.preparationFound = found
	state.preparationErr = err
}

type buildsAPIBuild struct {
	db.BuildForAPI
	state *buildsAPIBuildState
}

func (build *buildsAPIBuild) Pipeline() (db.Pipeline, bool, error) {
	build.state.mu.Lock()
	override := build.state.pipelineOverride
	pipeline := build.state.pipeline
	found := build.state.pipelineFound
	err := build.state.pipelineErr
	wrapper := build.state.pipelineWrapper
	build.state.mu.Unlock()

	if override {
		return pipeline, found, err
	}

	pipeline, found, err = build.BuildForAPI.Pipeline()
	if err != nil || !found || wrapper == nil {
		return pipeline, found, err
	}
	return wrapper(pipeline), true, nil
}

func (build *buildsAPIBuild) Resources() ([]db.BuildInput, []db.BuildOutput, error) {
	build.state.mu.Lock()
	err := build.state.resourcesErr
	build.state.mu.Unlock()

	if err != nil {
		return nil, nil, err
	}
	return build.BuildForAPI.Resources()
}

func (build *buildsAPIBuild) MarkAsAborted() error {
	build.state.mu.Lock()
	err := build.state.markAsAbortedErr
	build.state.mu.Unlock()

	// A valid persisted build cannot make only MarkAsAborted fail, so this is
	// the narrow error seam for that one existing handler branch.
	if err != nil {
		return err
	}
	return build.BuildForAPI.MarkAsAborted()
}

func (build *buildsAPIBuild) Preparation() (db.BuildPreparation, bool, error) {
	build.state.mu.Lock()
	override := build.state.preparationSet
	preparation := build.state.preparation
	found := build.state.preparationFound
	err := build.state.preparationErr
	build.state.mu.Unlock()

	// Consistent persisted builds always return a found preparation. This seam
	// is limited to the handler's existing not-found and lookup-error branches.
	if override {
		return preparation, found, err
	}
	return build.BuildForAPI.Preparation()
}

type buildsAPIPipelineState struct {
	mu sync.Mutex

	jobOverride     bool
	jobOverrideName string
	job             db.Job
	jobFound        bool
	jobErr          error
}

func (state *buildsAPIPipelineState) setJobResult(name string, job db.Job, found bool, err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.jobOverride = true
	state.jobOverrideName = name
	state.job = job
	state.jobFound = found
	state.jobErr = err
}

// buildsAPIPipeline preserves every real pipeline behavior except the one
// Pipeline.Job result that an error-path spec deliberately makes unreachable.
type buildsAPIPipeline struct {
	db.Pipeline
	state *buildsAPIPipelineState
}

func (pipeline *buildsAPIPipeline) Job(name string) (db.Job, bool, error) {
	pipeline.state.mu.Lock()
	override := pipeline.state.jobOverride
	overrideName := pipeline.state.jobOverrideName
	job := pipeline.state.job
	found := pipeline.state.jobFound
	err := pipeline.state.jobErr
	pipeline.state.mu.Unlock()

	if override && name == overrideName {
		return job, found, err
	}
	return pipeline.Pipeline.Job(name)
}

func buildsAPIStartJobBuild(job db.Job, createdBy string, plan atc.Plan, finalStatus db.BuildStatus) db.Build {
	GinkgoHelper()

	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	started, err := build.Start(plan)
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	if finalStatus != db.BuildStatusStarted {
		Expect(build.Finish(finalStatus)).To(Succeed())
	}
	return build
}

func buildsAPICreateCheckBuild(resource db.Resource, planID atc.PlanID) db.Build {
	GinkgoHelper()

	build, created, err := resource.CreateBuild(context.Background(), true, atc.Plan{
		ID: planID,
		Check: &atc.CheckPlan{
			Name:   resource.Name(),
			Type:   resource.Type(),
			Source: resource.Source(),
		},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	return build
}

func buildsAPIRequireBuildForAPI(factory db.BuildFactory, buildID int) db.BuildForAPI {
	GinkgoHelper()

	build, found, err := factory.BuildForAPI(buildID)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return build
}

func buildsAPIRequireBuild(factory db.BuildFactory, buildID int) db.Build {
	GinkgoHelper()

	build, found, err := factory.Build(buildID)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return build
}

func buildsAPIExpectedPresentedBuild(build db.BuildForAPI) atc.Build {
	GinkgoHelper()

	expected := atc.Build{
		ID:                   build.ID(),
		TeamName:             build.TeamName(),
		Name:                 build.Name(),
		Status:               atc.BuildStatus(build.Status()),
		APIURL:               fmt.Sprintf("/api/v1/builds/%d", build.ID()),
		JobName:              build.JobName(),
		ResourceName:         build.ResourceName(),
		PipelineID:           build.PipelineID(),
		PipelineName:         build.PipelineName(),
		PipelineInstanceVars: build.PipelineInstanceVars(),
		CreatedBy:            build.CreatedBy(),
	}
	if !build.StartTime().IsZero() {
		expected.StartTime = build.StartTime().Unix()
	}
	if !build.EndTime().IsZero() {
		expected.EndTime = build.EndTime().Unix()
	}
	if !build.ReapTime().IsZero() {
		expected.ReapTime = build.ReapTime().Unix()
	}
	return expected
}

func buildsAPIExpectBuildsResponse(response *http.Response, expectedBuilds []db.BuildForAPI) []atc.Build {
	GinkgoHelper()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())

	var actual []atc.Build
	Expect(json.Unmarshal(body, &actual)).To(Succeed())

	expected := make([]atc.Build, len(expectedBuilds))
	for i, build := range expectedBuilds {
		expected[i] = buildsAPIExpectedPresentedBuild(build)
	}
	expectedJSON, err := json.Marshal(expected)
	Expect(err).NotTo(HaveOccurred())
	Expect(body).To(MatchJSON(expectedJSON))
	return actual
}

func buildsAPIExpectBuildResponse(response *http.Response, expectedBuild db.BuildForAPI) atc.Build {
	GinkgoHelper()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())

	var actual atc.Build
	Expect(json.Unmarshal(body, &actual)).To(Succeed())

	expectedJSON, err := json.Marshal(buildsAPIExpectedPresentedBuild(expectedBuild))
	Expect(err).NotTo(HaveOccurred())
	Expect(body).To(MatchJSON(expectedJSON))
	return actual
}

func buildsAPIExpectedResources(build db.BuildForAPI) atc.BuildInputsOutputs {
	GinkgoHelper()

	inputs, outputs, err := build.Resources()
	Expect(err).NotTo(HaveOccurred())

	expected := atc.BuildInputsOutputs{
		Inputs:  make([]atc.PublicBuildInput, len(inputs)),
		Outputs: make([]atc.PublicBuildOutput, len(outputs)),
	}
	for i, input := range inputs {
		expected.Inputs[i] = atc.PublicBuildInput{
			Name:            input.Name,
			Version:         input.Version,
			PipelineID:      build.PipelineID(),
			FirstOccurrence: input.FirstOccurrence,
		}
	}
	for i, output := range outputs {
		expected.Outputs[i] = atc.PublicBuildOutput{
			Name:    output.Name,
			Version: output.Version,
		}
	}
	return expected
}

func buildsAPIExpectResourcesResponse(response *http.Response, expectedBuild db.BuildForAPI) atc.BuildInputsOutputs {
	GinkgoHelper()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())

	var actual atc.BuildInputsOutputs
	Expect(json.Unmarshal(body, &actual)).To(Succeed())

	expectedJSON, err := json.Marshal(buildsAPIExpectedResources(expectedBuild))
	Expect(err).NotTo(HaveOccurred())
	Expect(body).To(MatchJSON(expectedJSON))
	return actual
}

func buildsAPIExpectConstructedEventBuild(
	expectedBuild db.BuildForAPI,
	expectedTeam db.Team,
	expectedPipeline db.Pipeline,
	expectedJob db.Job,
) {
	GinkgoHelper()

	constructedEventHandler.lock.Lock()
	defer constructedEventHandler.lock.Unlock()

	actual := constructedEventHandler.build
	Expect(actual).NotTo(BeNil())
	Expect(actual.ID()).To(Equal(expectedBuild.ID()))
	Expect(actual.TeamID()).To(Equal(expectedTeam.ID()))
	Expect(actual.TeamName()).To(Equal(expectedTeam.Name()))
	Expect(actual.PipelineID()).To(Equal(expectedPipeline.ID()))
	Expect(actual.PipelineName()).To(Equal(expectedPipeline.Name()))
	Expect(actual.JobID()).To(Equal(expectedJob.ID()))
	Expect(actual.JobName()).To(Equal(expectedJob.Name()))
}

var _ = Describe("Builds API", func() {

	Describe("POST /api/v1/builds", func() {
		var (
			database   *realDB
			deps       apiDBDeps
			team       db.Team
			teamState  *buildsAPITeamState
			buildCount int
			plan       atc.Plan
			server     *httptest.Server
			response   *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps

			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())

			teamState = &buildsAPITeamState{}
			deps.teamFactory = buildsAPITeamFactory{
				TeamFactory: deps.teamFactory,
				teamName:    team.Name(),
				state:       teamState,
			}

			buildCount = len(buildsAPIRequireTeamBuilds(team))

			plan = atc.Plan{
				Task: &atc.TaskPlan{
					Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{
							Path: "ls",
						},
					},
				},
			}
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			reqPayload, err := json.Marshal(plan)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("POST", server.URL+"/api/v1/teams/some-team/builds", bytes.NewBuffer(reqPayload))
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("does not trigger a build", func() {
				Expect(buildsAPIRequireTeamBuilds(team)).To(HaveLen(buildCount))
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when creating a started build fails", func() {
					BeforeEach(func() {
						teamState.setCreateStartedBuildError(errors.New("oh no!"))
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						Expect(buildsAPIRequireTeamBuilds(team)).To(HaveLen(buildCount))
					})
				})

				Context("when creating a started build succeeds", func() {
				})
			})
		})
	})

	Describe("GET /api/v1/builds", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			listState        *buildsAPIBuildFactoryState

			publicBuilds          []db.BuildForAPI
			sameTeamPrivateBuild  db.BuildForAPI
			crossTeamPrivateBuild db.BuildForAPI
			unauthenticatedBuilds []db.BuildForAPI
			authenticatedBuilds   []db.BuildForAPI
			adminBuilds           []db.BuildForAPI
			buildStartTimes       []time.Time

			queryParams string
			server      *httptest.Server
			response    *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			someTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			otherTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "other-team"})
			Expect(err).NotTo(HaveOccurred())

			publicPipeline := database.SavePipeline(otherTeam, "public-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "public-job"}},
				Resources: atc.ResourceConfigs{{
					Name:   "public-resource",
					Type:   "mock",
					Source: atc.Source{"repository": "public"},
				}},
			})
			Expect(publicPipeline.Expose()).To(Succeed())
			reloaded, err := publicPipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			sameTeamPrivatePipeline := database.SavePipeline(someTeam, "same-team-private-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "same-team-private-job"}},
			})
			Expect(sameTeamPrivatePipeline.Hide()).To(Succeed())
			reloaded, err = sameTeamPrivatePipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			crossTeamPrivatePipeline := database.SavePipeline(otherTeam, "cross-team-private-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "cross-team-private-job"}},
			})
			Expect(crossTeamPrivatePipeline.Hide()).To(Succeed())
			reloaded, err = crossTeamPrivatePipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())

			publicJob, found, err := publicPipeline.Job("public-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			publicResource, found, err := publicPipeline.Resource("public-resource")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			sameTeamPrivateJob, found, err := sameTeamPrivatePipeline.Job("same-team-private-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			crossTeamPrivateJob, found, err := crossTeamPrivatePipeline.Job("cross-team-private-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			taskPlan := func(id atc.PlanID, path string) atc.Plan {
				return atc.Plan{
					ID: id,
					Task: &atc.TaskPlan{Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{Path: path},
					}},
				}
			}

			publicBuildRows := []db.Build{
				buildsAPIStartJobBuild(publicJob, "public-user-1", taskPlan("public-job-1", "public-one"), db.BuildStatusSucceeded),
				buildsAPIStartJobBuild(publicJob, "public-user-2", taskPlan("public-job-2", "public-two"), db.BuildStatusStarted),
				buildsAPICreateCheckBuild(publicResource, "public-check"),
				buildsAPIStartJobBuild(publicJob, "public-user-3", taskPlan("public-job-3", "public-three"), db.BuildStatusFailed),
			}
			sameTeamPrivateRow := buildsAPIStartJobBuild(
				sameTeamPrivateJob,
				"same-team-user",
				taskPlan("same-team-private", "same-team"),
				db.BuildStatusStarted,
			)
			crossTeamPrivateRow := buildsAPIStartJobBuild(
				crossTeamPrivateJob,
				"cross-team-user",
				taskPlan("cross-team-private", "cross-team"),
				db.BuildStatusSucceeded,
			)

			allRows := append(append([]db.Build{}, publicBuildRows...), sameTeamPrivateRow, crossTeamPrivateRow)
			startBase := time.Date(2020, time.January, 2, 3, 4, 0, 0, time.UTC)
			buildStartTimes = make([]time.Time, len(allRows))
			for i, build := range allRows {
				buildStartTimes[i] = startBase.Add(time.Duration(i) * time.Minute)
				result, err := database.Conn.Exec(
					`UPDATE builds SET start_time = $1 WHERE id = $2`,
					buildStartTimes[i],
					build.ID(),
				)
				Expect(err).NotTo(HaveOccurred())
				rowsAffected, err := result.RowsAffected()
				Expect(err).NotTo(HaveOccurred())
				Expect(rowsAffected).To(Equal(int64(1)))
			}

			publicBuilds = make([]db.BuildForAPI, len(publicBuildRows))
			for i, build := range publicBuildRows {
				publicBuilds[i] = buildsAPIRequireBuildForAPI(realBuildFactory, build.ID())
			}
			sameTeamPrivateBuild = buildsAPIRequireBuildForAPI(realBuildFactory, sameTeamPrivateRow.ID())
			crossTeamPrivateBuild = buildsAPIRequireBuildForAPI(realBuildFactory, crossTeamPrivateRow.ID())

			unauthenticatedBuilds = []db.BuildForAPI{
				publicBuilds[3], publicBuilds[2], publicBuilds[1], publicBuilds[0],
			}
			authenticatedBuilds = append([]db.BuildForAPI{sameTeamPrivateBuild}, unauthenticatedBuilds...)
			adminBuilds = append([]db.BuildForAPI{crossTeamPrivateBuild}, authenticatedBuilds...)

			Expect(publicPipeline.Public()).To(BeTrue())
			Expect(sameTeamPrivatePipeline.Public()).To(BeFalse())
			Expect(crossTeamPrivatePipeline.Public()).To(BeFalse())

			listState = &buildsAPIBuildFactoryState{}
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        listState,
			}

			queryParams = ""
			fakeAccess.TeamNamesReturns(nil)
			fakeAccess.IsAdminReturns(false)
		})

		JustBeforeEach(func() {
			var err error

			database.Deps = deps
			server = database.Serve()
			response, err = client.Get(server.URL + "/api/v1/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			Context("when no params are passed", func() {
				BeforeEach(func() {
					queryParams = ""
				})

				It("does not set defaults for since and until", func() {
					calls := listState.visibleBuildsCalls()
					Expect(calls).To(HaveLen(1))
					Expect(calls[0].page).To(Equal(db.Page{
						Limit: 100,
					}))
					Expect(calls[0].teamNames).To(BeEmpty())
				})
			})

			Context("when all the params are passed", func() {
				BeforeEach(func() {
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=8",
						publicBuilds[1].ID(),
						publicBuilds[2].ID(),
					)
				})

				It("passes them through", func() {
					calls := listState.visibleBuildsCalls()
					Expect(calls).To(HaveLen(1))
					Expect(calls[0].page).To(Equal(db.Page{
						From:  db.NewIntPtr(publicBuilds[1].ID()),
						To:    db.NewIntPtr(publicBuilds[2].ID()),
						Limit: 8,
					}))
					buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[1], publicBuilds[2]})
				})

				Context("timestamp is provided", func() {
					BeforeEach(func() {
						queryParams = fmt.Sprintf(
							"?from=%d&to=%d&timestamps=true",
							buildStartTimes[1].Unix(),
							buildStartTimes[2].Unix(),
						)
					})

					It("calls AllBuilds", func() {
						calls := listState.visibleBuildsCalls()
						Expect(calls).To(HaveLen(1))
						Expect(calls[0].page).To(Equal(db.Page{
							From:    db.NewIntPtr(int(buildStartTimes[1].Unix())),
							To:      db.NewIntPtr(int(buildStartTimes[2].Unix())),
							Limit:   100,
							UseDate: true,
						}))
						buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[2], publicBuilds[1]})
						Expect(response.Header.Values("Link")).To(BeEmpty())
					})
				})
			})

			Context("when getting the builds succeeds", func() {
				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns all builds", func() {
					buildsAPIExpectBuildsResponse(response, unauthenticatedBuilds)
				})
			})

			Context("when next/previous pages are available", func() {
				BeforeEach(func() {
					fakeAccess.TeamNamesReturns(nil)
					queryParams = fmt.Sprintf("?from=%d&limit=2", publicBuilds[1].ID())
				})

				It("returns Link headers per rfc5988", func() {
					buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[2], publicBuilds[1]})
					Expect(response.Header["Link"]).To(ConsistOf([]string{
						fmt.Sprintf(`<%s/api/v1/builds?from=%d&limit=2>; rel="previous"`, externalURL, publicBuilds[3].ID()),
						fmt.Sprintf(`<%s/api/v1/builds?to=%d&limit=2>; rel="next"`, externalURL, publicBuilds[0].ID()),
					}))
				})
			})

			Context("when getting all builds fails", func() {
				BeforeEach(func() {
					listState.setVisibleBuildsError(errors.New("oh no!"))
				})

				It("returns 500 Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.TeamNamesReturns([]string{"some-team"})
			})

			Context("when user has the admin privilege", func() {
				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
				})

				It("calls AllBuilds", func() {
					Expect(listState.allBuildsCalls()).To(ConsistOf(db.Page{Limit: 100}))
					Expect(listState.visibleBuildsCalls()).To(BeEmpty())
					builds := buildsAPIExpectBuildsResponse(response, adminBuilds)
					Expect(builds[0].ID).To(Equal(crossTeamPrivateBuild.ID()))
				})

			})

			Context("when no params are passed", func() {
				BeforeEach(func() {
					queryParams = ""
				})

				It("does not set defaults for since and until", func() {
					calls := listState.visibleBuildsCalls()
					Expect(calls).To(HaveLen(1))
					Expect(calls[0].page).To(Equal(db.Page{
						Limit: 100,
					}))
				})
			})

			Context("when all the params are passed", func() {
				BeforeEach(func() {
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=8",
						publicBuilds[1].ID(),
						publicBuilds[2].ID(),
					)
				})

				It("passes them through", func() {
					calls := listState.visibleBuildsCalls()
					Expect(calls).To(HaveLen(1))
					Expect(calls[0].page).To(Equal(db.Page{
						From:  db.NewIntPtr(publicBuilds[1].ID()),
						To:    db.NewIntPtr(publicBuilds[2].ID()),
						Limit: 8,
					}))
					buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[1], publicBuilds[2]})
				})
			})

			Context("when getting the builds succeeds", func() {
				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns all builds", func() {
					buildsAPIExpectBuildsResponse(response, authenticatedBuilds)
				})

				It("returns builds for teams from the token", func() {
					calls := listState.visibleBuildsCalls()
					Expect(calls).To(HaveLen(1))
					Expect(calls[0].teamNames).To(ConsistOf("some-team"))
					builds := buildsAPIExpectBuildsResponse(response, authenticatedBuilds)
					Expect(builds[0].ID).To(Equal(sameTeamPrivateBuild.ID()))
				})
			})

			Context("when next/previous pages are available", func() {
				BeforeEach(func() {
					fakeAccess.TeamNamesReturns(nil)
					queryParams = fmt.Sprintf("?from=%d&limit=2", publicBuilds[1].ID())
				})

				It("returns Link headers per rfc5988", func() {
					buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[2], publicBuilds[1]})
					Expect(response.Header["Link"]).To(ConsistOf([]string{
						fmt.Sprintf(`<%s/api/v1/builds?from=%d&limit=2>; rel="previous"`, externalURL, publicBuilds[3].ID()),
						fmt.Sprintf(`<%s/api/v1/builds?to=%d&limit=2>; rel="next"`, externalURL, publicBuilds[0].ID()),
					}))
				})
			})

			Context("when getting all builds fails", func() {
				BeforeEach(func() {
					listState.setVisibleBuildsError(errors.New("oh no!"))
				})

				It("returns 500 Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState

			team           db.Team
			pipeline       db.Pipeline
			persistedBuild db.BuildForAPI
			missingBuildID int
			requestBuildID string

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			var err error
			var found bool
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline = database.SavePipeline(team, "pipeline1", atc.Config{
				Jobs: atc.JobConfigs{{Name: "job1"}},
			})
			Expect(pipeline.Expose()).To(Succeed())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Public()).To(BeTrue())

			job, found, err := pipeline.Job("job1")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			build := buildsAPIStartJobBuild(job, "detail-user", atc.Plan{
				ID: "detail-task",
				Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "detail-task"},
				}},
			}, db.BuildStatusSucceeded)
			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, build.ID())
			Expect(persistedBuild.Status()).To(Equal(db.BuildStatusSucceeded))
			Expect(persistedBuild.StartTime()).NotTo(BeZero())
			Expect(persistedBuild.EndTime()).NotTo(BeZero())

			missingBuildID = persistedBuild.ID() + 1_000_000
			_, found, err = realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			buildState = &buildsAPIBuildState{}
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(build db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: build, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			var err error
			response, err = client.Get(server.URL + "/api/v1/builds/" + requestBuildID)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when parsing the build_id fails", func() {
			BeforeEach(func() {
				requestBuildID = "nope"
			})

			It("returns Bad Request", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when parsing the build_id succeeds", func() {
			Context("when calling the database fails", func() {
				BeforeEach(func() {
					factoryState.setBuildForAPIError(errors.New("disaster"))
				})

				It("returns 500 Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the build cannot be found", func() {
				BeforeEach(func() {
					requestBuildID = fmt.Sprintf("%d", missingBuildID)
				})

				It("returns Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the build can be found", func() {
				Context("when not authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(false)
						fakeAccess.IsAuthorizedReturns(false)
					})

					Context("and build is one off", func() {
						BeforeEach(func() {
							oneOffBuild, err := team.CreateOneOffBuild()
							Expect(err).NotTo(HaveOccurred())
							persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuild.ID())
							Expect(persistedBuild.PipelineID()).To(BeZero())
							requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
						})

					})

					Context("and the pipeline is not found", func() {
						BeforeEach(func() {
							buildState.setPipelineResult(nil, false, nil)
						})

						It("returns 404", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})

					Context("and the pipeline is private", func() {
						BeforeEach(func() {
							Expect(pipeline.Hide()).To(Succeed())
							reloaded, err := pipeline.Reload()
							Expect(err).NotTo(HaveOccurred())
							Expect(reloaded).To(BeTrue())
							Expect(pipeline.Public()).To(BeFalse())
						})

					})

					Context("and the pipeline is public", func() {
						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})
				})

				Context("when authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(true)
					})

					Context("when user is not authorized", func() {
						BeforeEach(func() {
							fakeAccess.IsAuthorizedReturns(false)

						})
						It("returns 200 OK", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})

					Context("when user is authorized", func() {
						BeforeEach(func() {
							fakeAccess.IsAuthorizedReturns(true)
						})

						It("returns 200 OK", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

					})
				})
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id/resources", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState
			builder          dbtest.Builder
			scenario         *dbtest.Scenario

			team             db.Team
			pipeline         db.Pipeline
			persistedBuild   db.BuildForAPI
			decoyBuild       db.BuildForAPI
			missingBuildID   int
			requestBuildID   string
			inputOneVersion  atc.Version
			inputTwoVersion  atc.Version
			outputOneVersion atc.Version
			outputTwoVersion atc.Version
			decoyInput       atc.Version
			decoyOutput      atc.Version

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			var err error
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline = database.SavePipeline(team, "resource-pipeline", atc.Config{
				Jobs: atc.JobConfigs{
					{Name: "empty-job"},
					{
						Name: "target-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "input1", Resource: "input-resource-1"}},
							{Config: &atc.GetStep{Name: "input2", Resource: "input-resource-2"}},
							{Config: &atc.PutStep{Name: "myresource3", Resource: "output-resource-3"}},
							{Config: &atc.PutStep{Name: "myresource4", Resource: "output-resource-4"}},
						},
					},
					{
						Name: "decoy-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "decoy-input", Resource: "decoy-input-resource"}},
							{Config: &atc.PutStep{Name: "decoy-output", Resource: "decoy-output-resource"}},
						},
					},
				},
				Resources: atc.ResourceConfigs{
					{Name: "input-resource-1", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "input-1"}},
					{Name: "input-resource-2", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "input-2"}},
					{Name: "output-resource-3", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "output-3"}},
					{Name: "output-resource-4", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "output-4"}},
					{Name: "decoy-input-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "decoy-input"}},
					{Name: "decoy-output-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "decoy-output"}},
				},
			})
			Expect(pipeline.Hide()).To(Succeed())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Public()).To(BeFalse())

			builder = dbtest.NewBuilder(database.Conn, database.LockFactory)
			scenario = &dbtest.Scenario{Team: team, Pipeline: pipeline}
			emptyBuild, err := scenario.Job("empty-job").CreateBuild("resource-user")
			Expect(err).NotTo(HaveOccurred())
			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, emptyBuild.ID())
			emptyResources := buildsAPIExpectedResources(persistedBuild)
			Expect(emptyResources.Inputs).To(BeEmpty())
			Expect(emptyResources.Outputs).To(BeEmpty())

			missingBuildID = persistedBuild.ID() + 1_000_000
			_, found, err := realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			buildState = &buildsAPIBuildState{}
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(build db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: build, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			inputOneVersion = atc.Version{"version": "value1"}
			inputTwoVersion = atc.Version{"version": "value2"}
			outputOneVersion = atc.Version{"version": "value3"}
			outputTwoVersion = atc.Version{"version": "value4"}
			decoyInput = atc.Version{"version": "decoy-input"}
			decoyOutput = atc.Version{"version": "decoy-output"}
			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			var err error
			response, err = client.Get(server.URL + "/api/v1/builds/" + requestBuildID + "/resources")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when the build is found", func() {
			Context("when not authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(false)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						oneOffBuild, err := team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())
						persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuild.ID())
						Expect(persistedBuild.PipelineID()).To(BeZero())
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

				})

				Context("and the pipeline is private", func() {
				})

				Context("and the pipeline is public", func() {
					BeforeEach(func() {
						Expect(pipeline.Expose()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeTrue())
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})
				})
			})

			Context("when authenticated, but not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(false)
				})

			})

			Context("when authenticated and authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(true)
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					buildsAPIExpectResourcesResponse(response, persistedBuild)
				})

				Context("when the build inputs/outputs are not empty", func() {
					BeforeEach(func() {
						var targetBuild db.Build
						var decoyBuildRow db.Build
						scenario.Run(
							builder.WithResourceVersions("input-resource-1", inputOneVersion),
							builder.WithResourceVersions("input-resource-2", inputTwoVersion),
							builder.WithJobBuild(&targetBuild, "target-job", dbtest.JobInputs{
								{Name: "input1", Version: inputOneVersion, FirstOccurrence: true},
								{Name: "input2", Version: inputTwoVersion, FirstOccurrence: false},
							}, dbtest.JobOutputs{
								"myresource3": outputOneVersion,
								"myresource4": outputTwoVersion,
							}),
							builder.WithResourceVersions("decoy-input-resource", decoyInput),
							builder.WithJobBuild(&decoyBuildRow, "decoy-job", dbtest.JobInputs{
								{Name: "decoy-input", Version: decoyInput, FirstOccurrence: true},
							}, dbtest.JobOutputs{
								"decoy-output": decoyOutput,
							}),
						)
						persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, targetBuild.ID())
						decoyBuild = buildsAPIRequireBuildForAPI(realBuildFactory, decoyBuildRow.ID())
						Expect(decoyBuild.ID()).NotTo(Equal(persistedBuild.ID()))
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns the build with it's input and output versioned resources", func() {
						expectedInputs := []atc.PublicBuildInput{
							{Name: "input1", Version: inputOneVersion, PipelineID: pipeline.ID(), FirstOccurrence: true},
							{Name: "input2", Version: inputTwoVersion, PipelineID: pipeline.ID(), FirstOccurrence: false},
						}
						expectedOutputs := []atc.PublicBuildOutput{
							{Name: "myresource3", Version: outputOneVersion},
							{Name: "myresource4", Version: outputTwoVersion},
						}
						targetResources := buildsAPIExpectedResources(persistedBuild)
						Expect(targetResources.Inputs).To(ConsistOf(expectedInputs))
						Expect(targetResources.Outputs).To(ConsistOf(expectedOutputs))

						decoyResources := buildsAPIExpectedResources(decoyBuild)
						Expect(decoyResources.Inputs).To(ConsistOf(atc.PublicBuildInput{
							Name: "decoy-input", Version: decoyInput, PipelineID: pipeline.ID(), FirstOccurrence: true,
						}))
						Expect(decoyResources.Outputs).To(ConsistOf(atc.PublicBuildOutput{
							Name: "decoy-output", Version: decoyOutput,
						}))

						actual := buildsAPIExpectResourcesResponse(response, persistedBuild)
						Expect(actual.Inputs).To(ConsistOf(expectedInputs))
						Expect(actual.Outputs).To(ConsistOf(expectedOutputs))
					})
				})

				Context("when the build resources error", func() {
					BeforeEach(func() {
						buildState.setResourcesError(errors.New("where are my feedback?"))
					})

					It("returns internal server error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("with an invalid build", func() {
					Context("when the lookup errors", func() {
						BeforeEach(func() {
							factoryState.setBuildForAPIError(errors.New("Freakin' out man, I'm freakin' out!"))
						})

						It("returns internal server error", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when the build does not exist", func() {
						BeforeEach(func() {
							requestBuildID = fmt.Sprintf("%d", missingBuildID)
						})

						It("returns internal server error", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})
				})
			})
		})

		Context("with an invalid build_id", func() {
			BeforeEach(func() {
				requestBuildID = "nope"
			})

			It("returns internal server error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id/events", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState
			pipelineState    *buildsAPIPipelineState

			team           db.Team
			pipeline       db.Pipeline
			privateJob     db.Job
			publicJob      db.Job
			persistedBuild db.BuildForAPI
			publicBuild    db.BuildForAPI
			persistedJob   db.Job
			missingBuildID int
			requestBuildID string

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			var err error
			var found bool
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline = database.SavePipeline(team, "events-pipeline", atc.Config{
				Jobs: atc.JobConfigs{
					{Name: "private-job"},
					{Name: "public-job", Public: true},
				},
			})
			Expect(pipeline.Hide()).To(Succeed())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Public()).To(BeFalse())

			privateJob, found, err = pipeline.Job("private-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(privateJob.Public()).To(BeFalse())
			publicJob, found, err = pipeline.Job("public-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(publicJob.Public()).To(BeTrue())

			privateBuildRow := buildsAPIStartJobBuild(privateJob, "events-private-user", atc.Plan{
				ID: "events-private-task",
				Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "events-private-task"},
				}},
			}, db.BuildStatusSucceeded)
			publicBuildRow := buildsAPIStartJobBuild(publicJob, "events-public-user", atc.Plan{
				ID: "events-public-task",
				Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "events-public-task"},
				}},
			}, db.BuildStatusSucceeded)
			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, privateBuildRow.ID())
			publicBuild = buildsAPIRequireBuildForAPI(realBuildFactory, publicBuildRow.ID())
			persistedJob = privateJob
			Expect(persistedBuild.ID()).NotTo(Equal(publicBuild.ID()))
			Expect(persistedBuild.PipelineID()).To(Equal(pipeline.ID()))
			Expect(publicBuild.PipelineID()).To(Equal(pipeline.ID()))

			missingBuildID = publicBuild.ID() + 1_000_000
			_, found, err = realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			pipelineState = &buildsAPIPipelineState{}
			buildState = &buildsAPIBuildState{}
			buildState.setPipelineWrapper(func(realPipeline db.Pipeline) db.Pipeline {
				return &buildsAPIPipeline{Pipeline: realPipeline, state: pipelineState}
			})
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(realBuild db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: realBuild, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			request, err := http.NewRequest("GET", server.URL+"/api/v1/builds/"+requestBuildID+"/events", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when the build can be found", func() {
			Context("when authenticated, but not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(false)
				})

			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(true)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(200))
				})

				It("serves the request via the event handler", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal("fake event handler factory was here"))
					buildsAPIExpectConstructedEventBuild(persistedBuild, team, pipeline, persistedJob)
					Expect(factoryState.buildForAPICalls()).To(Equal([]int{persistedBuild.ID()}))
				})
			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(false)
				})

				Context("and the pipeline is private", func() {
				})

				Context("and the pipeline is public", func() {
					BeforeEach(func() {
						Expect(pipeline.Expose()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeTrue())
					})

					Context("when the job is found", func() {
						Context("and the job is private", func() {
						})

						Context("and the job is public", func() {
							BeforeEach(func() {
								persistedBuild = publicBuild
								persistedJob = publicJob
								requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
							})

							It("returns 200", func() {
								Expect(response.StatusCode).To(Equal(200))
							})

							It("serves the request via the event handler", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(string(body)).To(Equal("fake event handler factory was here"))
								buildsAPIExpectConstructedEventBuild(persistedBuild, team, pipeline, persistedJob)
								Expect(factoryState.buildForAPICalls()).To(Equal([]int{persistedBuild.ID()}))
							})
						})
					})

					Context("when finding the job fails", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(privateJob.Name(), nil, false, errors.New("nope"))
						})

						It("returns Internal Server Error", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when the job cannot be found", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(privateJob.Name(), nil, false, nil)
						})

						It("returns Not Found", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})
				})

				Context("when the build can not be found", func() {
					BeforeEach(func() {
						requestBuildID = fmt.Sprintf("%d", missingBuildID)
					})

					It("returns Not Found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when calling the database fails", func() {
					BeforeEach(func() {
						factoryState.setBuildForAPIError(errors.New("nope"))
					})

					It("returns Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})
		})

		Context("when calling the database fails", func() {
			BeforeEach(func() {
				factoryState.setBuildForAPIError(errors.New("nope"))
			})

			It("returns Internal Server Error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})

	Describe("PUT /api/v1/builds/:build_id/abort", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState

			persistedBuild db.BuildForAPI
			missingBuildID int
			requestBuildID string

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			team, err := deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			oneOffBuild, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			started, err := oneOffBuild.Start(atc.Plan{
				ID: "abort-task",
				Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "abort-task"},
				}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())
			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuild.ID())
			Expect(persistedBuild.PipelineID()).To(BeZero())
			Expect(persistedBuild.Status()).To(Equal(db.BuildStatusStarted))
			Expect(buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID()).IsAborted()).To(BeFalse())

			missingBuildID = persistedBuild.ID() + 1_000_000
			_, found, err := realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			buildState = &buildsAPIBuildState{}
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(realBuild db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: realBuild, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			req, err := http.NewRequest("PUT", server.URL+"/api/v1/builds/"+requestBuildID+"/abort", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when looking up the build fails", func() {
				BeforeEach(func() {
					factoryState.setBuildForAPIError(errors.New("nope"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
					Expect(reloaded.IsAborted()).To(BeFalse())
				})
			})

			Context("when the build can not be found", func() {
				BeforeEach(func() {
					requestBuildID = fmt.Sprintf("%d", missingBuildID)
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
					Expect(reloaded.IsAborted()).To(BeFalse())
				})
			})

			Context("when the build is found", func() {
				Context("when not authorized", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthorizedReturns(false)
					})

				})

				Context("when authorized", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthorizedReturns(true)
					})

					Context("when aborting the build fails", func() {
						BeforeEach(func() {
							buildState.setMarkAsAbortedError(errors.New("nope"))
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
							Expect(reloaded.IsAborted()).To(BeFalse())
						})
					})

					Context("when aborting succeeds", func() {
					})
				})
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id/preparation", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState
			pipelineState    *buildsAPIPipelineState
			builder          dbtest.Builder
			scenario         *dbtest.Scenario

			team              db.Team
			pipeline          db.Pipeline
			preparationJob    db.Job
			publicJob         db.Job
			persistedBuild    db.BuildForAPI
			publicBuild       db.BuildForAPI
			oneOffBuild       db.BuildForAPI
			expectedBuildPrep db.BuildPreparation
			missingBuildID    int
			requestBuildID    string

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			var err error
			var found bool
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline = database.SavePipeline(team, "preparation-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "ready-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "ready"}},
					{Name: "errored-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "errored"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name:           "preparation-job",
						RawMaxInFlight: 1,
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "ready-input", Resource: "ready-resource"}},
							{Config: &atc.GetStep{Name: "errored-input", Resource: "errored-resource"}},
						},
					},
					{Name: "public-job", Public: true},
				},
			})
			Expect(pipeline.Hide()).To(Succeed())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Public()).To(BeFalse())

			preparationJob, found, err = pipeline.Job("preparation-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(preparationJob.Public()).To(BeFalse())
			publicJob, found, err = pipeline.Job("public-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(publicJob.Public()).To(BeTrue())

			builder = dbtest.NewBuilder(database.Conn, database.LockFactory)
			scenario = &dbtest.Scenario{Team: team, Pipeline: pipeline}
			readyVersion := atc.Version{"version": "ready-version"}
			erroredVersion := atc.Version{"version": "errored-version"}
			scenario.Run(
				builder.WithResourceVersions("ready-resource", readyVersion),
				builder.WithResourceVersions("errored-resource", erroredVersion),
			)

			var firstBuild db.Build
			var targetBuild db.Build
			scenario.Run(
				builder.WithPendingJobBuild(&firstBuild, preparationJob.Name()),
				builder.WithPendingJobBuild(&targetBuild, preparationJob.Name()),
				builder.WithNextInputMapping(preparationJob.Name(), dbtest.JobInputs{
					{Name: "ready-input", Version: readyVersion},
					{Name: "errored-input", Version: erroredVersion},
				}),
			)

			scheduled, err := preparationJob.ScheduleBuild(firstBuild)
			Expect(err).NotTo(HaveOccurred())
			Expect(scheduled).To(BeTrue())
			scheduled, err = preparationJob.ScheduleBuild(targetBuild)
			Expect(err).NotTo(HaveOccurred())
			Expect(scheduled).To(BeFalse())

			scenario.Run(
				builder.WithNextInputMapping(preparationJob.Name(), dbtest.JobInputs{
					{Name: "ready-input", Version: readyVersion},
					{Name: "errored-input", Version: erroredVersion, ResolveError: "resolve error"},
				}),
				// The successful check happens after target creation, so the
				// manually triggered ready input is not blocked on a fresh check.
				builder.WithResourceVersions("ready-resource"),
			)
			Expect(pipeline.Pause("preparation-test")).To(Succeed())
			Expect(preparationJob.Pause("preparation-test")).To(Succeed())

			publicBuildRow := buildsAPIStartJobBuild(publicJob, "preparation-public-user", atc.Plan{
				ID: "preparation-public-task",
				Task: &atc.TaskPlan{
					Name: "preparation-public-task",
					Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{Path: "preparation-public-task"},
					},
				},
			}, db.BuildStatusStarted)
			oneOffBuildRow, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, targetBuild.ID())
			publicBuild = buildsAPIRequireBuildForAPI(realBuildFactory, publicBuildRow.ID())
			oneOffBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuildRow.ID())
			Expect(persistedBuild.ID()).NotTo(Equal(publicBuild.ID()))
			Expect(oneOffBuild.PipelineID()).To(BeZero())

			expectedBuildPrep = db.BuildPreparation{
				BuildID:          persistedBuild.ID(),
				PausedPipeline:   db.BuildPreparationStatusBlocking,
				PausedJob:        db.BuildPreparationStatusBlocking,
				MaxRunningBuilds: db.BuildPreparationStatusBlocking,
				Inputs: map[string]db.BuildPreparationStatus{
					"ready-input":   db.BuildPreparationStatusNotBlocking,
					"errored-input": db.BuildPreparationStatusBlocking,
				},
				InputsSatisfied: db.BuildPreparationStatusBlocking,
				MissingInputReasons: db.MissingInputReasons{
					"errored-input": "resolve error",
				},
			}
			actualBuildPrep, found, err := persistedBuild.Preparation()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(actualBuildPrep).To(Equal(expectedBuildPrep))

			missingBuildID = oneOffBuild.ID() + 1_000_000
			_, found, err = realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			pipelineState = &buildsAPIPipelineState{}
			buildState = &buildsAPIBuildState{}
			buildState.setPipelineWrapper(func(realPipeline db.Pipeline) db.Pipeline {
				return &buildsAPIPipeline{Pipeline: realPipeline, state: pipelineState}
			})
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(realBuild db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: realBuild, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			var err error
			response, err = client.Get(server.URL + "/api/v1/builds/" + requestBuildID + "/preparation")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when the build is found", func() {
			Context("when authenticated, but not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(false)
				})

			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(false)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						persistedBuild = oneOffBuild
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

				})

				Context("and the pipeline is private", func() {
				})

				Context("and the pipeline is public", func() {
					BeforeEach(func() {
						Expect(pipeline.Expose()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeTrue())
					})

					Context("when the job is found", func() {
						Context("when job is private", func() {
						})

						Context("when job is public", func() {
							BeforeEach(func() {
								persistedBuild = publicBuild
								requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
							})

							It("returns 200", func() {
								Expect(response.StatusCode).To(Equal(http.StatusOK))
							})
						})
					})

					Context("when finding the job fails", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(preparationJob.Name(), nil, false, errors.New("nope"))
						})

						It("returns Internal Server Error", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when the job cannot be found", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(preparationJob.Name(), nil, false, nil)
						})

						It("returns Not Found", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})
				})
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(true)
				})

				It("fetches data from the db", func() {
					actual, found, err := persistedBuild.Preparation()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(actual).To(Equal(expectedBuildPrep))
				})

				It("returns OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns the build preparation", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					expected := atc.BuildPreparation{
						BuildID:          persistedBuild.ID(),
						PausedPipeline:   atc.BuildPreparationStatusBlocking,
						PausedJob:        atc.BuildPreparationStatusBlocking,
						MaxRunningBuilds: atc.BuildPreparationStatusBlocking,
						Inputs: map[string]atc.BuildPreparationStatus{
							"ready-input":   atc.BuildPreparationStatusNotBlocking,
							"errored-input": atc.BuildPreparationStatusBlocking,
						},
						InputsSatisfied: atc.BuildPreparationStatusBlocking,
						MissingInputReasons: atc.MissingInputReasons{
							"errored-input": "resolve error",
						},
					}
					expectedJSON, err := json.Marshal(expected)
					Expect(err).NotTo(HaveOccurred())
					Expect(body).To(MatchJSON(expectedJSON))
				})

				Context("when the build preparation is not found", func() {
					BeforeEach(func() {
						buildState.setPreparationResult(db.BuildPreparation{}, false, nil)
					})

					It("returns Not Found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when looking up the build preparation fails", func() {
					BeforeEach(func() {
						buildState.setPreparationResult(
							db.BuildPreparation{},
							false,
							errors.New("ho ho ho merry festivus"),
						)
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})
		})

		Context("when looking up the build fails", func() {
			BeforeEach(func() {
				factoryState.setBuildForAPIError(errors.New("ho ho ho merry festivus"))
			})

			It("returns 500 Internal Server Error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})

		Context("when build is not found", func() {
			BeforeEach(func() {
				requestBuildID = fmt.Sprintf("%d", missingBuildID)
			})

			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id/plan", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory
			factoryState     *buildsAPIBuildFactoryState
			buildState       *buildsAPIBuildState
			pipelineState    *buildsAPIPipelineState

			team              db.Team
			pipeline          db.Pipeline
			privateJob        db.Job
			publicJob         db.Job
			plan              atc.Plan
			persistedBuild    db.BuildForAPI
			publicBuild       db.BuildForAPI
			publicNoPlanBuild db.BuildForAPI
			oneOffBuild       db.BuildForAPI
			missingBuildID    int
			requestBuildID    string

			server   *httptest.Server
			response *http.Response
		)

		BeforeEach(func() {
			database = useRealDB()
			deps = database.Deps
			realBuildFactory = deps.buildFactory

			var err error
			var found bool
			team, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline = database.SavePipeline(team, "plan-pipeline", atc.Config{
				Jobs: atc.JobConfigs{
					{Name: "private-job"},
					{Name: "public-job", Public: true},
				},
			})
			Expect(pipeline.Hide()).To(Succeed())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Public()).To(BeFalse())

			privateJob, found, err = pipeline.Job("private-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(privateJob.Public()).To(BeFalse())
			publicJob, found, err = pipeline.Job("public-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(publicJob.Public()).To(BeTrue())

			plan = atc.Plan{
				ID: "plan-step",
				Task: &atc.TaskPlan{
					Name: "public-task",
					Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{Path: "private-task-path"},
					},
				},
			}
			privateBuildRow := buildsAPIStartJobBuild(
				privateJob,
				"plan-private-user",
				plan,
				db.BuildStatusStarted,
			)
			publicBuildRow := buildsAPIStartJobBuild(
				publicJob,
				"plan-public-user",
				plan,
				db.BuildStatusStarted,
			)
			publicNoPlanBuildRow, err := publicJob.CreateBuild("plan-public-no-plan-user")
			Expect(err).NotTo(HaveOccurred())
			oneOffBuildRow, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())

			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, privateBuildRow.ID())
			publicBuild = buildsAPIRequireBuildForAPI(realBuildFactory, publicBuildRow.ID())
			publicNoPlanBuild = buildsAPIRequireBuildForAPI(realBuildFactory, publicNoPlanBuildRow.ID())
			oneOffBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuildRow.ID())
			Expect(persistedBuild.Schema()).To(Equal("exec.v2"))
			Expect(persistedBuild.HasPlan()).To(BeTrue())
			Expect(persistedBuild.PublicPlan()).NotTo(BeNil())
			Expect(*persistedBuild.PublicPlan()).To(MatchJSON(*plan.Public()))
			Expect(publicNoPlanBuild.HasPlan()).To(BeFalse())
			Expect(oneOffBuild.HasPlan()).To(BeFalse())
			Expect(oneOffBuild.PipelineID()).To(BeZero())

			missingBuildID = oneOffBuild.ID() + 1_000_000
			_, found, err = realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			pipelineState = &buildsAPIPipelineState{}
			buildState = &buildsAPIBuildState{}
			buildState.setPipelineWrapper(func(realPipeline db.Pipeline) db.Pipeline {
				return &buildsAPIPipeline{Pipeline: realPipeline, state: pipelineState}
			})
			factoryState = &buildsAPIBuildFactoryState{}
			factoryState.setBuildWrapper(func(realBuild db.BuildForAPI) db.BuildForAPI {
				return &buildsAPIBuild{BuildForAPI: realBuild, state: buildState}
			})
			deps.buildFactory = &buildsAPIBuildFactory{
				BuildFactory: realBuildFactory,
				state:        factoryState,
			}

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			var err error
			response, err = client.Get(server.URL + "/api/v1/builds/" + requestBuildID + "/plan")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when the build is found", func() {
			Context("when authenticated, but not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(false)
				})

			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(false)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						persistedBuild = oneOffBuild
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

				})

				Context("and the pipeline is private", func() {
				})

				Context("and the pipeline is public", func() {
					BeforeEach(func() {
						Expect(pipeline.Expose()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeTrue())
					})

					Context("when finding the job fails", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(privateJob.Name(), nil, false, errors.New("nope"))
						})
						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when the job does not exist", func() {
						BeforeEach(func() {
							pipelineState.setJobResult(privateJob.Name(), nil, false, nil)
						})
						It("returns 404", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})

					Context("when the job exists", func() {
						Context("and the job is public", func() {
							BeforeEach(func() {
								persistedBuild = publicBuild
								requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
							})
							Context("and the build has a plan", func() {
								It("returns 200", func() {
									Expect(response.StatusCode).To(Equal(http.StatusOK))
								})
							})
							Context("and the build has no plan", func() {
								BeforeEach(func() {
									persistedBuild = publicNoPlanBuild
									requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
								})
								It("returns 404", func() {
									Expect(response.StatusCode).To(Equal(http.StatusNotFound))
								})
							})
						})

						Context("and the job is private", func() {
						})
					})
				})
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when the build returns a plan", func() {
					It("returns OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns the plan", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`{
						"schema": "exec.v2",
						"plan": {
							"id": "plan-step",
							"task": {
								"name": "public-task",
								"privileged": false,
								"hermetic": false
							}
						}
					}`))
						Expect(persistedBuild.Schema()).To(Equal("exec.v2"))
						Expect(*persistedBuild.PublicPlan()).To(MatchJSON(*plan.Public()))
					})
				})

				Context("when the build has no plan", func() {
					BeforeEach(func() {
						persistedBuild = oneOffBuild
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

					It("returns no Content-Type header", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "",
						}
						Expect(response).ShouldNot(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns not found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})
		})

		Context("when the build is not found", func() {
			BeforeEach(func() {
				requestBuildID = fmt.Sprintf("%d", missingBuildID)
			})

			It("returns Not Found", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})

		Context("when looking up the build fails", func() {
			BeforeEach(func() {
				factoryState.setBuildForAPIError(errors.New("oh no!"))
			})

			It("returns 500 Internal Server Error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})
})
