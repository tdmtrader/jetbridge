package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type jobsAPIFixture struct {
	Real     *realDB
	Team     db.Team
	Pipeline db.Pipeline
	Builder  dbtest.Builder
	Scenario *dbtest.Scenario
}

type jobsAPITeamFactory struct {
	db.TeamFactory
	teamName string
	team     db.Team
}

func (factory jobsAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	if name == factory.teamName {
		return factory.team, true, nil
	}
	return factory.TeamFactory.FindTeam(name)
}

type jobsAPITeam struct {
	db.Team
	pipeline db.Pipeline
}

func (team jobsAPITeam) Pipeline(ref atc.PipelineRef) (db.Pipeline, bool, error) {
	if ref.Name == team.pipeline.Name() && reflect.DeepEqual(ref.InstanceVars, team.pipeline.InstanceVars()) {
		return team.pipeline, true, nil
	}
	return team.Team.Pipeline(ref)
}

type jobsAPIPipeline struct {
	db.Pipeline
	jobName string
	job     db.Job
}

func (pipeline jobsAPIPipeline) Job(name string) (db.Job, bool, error) {
	if pipeline.job != nil && name == pipeline.jobName {
		return pipeline.job, true, nil
	}
	return pipeline.Pipeline.Job(name)
}

type outputsErrorJob struct {
	db.Job
	err error
}

func (job outputsErrorJob) Outputs() ([]atc.JobOutput, error) {
	return nil, job.err
}

type finishedAndNextErrorJob struct {
	db.Job
	err error
}

func (job finishedAndNextErrorJob) FinishedAndNextBuild() (db.Build, db.Build, error) {
	return nil, nil, job.err
}

type resourcesErrorPipeline struct {
	db.Pipeline
	err error
}

type dashboardErrorPipeline struct {
	db.Pipeline
	err error
}

func (pipeline dashboardErrorPipeline) Dashboard() ([]atc.JobSummary, error) {
	return nil, pipeline.err
}

func (pipeline resourcesErrorPipeline) Resources() (db.Resources, error) {
	return nil, pipeline.err
}

type resourceTypesErrorPipeline struct {
	db.Pipeline
	err error
}

func (pipeline resourceTypesErrorPipeline) ResourceTypes() (db.ResourceTypes, error) {
	return nil, pipeline.err
}

type inputsErrorJob struct {
	db.Job
	err error
}

func (job inputsErrorJob) Inputs() ([]atc.JobInput, error) {
	return nil, job.err
}

type configErrorJob struct {
	db.Job
	err error
}

func (job configErrorJob) Config() (atc.JobConfig, error) {
	return atc.JobConfig{}, job.err
}

type rerunErrorJob struct {
	db.Job
	err error
}

func (job rerunErrorJob) RerunBuild(db.Build, string) (db.Build, error) {
	return nil, job.err
}

type recordingJobsCheckCall struct {
	checkable               db.Checkable
	resourceTypes           db.ResourceTypes
	from                    atc.Version
	manuallyTriggered       bool
	skipIntervalRecursively bool
	toDB                    bool
	build                   db.Build
	created                 bool
	err                     error
}

type recordingJobsCheckFactory struct {
	db.CheckFactory
	mu    sync.Mutex
	calls []recordingJobsCheckCall
}

func (factory *recordingJobsCheckFactory) TryCreateCheck(
	ctx context.Context,
	checkable db.Checkable,
	resourceTypes db.ResourceTypes,
	from atc.Version,
	manuallyTriggered bool,
	skipIntervalRecursively bool,
	toDB bool,
) (db.Build, bool, error) {
	build, created, err := factory.CheckFactory.TryCreateCheck(
		ctx, checkable, resourceTypes, from,
		manuallyTriggered, skipIntervalRecursively, toDB,
	)
	factory.mu.Lock()
	factory.calls = append(factory.calls, recordingJobsCheckCall{
		checkable: checkable, resourceTypes: resourceTypes, from: from,
		manuallyTriggered:       manuallyTriggered,
		skipIntervalRecursively: skipIntervalRecursively,
		toDB:                    toDB, build: build, created: created, err: err,
	})
	factory.mu.Unlock()
	return build, created, err
}

func (factory *recordingJobsCheckFactory) Calls() []recordingJobsCheckCall {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]recordingJobsCheckCall(nil), factory.calls...)
}

func useJobsAPIFixture(ref atc.PipelineRef, config atc.Config) *jobsAPIFixture {
	GinkgoHelper()

	realdb := useRealDB()
	team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(ref, config, db.ConfigVersion(0), false)
	Expect(err).NotTo(HaveOccurred())
	builder := dbtest.NewBuilder(realdb.Conn, realdb.LockFactory)

	return &jobsAPIFixture{
		Real:     realdb,
		Team:     team,
		Pipeline: pipeline,
		Builder:  builder,
		Scenario: &dbtest.Scenario{Team: team, Pipeline: pipeline},
	}
}

func (fixture *jobsAPIFixture) Job(name string) db.Job {
	GinkgoHelper()

	job, found, err := fixture.Pipeline.Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return job
}

func (fixture *jobsAPIFixture) Serve() *httptest.Server {
	GinkgoHelper()
	return fixture.Real.Serve()
}

func (fixture *jobsAPIFixture) ServePipeline(pipeline db.Pipeline) *httptest.Server {
	GinkgoHelper()

	deps := fixture.Real.Deps
	deps.teamFactory = jobsAPITeamFactory{
		TeamFactory: deps.teamFactory,
		teamName:    fixture.Team.Name(),
		team: jobsAPITeam{
			Team: fixture.Team, pipeline: pipeline,
		},
	}
	server := newAPIServer(deps)
	DeferCleanup(server.Close)
	return server
}

func (fixture *jobsAPIFixture) doomedPipeline() db.Pipeline {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	teamFactory := db.NewTeamFactory(conn, fixture.Real.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.Team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name: fixture.Pipeline.Name(), InstanceVars: fixture.Pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(conn.Close()).To(Succeed())
	return pipeline
}

func (fixture *jobsAPIFixture) doomedJob(name string) db.Job {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	teamFactory := db.NewTeamFactory(conn, fixture.Real.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.Team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name: fixture.Pipeline.Name(), InstanceVars: fixture.Pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	job, found, err := pipeline.Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(conn.Close()).To(Succeed())
	return job
}

func createJobsAPIBuild(job db.Job, createdBy string) db.Build {
	GinkgoHelper()

	build, err := job.CreateBuild(createdBy)
	Expect(err).NotTo(HaveOccurred())
	reloadJobsAPIBuild(build)
	return build
}

func startJobsAPIBuild(build db.Build) {
	GinkgoHelper()

	started, err := build.Start(atc.Plan{})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	reloadJobsAPIBuild(build)
}

func finishJobsAPIBuild(build db.Build, status db.BuildStatus) {
	GinkgoHelper()

	Expect(build.Finish(status)).To(Succeed())
	reloadJobsAPIBuild(build)
}

func reloadJobsAPIBuild(build db.Build) {
	GinkgoHelper()

	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
}

func jobsAPIGet(server *httptest.Server, path string) *http.Response {
	GinkgoHelper()

	response, err := client.Get(server.URL + path)
	if response != nil {
		DeferCleanup(func() {
			Expect(response.Body.Close()).To(Succeed())
		})
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func jobsAPIPost(server *httptest.Server, path string) *http.Response {
	GinkgoHelper()

	request, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() {
			Expect(response.Body.Close()).To(Succeed())
		})
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func jobsAPIRequest(server *httptest.Server, method string, path string) *http.Response {
	GinkgoHelper()

	request, err := http.NewRequest(method, server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	if response != nil {
		DeferCleanup(func() {
			Expect(response.Body.Close()).To(Succeed())
		})
	}
	Expect(err).NotTo(HaveOccurred())
	return response
}

func decodeJobsAPIResponse[T any](response *http.Response) T {
	GinkgoHelper()

	var value T
	Expect(json.NewDecoder(response.Body).Decode(&value)).To(Succeed())
	return value
}

func expectJobsAPIBuild(actual atc.Build, expected db.Build) {
	GinkgoHelper()

	Expect(actual.ID).To(Equal(expected.ID()))
	Expect(actual.Name).To(Equal(expected.Name()))
	Expect(actual.JobName).To(Equal(expected.JobName()))
	Expect(actual.PipelineID).To(Equal(expected.PipelineID()))
	Expect(actual.PipelineName).To(Equal(expected.PipelineName()))
	Expect(actual.PipelineInstanceVars).To(Equal(expected.PipelineInstanceVars()))
	Expect(actual.TeamName).To(Equal(expected.TeamName()))
	Expect(actual.Status).To(Equal(atc.BuildStatus(expected.Status())))
	Expect(actual.APIURL).To(Equal(fmt.Sprintf("/api/v1/builds/%d", expected.ID())))
	Expect(actual.CreatedBy).To(Equal(expected.CreatedBy()))
	if expected.StartTime().IsZero() {
		Expect(actual.StartTime).To(BeZero())
	} else {
		Expect(actual.StartTime).To(Equal(expected.StartTime().Unix()))
	}
	if expected.EndTime().IsZero() {
		Expect(actual.EndTime).To(BeZero())
	} else {
		Expect(actual.EndTime).To(Equal(expected.EndTime().Unix()))
	}
	if expected.RerunOf() == 0 {
		Expect(actual.RerunOf).To(BeNil())
		Expect(actual.RerunNumber).To(BeZero())
	} else {
		Expect(actual.RerunOf).To(Equal(&atc.RerunOfBuild{
			ID: expected.RerunOf(), Name: expected.RerunOfName(),
		}))
		Expect(actual.RerunNumber).To(Equal(expected.RerunNumber()))
	}
}

func expectJobsAPIBuildSummary(actual *atc.BuildSummary, expected db.Build) {
	GinkgoHelper()

	if expected == nil {
		Expect(actual).To(BeNil())
		return
	}
	Expect(actual).NotTo(BeNil())
	Expect(actual.ID).To(Equal(expected.ID()))
	Expect(actual.Name).To(Equal(expected.Name()))
	Expect(actual.JobName).To(Equal(expected.JobName()))
	Expect(actual.PipelineID).To(Equal(expected.PipelineID()))
	Expect(actual.PipelineName).To(Equal(expected.PipelineName()))
	Expect(actual.PipelineInstanceVars).To(Equal(expected.PipelineInstanceVars()))
	Expect(actual.TeamName).To(Equal(expected.TeamName()))
	Expect(actual.Status).To(Equal(atc.BuildStatus(expected.Status())))
	Expect(actual.StartTime).To(Equal(expected.StartTime().Unix()))
	Expect(actual.EndTime).To(Equal(expected.EndTime().Unix()))
}

var _ = Describe("Jobs API", func() {
	Describe("GET /api/v1/jobs", func() {
		var server *httptest.Server

		type listingState struct {
			fixture   *jobsAPIFixture
			pipelines map[string]db.Pipeline
			jobs      map[int]db.Job
		}

		setupListing := func() listingState {
			GinkgoHelper()
			config := atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-input", Type: dbtest.BaseResourceType},
					{Name: "some-other-input", Type: dbtest.BaseResourceType},
				},
				Jobs: atc.JobConfigs{
					{Name: "job-a"}, {Name: "job-b"},
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-input"}},
							{Config: &atc.GetStep{Name: "some-name", Resource: "some-other-input", Passed: []string{"job-a", "job-b"}, Trigger: true}},
						},
					},
				},
				Groups: atc.GroupConfigs{
					{Name: "group-1", Jobs: []string{"some-job"}},
					{Name: "group-2", Jobs: []string{"some-job"}},
				},
			}
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "private-authorized"}, config)
			pipelines := map[string]db.Pipeline{"private-authorized": fixture.Pipeline}
			for _, saved := range []struct {
				teamName     string
				pipelineName string
				public       bool
			}{
				{teamName: "public-team", pipelineName: "public-pipeline", public: true},
				{teamName: "private-team", pipelineName: "private-unauthorized"},
			} {
				team, err := fixture.Real.Deps.teamFactory.CreateTeam(atc.Team{Name: saved.teamName})
				Expect(err).NotTo(HaveOccurred())
				pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: saved.pipelineName}, config, db.ConfigVersion(0), false)
				Expect(err).NotTo(HaveOccurred())
				if saved.public {
					Expect(pipeline.Expose()).To(Succeed())
				}
				pipelines[saved.pipelineName] = pipeline
			}

			jobs := map[int]db.Job{}
			for _, pipeline := range pipelines {
				persistedJobs, err := pipeline.Jobs()
				Expect(err).NotTo(HaveOccurred())
				for _, job := range persistedJobs {
					jobs[job.ID()] = job
				}
				mainJob, found, err := pipeline.Job("some-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(mainJob.Pause("listing-test")).To(Succeed())
				finished := createJobsAPIBuild(mainJob, "listing-test")
				startJobsAPIBuild(finished)
				finishJobsAPIBuild(finished, db.BuildStatusSucceeded)
				next := createJobsAPIBuild(mainJob, "listing-test")
				startJobsAPIBuild(next)
				found, err = mainJob.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
			}
			return listingState{fixture: fixture, pipelines: pipelines, jobs: jobs}
		}

		expectListing := func(response *http.Response, state listingState, visiblePipelineNames ...string) {
			GinkgoHelper()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))
			summaries := decodeJobsAPIResponse[[]atc.JobSummary](response)
			expectedIDs := []int{}
			for _, pipelineName := range visiblePipelineNames {
				persistedJobs, err := state.pipelines[pipelineName].Jobs()
				Expect(err).NotTo(HaveOccurred())
				for _, job := range persistedJobs {
					expectedIDs = append(expectedIDs, job.ID())
				}
			}
			actualIDs := make([]int, 0, len(summaries))
			for _, summary := range summaries {
				actualIDs = append(actualIDs, summary.ID)
				job := state.jobs[summary.ID]
				Expect(job).NotTo(BeNil())
				found, err := job.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(summary.Name).To(Equal(job.Name()))
				Expect(summary.PipelineID).To(Equal(job.PipelineID()))
				Expect(summary.PipelineName).To(Equal(job.PipelineName()))
				Expect(summary.TeamName).To(Equal(job.TeamName()))
				Expect(summary.Paused).To(Equal(job.Paused()))
				finished, next, err := job.FinishedAndNextBuild()
				Expect(err).NotTo(HaveOccurred())
				expectJobsAPIBuildSummary(summary.FinishedBuild, finished)
				expectJobsAPIBuildSummary(summary.NextBuild, next)
				if job.Name() == "some-job" {
					Expect(summary.Inputs).To(Equal([]atc.JobInputSummary{
						{Name: "some-input", Resource: "some-input"},
						{Name: "some-name", Resource: "some-other-input", Passed: []string{"job-a", "job-b"}, Trigger: true},
					}))
					Expect(summary.Groups).To(Equal([]string{"group-1", "group-2"}))
				} else {
					Expect(summary.Inputs).To(BeEmpty())
					Expect(summary.Groups).To(BeEmpty())
				}
			}
			Expect(actualIDs).To(ConsistOf(expectedIDs))
		}

		It("returns only jobs in persisted public pipelines without authentication", func() {
			state := setupListing()
			server = state.fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/jobs")
			expectListing(response, state, "public-pipeline")
		})

		It("adds persisted private jobs for authenticated team membership", func() {
			state := setupListing()
			fakeAccess.TeamNamesReturns([]string{"some-team"})
			server = state.fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/jobs")
			expectListing(response, state, "public-pipeline", "private-authorized")
		})

		It("returns every persisted active job for an administrator", func() {
			state := setupListing()
			fakeAccess.IsAdminReturns(true)
			server = state.fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/jobs")
			expectListing(response, state, "public-pipeline", "private-authorized", "private-unauthorized")
		})

		It("returns an empty array when no configured jobs are visible", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "empty-pipeline"}, atc.Config{})
			server = fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeJobsAPIResponse[[]atc.JobSummary](response)).To(BeEmpty())
		})

		It("returns 500 when the real job factory query uses a closed connection", func() {
			fixture := useJobsAPIFixture(
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			)
			doomedConn := postgresRunner.OpenConn()
			doomedJobFactory := db.NewJobFactory(doomedConn, fixture.Real.LockFactory)
			Expect(doomedConn.Close()).To(Succeed())
			deps := fixture.Real.Deps
			deps.jobFactory = doomedJobFactory
			server = newAPIServer(deps)
			DeferCleanup(server.Close)
			response := jobsAPIGet(server, "/api/v1/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name", func() {
		var (
			response       *http.Response
			server         *httptest.Server
			fixture        *jobsAPIFixture
			config         atc.Config
			pipelineRef    atc.PipelineRef
			job            db.Job
			finishedBuild  db.Build
			nextBuild      db.Build
			exposePipeline bool
			setup          func(*jobsAPIFixture)
			routePipeline  func(*jobsAPIFixture) db.Pipeline
		)

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipelineRef = atc.PipelineRef{Name: "some-pipeline"}
			exposePipeline = false
			routePipeline = nil
			config = atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-input", Type: dbtest.BaseResourceType},
					{Name: "some-other-input", Type: dbtest.BaseResourceType},
					{Name: "some-output", Type: dbtest.BaseResourceType},
					{Name: "some-other-output", Type: dbtest.BaseResourceType},
				},
				Jobs: atc.JobConfigs{
					{Name: "upstream-a"},
					{Name: "upstream-b"},
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-input"}},
							{Config: &atc.GetStep{
								Name: "some-name", Resource: "some-other-input",
								Passed: []string{"upstream-a", "upstream-b"}, Trigger: true,
							}},
							{Config: &atc.PutStep{Name: "some-output"}},
							{Config: &atc.PutStep{Name: "some-other-output"}},
						},
					},
				},
				Groups: atc.GroupConfigs{
					{Name: "group-1", Jobs: []string{"some-job"}},
					{Name: "group-2", Jobs: []string{"some-job"}},
				},
			}
			setup = func(fixture *jobsAPIFixture) {
				job = fixture.Job("some-job")
				Expect(job.Pause("api-test")).To(Succeed())
				finishedBuild = createJobsAPIBuild(job, "api-test")
				startJobsAPIBuild(finishedBuild)
				finishJobsAPIBuild(finishedBuild, db.BuildStatusSucceeded)
				nextBuild = createJobsAPIBuild(job, "api-test")
				startJobsAPIBuild(nextBuild)
				Expect(job.UpdateFirstLoggedBuildID(finishedBuild.ID())).To(Succeed())
				found, err := job.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
			}
		})

		JustBeforeEach(func() {
			fixture = useJobsAPIFixture(pipelineRef, config)
			setup(fixture)
			if exposePipeline {
				Expect(fixture.Pipeline.Expose()).To(Succeed())
			}
			if routePipeline == nil {
				server = fixture.Serve()
			} else {
				server = fixture.ServePipeline(routePipeline(fixture))
			}
			response = jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job")
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
				fakeAccess.IsAuthorizedReturns(false)
			})

			It("returns 401 for a private pipeline", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					exposePipeline = true
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authenticated and not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)
			})

			It("returns 403 for a private pipeline", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					exposePipeline = true
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			Context("when Pipeline.Job fails", func() {
				BeforeEach(func() {
					setup = func(*jobsAPIFixture) {}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return fixture.doomedPipeline()
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is absent", func() {
				BeforeEach(func() {
					config.Jobs = atc.JobConfigs{{Name: "other-job"}}
					setup = func(*jobsAPIFixture) {}
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when Inputs fails", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job = fixture.Job("some-job")
					}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return jobsAPIPipeline{
							Pipeline: fixture.Pipeline,
							jobName:  "some-job",
							job:      fixture.doomedJob("some-job"),
						}
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when Outputs fails after real inputs succeed", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job = fixture.Job("some-job")
					}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return jobsAPIPipeline{
							Pipeline: fixture.Pipeline,
							jobName:  "some-job",
							job: outputsErrorJob{
								Job: fixture.Job("some-job"), err: errors.New("outputs failed"),
							},
						}
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when FinishedAndNextBuild fails after real inputs and outputs succeed", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job = fixture.Job("some-job")
					}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return jobsAPIPipeline{
							Pipeline: fixture.Pipeline,
							jobName:  "some-job",
							job: finishedAndNextErrorJob{
								Job: fixture.Job("some-job"), err: errors.New("build lookup failed"),
							},
						}
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			It("returns the persisted finished build identity and status", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
				actual := decodeJobsAPIResponse[atc.Job](response)

				Expect(actual.ID).To(Equal(job.ID()))
				Expect(actual.Name).To(Equal(job.Name()))
				Expect(actual.TeamName).To(Equal(job.TeamName()))
				Expect(actual.PipelineID).To(Equal(job.PipelineID()))
				Expect(actual.PipelineName).To(Equal(job.PipelineName()))
				Expect(actual.PipelineInstanceVars).To(Equal(job.PipelineInstanceVars()))
				Expect(actual.Paused).To(Equal(job.Paused()))
				Expect(actual.PausedBy).To(Equal(job.PausedBy()))
				Expect(actual.PausedAt).To(Equal(job.PausedAt().Unix()))
				Expect(actual.FirstLoggedBuildID).To(Equal(job.FirstLoggedBuildID()))
				Expect(actual.Groups).To(ConsistOf(job.Tags()))
				Expect(actual.Inputs).To(ConsistOf(
					atc.JobInput{Name: "some-input", Resource: "some-input"},
					atc.JobInput{
						Name: "some-name", Resource: "some-other-input",
						Passed: []string{"upstream-a", "upstream-b"}, Trigger: true,
					},
				))
				Expect(actual.Outputs).To(ConsistOf(
					atc.JobOutput{Name: "some-output", Resource: "some-output"},
					atc.JobOutput{Name: "some-other-output", Resource: "some-other-output"},
				))
				Expect(actual.FinishedBuild).NotTo(BeNil())
				expectJobsAPIBuild(*actual.FinishedBuild, finishedBuild)
				Expect(actual.NextBuild).NotTo(BeNil())
				expectJobsAPIBuild(*actual.NextBuild, nextBuild)
				Expect(actual.FinishedBuild.EndTime).NotTo(BeZero())
				Expect(actual.NextBuild.EndTime).To(BeZero())
			})

			Context("when the job naturally has no builds", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job = fixture.Job("some-job")
					}
				})

				It("returns null build fields", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					actual := decodeJobsAPIResponse[atc.Job](response)
					Expect(actual.FinishedBuild).To(BeNil())
					Expect(actual.NextBuild).To(BeNil())
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/badge", func() {
		var server *httptest.Server
		var response *http.Response
		var fixture *jobsAPIFixture
		var config atc.Config
		var expose bool
		var hasFinishedBuild bool
		var finishedStatus db.BuildStatus
		var failure string

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			config = atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
			expose = false
			hasFinishedBuild = false
			finishedStatus = db.BuildStatusSucceeded
			failure = ""
		})

		JustBeforeEach(func() {
			fixture = useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			if expose {
				Expect(fixture.Pipeline.Expose()).To(Succeed())
			} else {
				Expect(fixture.Pipeline.Hide()).To(Succeed())
			}

			pipeline := fixture.Pipeline
			switch failure {
			case "pipeline-job":
				pipeline = fixture.doomedPipeline()
			case "finished-and-next":
				job := fixture.Job("some-job")
				pipeline = jobsAPIPipeline{
					Pipeline: fixture.Pipeline,
					jobName:  "some-job",
					job: finishedAndNextErrorJob{
						Job: job,
						err: errors.New("oh no!"),
					},
				}
			default:
				if hasFinishedBuild {
					build := createJobsAPIBuild(fixture.Job("some-job"), "api-badge-test")
					startJobsAPIBuild(build)
					finishJobsAPIBuild(build, finishedStatus)
				}
			}

			if failure == "" {
				server = fixture.Serve()
			} else {
				server = fixture.ServePipeline(pipeline)
			}
			response = jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge")
		})

		readBadge := func(response *http.Response) string {
			GinkgoHelper()
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			return string(body)
		}

		Context("when authenticated and not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)
			})

			Context("and the pipeline is private", func() {
				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					expose = true
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			It("returns 200 OK with SVG headers and an unknown badge for the real buildless job", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(response).Should(IncludeHeaderEntries(map[string]string{
					"Content-Type":  "image/svg+xml",
					"Cache-Control": "no-cache, no-store, must-revalidate",
					"Expires":       "0",
				}))
				body := readBadge(response)
				Expect(body).To(ContainSubstring("unknown"))
				Expect(body).To(ContainSubstring("#9f9f9f"))
			})

			assertPersistedStatus := func(description string, status db.BuildStatus, label, color string) {
				Context(description, func() {
					BeforeEach(func() {
						hasFinishedBuild = true
						finishedStatus = status
					})

					It("renders the persisted finished build status and color", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						body := readBadge(response)
						Expect(body).To(ContainSubstring(label))
						Expect(body).To(ContainSubstring(color))
					})
				})
			}
			assertPersistedStatus("when the finished build succeeded", db.BuildStatusSucceeded, "passing", "#44cc11")
			assertPersistedStatus("when the finished build failed", db.BuildStatusFailed, "failing", "#e05d44")
			assertPersistedStatus("when the finished build was aborted", db.BuildStatusAborted, "aborted", "#8f4b2d")
			assertPersistedStatus("when the finished build errored", db.BuildStatusErrored, "errored", "#fe7d37")

			Context("with a successful persisted build", func() {
				BeforeEach(func() {
					hasFinishedBuild = true
					finishedStatus = db.BuildStatusSucceeded
				})

				It("uses the URL title parameter and HTML escapes it", func() {
					alternate := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge?title=%24custom")
					Expect(readBadge(alternate)).To(ContainSubstring("$custom"))
				})

				It("uses the default title and dimensions when the title is omitted", func() {
					body := readBadge(response)
					Expect(body).To(ContainSubstring("build"))
					Expect(body).To(ContainSubstring(`width="88"`))
					Expect(body).To(ContainSubstring(`d="M0 0h37v20H0z"`))
				})

				It("uses the default build title when the title parameter is empty", func() {
					alternate := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge?title=")
					Expect(readBadge(alternate)).To(ContainSubstring("build"))
				})

				DescribeTable("scales custom titles",
					func(title, width string) {
						alternate := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge?title="+title)
						body := readBadge(alternate)
						Expect(body).To(ContainSubstring(title))
						Expect(body).To(ContainSubstring(`width="` + width + `"`))
					},
					Entry("short", "test", "87"),
					Entry("medium", "integration", "123"),
					Entry("long", "very-long-deployment-name", "201"),
				)

				It("scales production-deployment and preserves the passing status", func() {
					alternate := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge?title=production-deployment")
					body := readBadge(alternate)
					Expect(body).To(ContainSubstring("production-deployment"))
					Expect(body).To(ContainSubstring("passing"))
					Expect(body).To(MatchRegexp(`width="[0-9]{3}"`))
				})

				It("preserves the original status width for custom titles", func() {
					alternate := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/badge?title=custom")
					Expect(readBadge(alternate)).To(MatchRegexp(`d="M[0-9]+ 0h51v20H[0-9]+z"`))
				})
			})

			Context("when getting the real job's builds fails", func() {
				BeforeEach(func() {
					failure = "finished-and-next"
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is naturally absent", func() {
				BeforeEach(func() {
					config = atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}}
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when finding the job through the real pipeline fails", func() {
				BeforeEach(func() {
					failure = "pipeline-job"
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs", func() {
		var server *httptest.Server

		dashboardConfig := func() atc.Config {
			return atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "input-1", Type: dbtest.BaseResourceType},
					{Name: "input-2", Type: dbtest.BaseResourceType},
					{Name: "input-3", Type: dbtest.BaseResourceType},
				},
				Jobs: atc.JobConfigs{
					{Name: "job-1", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input-1"}}}},
					{Name: "job-2", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input-2"}}}},
					{Name: "job-3", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "input-3"}}}},
				},
				Groups: atc.GroupConfigs{
					{Name: "group-1", Jobs: []string{"job-1"}},
					{Name: "group-2", Jobs: []string{"job-1", "job-2"}},
				},
			}
		}

		It("returns persisted instanced jobs, groups, inputs, pause state, and ordinary build relationships", func() {
			pipelineRef := atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
			fixture := useJobsAPIFixture(
				pipelineRef,
				dashboardConfig(),
			)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			jobs := map[string]db.Job{}
			for _, name := range []string{"job-1", "job-2", "job-3"} {
				jobs[name] = fixture.Job(name)
				Expect(jobs[name].Pause("dashboard-test")).To(Succeed())
			}
			job1Finished := createJobsAPIBuild(jobs["job-1"], "dashboard-test")
			startJobsAPIBuild(job1Finished)
			finishJobsAPIBuild(job1Finished, db.BuildStatusSucceeded)
			job1Next := createJobsAPIBuild(jobs["job-1"], "dashboard-test")
			startJobsAPIBuild(job1Next)
			job2Finished := createJobsAPIBuild(jobs["job-2"], "dashboard-test")
			startJobsAPIBuild(job2Finished)
			finishJobsAPIBuild(job2Finished, db.BuildStatusFailed)

			server = fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs?"+pipelineRef.QueryParams().Encode())
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))
			summaries := decodeJobsAPIResponse[[]atc.JobSummary](response)
			Expect(summaries).To(HaveLen(3))
			byName := map[string]atc.JobSummary{}
			for _, summary := range summaries {
				byName[summary.Name] = summary
				job := jobs[summary.Name]
				found, err := job.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(summary.ID).To(Equal(job.ID()))
				Expect(summary.PipelineID).To(Equal(fixture.Pipeline.ID()))
				Expect(summary.PipelineName).To(Equal(fixture.Pipeline.Name()))
				Expect(summary.PipelineInstanceVars).To(Equal(fixture.Pipeline.InstanceVars()))
				Expect(summary.TeamName).To(Equal(fixture.Team.Name()))
				Expect(summary.Paused).To(Equal(job.Paused()))
			}
			Expect(byName["job-1"].Inputs).To(Equal([]atc.JobInputSummary{{Name: "input-1", Resource: "input-1"}}))
			Expect(byName["job-1"].Groups).To(Equal([]string{"group-1", "group-2"}))
			expectJobsAPIBuildSummary(byName["job-1"].NextBuild, job1Next)
			expectJobsAPIBuildSummary(byName["job-1"].FinishedBuild, job1Finished)
			expectJobsAPIBuildSummary(byName["job-1"].TransitionBuild, job1Finished)
			Expect(byName["job-2"].Inputs).To(Equal([]atc.JobInputSummary{{Name: "input-2", Resource: "input-2"}}))
			Expect(byName["job-2"].Groups).To(Equal([]string{"group-2"}))
			expectJobsAPIBuildSummary(byName["job-2"].NextBuild, nil)
			expectJobsAPIBuildSummary(byName["job-2"].FinishedBuild, job2Finished)
			expectJobsAPIBuildSummary(byName["job-2"].TransitionBuild, job2Finished)
			Expect(byName["job-3"].Inputs).To(Equal([]atc.JobInputSummary{{Name: "input-3", Resource: "input-3"}}))
			Expect(byName["job-3"].Groups).To(BeEmpty())
			expectJobsAPIBuildSummary(byName["job-3"].NextBuild, nil)
			expectJobsAPIBuildSummary(byName["job-3"].FinishedBuild, nil)
			expectJobsAPIBuildSummary(byName["job-3"].TransitionBuild, nil)
		})

		It("returns an empty dashboard for a persisted pipeline with no jobs", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeJobsAPIResponse[[]atc.JobSummary](response)).To(BeEmpty())
		})

		It("returns 500 from the narrow real-pipeline dashboard boundary", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, dashboardConfig())
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.ServePipeline(dashboardErrorPipeline{Pipeline: fixture.Pipeline, err: errors.New("dashboard failed")})
			response := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 401 for an unauthenticated private persisted pipeline", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, dashboardConfig())
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.IsAuthorizedReturns(false)
			Expect(fixture.Pipeline.Hide()).To(Succeed())
			server = fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns a public persisted dashboard without authentication", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, dashboardConfig())
			fakeAccess.IsAuthenticatedReturns(false)
			fakeAccess.IsAuthorizedReturns(false)
			Expect(fixture.Pipeline.Expose()).To(Succeed())
			server = fixture.Serve()
			response := jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs")
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds", func() {
		var (
			server        *httptest.Server
			response      *http.Response
			fixture       *jobsAPIFixture
			pipelineRef   atc.PipelineRef
			config        atc.Config
			queryParams   string
			expose        bool
			setup         func(*jobsAPIFixture)
			routePipeline func(*jobsAPIFixture) db.Pipeline
		)

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipelineRef = atc.PipelineRef{Name: "some-pipeline"}
			config = atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
			queryParams = ""
			expose = false
			setup = func(*jobsAPIFixture) {}
			routePipeline = nil
		})

		JustBeforeEach(func() {
			fixture = useJobsAPIFixture(pipelineRef, config)
			setup(fixture)
			if expose {
				Expect(fixture.Pipeline.Expose()).To(Succeed())
			}
			if routePipeline == nil {
				server = fixture.Serve()
			} else {
				server = fixture.ServePipeline(routePipeline(fixture))
			}
			response = jobsAPIGet(server, "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds"+queryParams)
		})

		decodeBuilds := func(response *http.Response) []atc.Build {
			GinkgoHelper()
			return decodeJobsAPIResponse[[]atc.Build](response)
		}

		Context("when authenticated and not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)
			})

			It("returns 403 for a private pipeline", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
			})

			Context("and the pipeline is public", func() {
				var publicBuild db.Build

				BeforeEach(func() {
					expose = true
					setup = func(fixture *jobsAPIFixture) {
						publicBuild = createJobsAPIBuild(fixture.Job("some-job"), "api-public-test")
					}
				})

				It("returns the persisted build", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					actual := decodeBuilds(response)
					Expect(actual).To(HaveLen(1))
					expectJobsAPIBuild(actual[0], publicBuild)
				})
			})
		})

		Context("when authorized", func() {
			Context("when no params are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job := fixture.Job("some-job")
						persistedBuilds = make([]db.Build, 0, 101)
						for range 101 {
							persistedBuilds = append(persistedBuilds, createJobsAPIBuild(job, "api-default-limit"))
						}
					}
				})

				It("returns only the 100 newest persisted builds", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					actual := decodeBuilds(response)
					Expect(actual).To(HaveLen(atc.PaginationAPIDefaultLimit))
					expectedIDs := make([]int, 0, atc.PaginationAPIDefaultLimit)
					for i := len(persistedBuilds) - 1; i >= 1; i-- {
						expectedIDs = append(expectedIDs, persistedBuilds[i].ID())
					}
					actualIDs := make([]int, len(actual))
					for i, build := range actual {
						actualIDs[i] = build.ID
					}
					Expect(actualIDs).To(Equal(expectedIDs))
					Expect(actualIDs).NotTo(ContainElement(persistedBuilds[0].ID()))
				})
			})

			Context("when from, to, and limit are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job := fixture.Job("some-job")
						persistedBuilds = make([]db.Build, 0, 7)
						for range 7 {
							persistedBuilds = append(persistedBuilds, createJobsAPIBuild(job, "api-range"))
						}
						queryParams = fmt.Sprintf(
							"?from=%d&to=%d&limit=3",
							persistedBuilds[1].ID(),
							persistedBuilds[5].ID(),
						)
					}
				})

				It("applies all supported cursor parameters to persisted rows", func() {
					actual := decodeBuilds(response)
					Expect(actual).To(HaveLen(3))
					Expect([]int{actual[0].ID, actual[1].ID, actual[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))

					bounded := jobsAPIGet(server, fmt.Sprintf(
						"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds?from=%d&to=%d&limit=6",
						persistedBuilds[1].ID(),
						persistedBuilds[3].ID(),
					))
					Expect(bounded.StatusCode).To(Equal(http.StatusOK))
					boundedBuilds := decodeBuilds(bounded)
					Expect(boundedBuilds).To(HaveLen(3))
					Expect([]int{boundedBuilds[0].ID, boundedBuilds[1].ID, boundedBuilds[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))
				})
			})

			Context("when persisted builds have ordinary and rerun state", func() {
				var original db.Build
				var started db.Build
				var rerun db.Build

				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job := fixture.Job("some-job")
						original = createJobsAPIBuild(job, "api-original")
						startJobsAPIBuild(original)
						finishJobsAPIBuild(original, db.BuildStatusSucceeded)
						started = createJobsAPIBuild(job, "api-started")
						startJobsAPIBuild(started)
						var err error
						rerun, err = job.RerunBuild(original, "api-rerun")
						Expect(err).NotTo(HaveOccurred())
						reloadJobsAPIBuild(rerun)
						startJobsAPIBuild(rerun)
						finishJobsAPIBuild(rerun, db.BuildStatusSucceeded)
						queryParams = "?limit=3"
					}
				})

				It("returns each dynamic build field in production order", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
					actual := decodeBuilds(response)
					Expect(actual).To(HaveLen(3))
					expectJobsAPIBuild(actual[0], started)
					expectJobsAPIBuild(actual[1], rerun)
					expectJobsAPIBuild(actual[2], original)
					Expect(actual[0].EndTime).To(BeZero())
					Expect(actual[1].RerunOf).To(Equal(&atc.RerunOfBuild{
						ID: original.ID(), Name: original.Name(),
					}))
					Expect(actual[1].RerunNumber).To(Equal(rerun.RerunNumber()))
				})
			})

			Context("when older and newer pages are available", func() {
				var olderBuild db.Build
				var middleBuild db.Build
				var newerBuild db.Build

				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						job := fixture.Job("some-job")
						olderBuild = createJobsAPIBuild(job, "api-pagination")
						middleBuild = createJobsAPIBuild(job, "api-pagination")
						newerBuild = createJobsAPIBuild(job, "api-pagination")
						queryParams = fmt.Sprintf(
							"?from=%d&to=%d&limit=1",
							middleBuild.ID(),
							middleBuild.ID(),
						)
						if varsQuery := pipelineRef.QueryParams().Encode(); varsQuery != "" {
							queryParams += "&" + varsQuery
						}
					}
				})

				It("returns RFC5988 links derived from the persisted cursors", func() {
					actual := decodeBuilds(response)
					Expect(actual).To(HaveLen(1))
					Expect(actual[0].ID).To(Equal(middleBuild.ID()))
					Expect(response.Header["Link"]).To(ConsistOf([]string{
						fmt.Sprintf(`<%s/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds?to=%d&limit=1>; rel="next"`,
							externalURL, olderBuild.ID()),
						fmt.Sprintf(`<%s/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds?from=%d&limit=1>; rel="previous"`,
							externalURL, newerBuild.ID()),
					}))
				})

				Context("and the pipeline has real instance vars", func() {
					BeforeEach(func() {
						pipelineRef = atc.PipelineRef{
							Name: "some-pipeline",
							InstanceVars: atc.InstanceVars{
								"branch": "master",
								"nested": map[string]any{"enabled": true},
							},
						}
					})

					It("preserves PipelineRef query encoding in both links", func() {
						actual := decodeBuilds(response)
						Expect(actual).To(HaveLen(1))
						Expect(actual[0].PipelineInstanceVars).To(Equal(pipelineRef.InstanceVars))
						varsQuery := pipelineRef.QueryParams().Encode()
						Expect(response.Header["Link"]).To(ConsistOf([]string{
							fmt.Sprintf(`<%s/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds?to=%d&limit=1&%s>; rel="next"`,
								externalURL, olderBuild.ID(), varsQuery),
							fmt.Sprintf(`<%s/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds?from=%d&limit=1&%s>; rel="previous"`,
								externalURL, newerBuild.ID(), varsQuery),
						}))
					})
				})
			})

			Context("when querying the real job's builds fails", func() {
				BeforeEach(func() {
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return jobsAPIPipeline{
							Pipeline: fixture.Pipeline,
							jobName:  "some-job",
							job:      fixture.doomedJob("some-job"),
						}
					}
				})

				It("preserves the handler's 404 response", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when finding the job through the real pipeline fails", func() {
				BeforeEach(func() {
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return fixture.doomedPipeline()
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is naturally absent", func() {
				BeforeEach(func() {
					config = atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}}
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})
		})
	})
	Describe("POST /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds", func() {
		const path = "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds"
		var server *httptest.Server

		manualConfig := func(disableManualTrigger bool) atc.Config {
			return atc.Config{
				ResourceTypes: atc.ResourceTypes{{
					Name: "some-type", Type: dbtest.BaseResourceType,
					Source: atc.Source{"repository": "resource-type"},
				}},
				Resources: atc.ResourceConfigs{{
					Name: "some-input", Type: "some-type",
					Source: atc.Source{"repository": "resource"},
				}},
				Jobs: atc.JobConfigs{{
					Name: "some-job", DisableManualTrigger: disableManualTrigger,
					PlanSequence: []atc.Step{{Config: &atc.GetStep{
						Name: "some-input", Resource: "some-input",
					}}},
				}},
			}
		}

		BeforeEach(func() {
			fakeAccess.IsAuthorizedReturns(true)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
		})

		It("persists exactly one pending build and schedules a real check from the persisted pin", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, manualConfig(false))
			pinnedVersion := atc.Version{"ref": "pinned"}
			fixture.Scenario.Run(fixture.Builder.WithResourceVersions("some-input", pinnedVersion))

			resource := fixture.Scenario.Resource("some-input")
			pinned, err := resource.PinVersion(fixture.Scenario.ResourceVersion("some-input", pinnedVersion).ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(pinned).To(BeTrue())
			found, err := resource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.CurrentPinnedVersion()).To(Equal(pinnedVersion))

			var checksBefore int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE resource_id = $1`, resource.ID(),
			).Scan(&checksBefore)).To(Succeed())

			recorder := &recordingJobsCheckFactory{CheckFactory: fixture.Real.Deps.checkFactory}
			fixture.Real.Deps.checkFactory = recorder
			server = fixture.Serve()
			response := jobsAPIPost(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))

			actual := decodeJobsAPIResponse[atc.Build](response)
			job := fixture.Job("some-job")
			persisted, found, err := job.Build(actual.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			expectJobsAPIBuild(actual, persisted)
			Expect(actual.ID).To(BeNumerically(">", 0))
			Expect(actual.Name).NotTo(BeEmpty())
			Expect(actual.PipelineName).To(Equal("some-pipeline"))
			Expect(actual.TeamName).To(Equal("some-team"))
			Expect(actual.CreatedBy).NotTo(BeNil())
			Expect(*actual.CreatedBy).To(Equal("api-user"))
			Expect(actual.Status).To(Equal(atc.StatusPending))
			Expect(actual.StartTime).To(BeZero())
			Expect(actual.EndTime).To(BeZero())

			var jobBuilds int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE job_id = $1`, job.ID(),
			).Scan(&jobBuilds)).To(Succeed())
			Expect(jobBuilds).To(Equal(1))

			calls := recorder.Calls()
			Expect(calls).To(HaveLen(1))
			call := calls[0]
			Expect(call.checkable.(db.Resource).ID()).To(Equal(resource.ID()))
			Expect(call.resourceTypes).To(HaveLen(1))
			Expect(call.resourceTypes[0].Name()).To(Equal("some-type"))
			Expect(call.from).To(Equal(pinnedVersion))
			Expect(call.manuallyTriggered).To(BeTrue())
			Expect(call.skipIntervalRecursively).To(BeTrue())
			Expect(call.toDB).To(BeTrue())
			Expect(call.build).NotTo(BeNil())
			Expect(call.created).To(BeTrue())
			Expect(call.err).NotTo(HaveOccurred())

			var checksAfter int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE resource_id = $1`, resource.ID(),
			).Scan(&checksAfter)).To(Succeed())
			Expect(checksAfter).To(Equal(checksBefore + 1))
			Expect(call.build.ID()).To(BeNumerically(">", 0))
		})

		It("rejects a job with manual triggering disabled without inserting a build", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, manualConfig(true))
			server = fixture.Serve()
			response := jobsAPIPost(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))

			var count int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE job_id = $1`, fixture.Job("some-job").ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})

		It("returns 500 when the real job create fails on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, manualConfig(false))
			doomed := fixture.doomedJob("some-job")
			pipeline := jobsAPIPipeline{
				Pipeline: fixture.Pipeline, jobName: "some-job", job: doomed,
			}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIPost(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			var count int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE job_id = $1`, fixture.Job("some-job").ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, manualConfig(false))
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIPost(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 404 for a missing persisted job", func() {
			fixture := useJobsAPIFixture(
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}},
			)
			server = fixture.Serve()
			response := jobsAPIPost(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		DescribeTable("keeps the inserted manual build when a downstream read fails",
			func(boundary string) {
				fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, manualConfig(false))
				var pipeline db.Pipeline
				switch boundary {
				case "resources":
					pipeline = resourcesErrorPipeline{Pipeline: fixture.Pipeline, err: errors.New("resources failed")}
				case "resource types":
					pipeline = resourceTypesErrorPipeline{Pipeline: fixture.Pipeline, err: errors.New("resource types failed")}
				case "inputs":
					job := inputsErrorJob{Job: fixture.Job("some-job"), err: errors.New("inputs failed")}
					pipeline = jobsAPIPipeline{Pipeline: fixture.Pipeline, jobName: "some-job", job: job}
				default:
					Fail("unknown downstream boundary: " + boundary)
				}

				server = fixture.ServePipeline(pipeline)
				response := jobsAPIPost(server, path)
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				var count int
				Expect(fixture.Real.Conn.QueryRow(
					`SELECT count(*) FROM builds WHERE job_id = $1`, fixture.Job("some-job").ID(),
				).Scan(&count)).To(Succeed())
				Expect(count).To(Equal(1))
			},
			Entry("Pipeline.Resources", "resources"),
			Entry("Pipeline.ResourceTypes", "resource types"),
			Entry("Job.Inputs", "inputs"),
		)
	})
	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/inputs", func() {
		const path = "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/inputs"
		var server *httptest.Server

		inputConfig := func() atc.Config {
			return atc.Config{
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource", Type: dbtest.BaseResourceType,
						Source: atc.Source{"repository": "primary"},
					},
					{
						Name: "some-other-resource", Type: dbtest.UniqueBaseResourceType,
						Source: atc.Source{"repository": "secondary"},
					},
				},
				Jobs: atc.JobConfigs{
					{Name: "a"},
					{Name: "b"},
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{
								Name: "some-input", Resource: "some-resource",
								Passed: []string{"a"}, Params: atc.Params{"some": "params"},
							}},
							{Config: &atc.GetStep{
								Name: "some-other-input", Resource: "some-other-resource",
								Passed: []string{"b"}, Params: atc.Params{"some": "other-params"},
								Tags: []string{"some-tag"},
							}},
						},
					},
				},
			}
		}

		setupInputMapping := func(fixture *jobsAPIFixture) (atc.Version, atc.Version) {
			GinkgoHelper()
			first := atc.Version{"ref": "first"}
			second := atc.Version{"ref": "second"}
			fixture.Scenario.Run(
				fixture.Builder.WithResourceVersions("some-resource", first),
				fixture.Builder.WithResourceVersions("some-other-resource", second),
				fixture.Builder.WithNextInputMapping("some-job", dbtest.JobInputs{
					{Name: "some-input", Version: first},
					{Name: "some-other-input", Version: second},
				}),
			)
			return first, second
		}

		BeforeEach(func() {
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		It("returns 401 when unauthenticated", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			fakeAccess.IsAuthenticatedReturns(false)
			server = fixture.Serve()
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("returns 403 when authenticated but unauthorized", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			fakeAccess.IsAuthorizedReturns(false)
			server = fixture.Serve()
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 404 for a missing persisted job", func() {
			fixture := useJobsAPIFixture(
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}},
			)
			server = fixture.Serve()
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 from the narrow real Pipeline.Resources boundary", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			pipeline := resourcesErrorPipeline{Pipeline: fixture.Pipeline, err: errors.New("resources failed")}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when the real next-input read fails on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			doomed := fixture.doomedJob("some-job")
			pipeline := jobsAPIPipeline{
				Pipeline: fixture.Pipeline, jobName: "some-job", job: doomed,
			}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 404 while input versions have not been determined", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			inputs, determined, err := fixture.Job("some-job").GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(determined).To(BeFalse())
			Expect(inputs).To(BeNil())

			server = fixture.Serve()
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 from the later narrow Job.Config boundary after resolving real inputs", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			setupInputMapping(fixture)
			job := configErrorJob{Job: fixture.Job("some-job"), err: errors.New("config failed")}
			pipeline := jobsAPIPipeline{
				Pipeline: fixture.Pipeline, jobName: "some-job", job: job,
			}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns the two persisted input mappings and their real resource graph", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, inputConfig())
			firstVersion, secondVersion := setupInputMapping(fixture)
			job := fixture.Job("some-job")
			firstResource := fixture.Scenario.Resource("some-resource")
			secondResource := fixture.Scenario.Resource("some-other-resource")

			mapped, determined, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(determined).To(BeTrue())
			Expect(mapped).To(HaveLen(2))
			mappedByName := map[string]db.BuildInput{}
			for _, input := range mapped {
				mappedByName[input.Name] = input
			}
			Expect(mappedByName["some-input"].ResourceID).To(Equal(firstResource.ID()))
			Expect(mappedByName["some-input"].Version).To(Equal(firstVersion))
			Expect(mappedByName["some-other-input"].ResourceID).To(Equal(secondResource.ID()))
			Expect(mappedByName["some-other-input"].Version).To(Equal(secondVersion))
			Expect(firstResource.ID()).NotTo(Equal(secondResource.ID()))

			persistedConfig, err := job.Config()
			Expect(err).NotTo(HaveOccurred())
			Expect(persistedConfig.Inputs()).To(ContainElements(
				atc.JobInputParams{
					JobInput: atc.JobInput{
						Name: "some-input", Resource: "some-resource", Passed: []string{"a"},
					},
					Params: atc.Params{"some": "params"},
				},
				atc.JobInputParams{
					JobInput: atc.JobInput{
						Name: "some-other-input", Resource: "some-other-resource", Passed: []string{"b"},
					},
					Params: atc.Params{"some": "other-params"}, Tags: []string{"some-tag"},
				},
			))

			server = fixture.Serve()
			response := jobsAPIGet(server, path)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))
			actual := decodeJobsAPIResponse[[]atc.BuildInput](response)
			Expect(actual).To(HaveLen(2))
			actualByName := map[string]atc.BuildInput{}
			for _, input := range actual {
				actualByName[input.Name] = input
			}
			Expect(actualByName).To(HaveKeyWithValue("some-input", atc.BuildInput{
				Name: "some-input", Resource: "some-resource",
				Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "primary"},
				Version: firstVersion, Params: atc.Params{"some": "params"},
			}))
			Expect(actualByName).To(HaveKeyWithValue("some-other-input", atc.BuildInput{
				Name: "some-other-input", Resource: "some-other-resource",
				Type: dbtest.UniqueBaseResourceType, Source: atc.Source{"repository": "secondary"},
				Version: secondVersion, Params: atc.Params{"some": "other-params"},
				Tags: []string{"some-tag"},
			}))
		})
	})
	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds/:build_name", func() {
		var (
			server        *httptest.Server
			response      *http.Response
			fixture       *jobsAPIFixture
			config        atc.Config
			buildName     string
			persisted     db.Build
			expose        bool
			setup         func(*jobsAPIFixture)
			routePipeline func(*jobsAPIFixture) db.Pipeline
		)

		BeforeEach(func() {
			fakeAccess.IsAuthorizedReturns(true)
			fakeAccess.IsAuthenticatedReturns(true)
			config = atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
			buildName = ""
			expose = false
			routePipeline = nil
			setup = func(fixture *jobsAPIFixture) {
				persisted = createJobsAPIBuild(fixture.Job("some-job"), "api-named-build")
				startJobsAPIBuild(persisted)
				finishJobsAPIBuild(persisted, db.BuildStatusSucceeded)
				buildName = persisted.Name()
			}
		})

		JustBeforeEach(func() {
			fixture = useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			setup(fixture)
			if expose {
				Expect(fixture.Pipeline.Expose()).To(Succeed())
			}
			if routePipeline == nil {
				server = fixture.Serve()
			} else {
				server = fixture.ServePipeline(routePipeline(fixture))
			}
			response = jobsAPIGet(
				server,
				"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds/"+buildName,
			)
		})

		Context("when authorized", func() {
			It("returns the exact persisted build named in the request", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
				actual := decodeJobsAPIResponse[atc.Build](response)
				expectJobsAPIBuild(actual, persisted)
				Expect(actual.Name).To(Equal(buildName))
				Expect(actual.PipelineName).To(Equal("some-pipeline"))
				Expect(actual.EndTime).NotTo(BeZero())
			})

			Context("when the build is naturally absent", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						_ = fixture.Job("some-job")
						buildName = "does-not-exist"
					}
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when querying the real job for the build fails", func() {
				BeforeEach(func() {
					setup = func(fixture *jobsAPIFixture) {
						_ = fixture.Job("some-job")
						buildName = "does-not-exist"
					}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return jobsAPIPipeline{
							Pipeline: fixture.Pipeline,
							jobName:  "some-job",
							job:      fixture.doomedJob("some-job"),
						}
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is naturally absent", func() {
				BeforeEach(func() {
					config = atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}}
					setup = func(*jobsAPIFixture) {
						buildName = "1"
					}
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when finding the job through the real pipeline fails", func() {
				BeforeEach(func() {
					setup = func(*jobsAPIFixture) {
						buildName = "1"
					}
					routePipeline = func(fixture *jobsAPIFixture) db.Pipeline {
						return fixture.doomedPipeline()
					}
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(false)
			})

			Context("and the pipeline is private", func() {
				Context("when not authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(false)
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("when authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(true)
					})

					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					expose = true
				})

				It("returns the requested persisted build", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					actual := decodeJobsAPIResponse[atc.Build](response)
					expectJobsAPIBuild(actual, persisted)
				})
			})
		})
	})

	Describe("POST /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds/:build_name", func() {
		var server *httptest.Server

		rerunConfig := func() atc.Config {
			return atc.Config{
				Resources: atc.ResourceConfigs{{
					Name: "some-resource", Type: dbtest.BaseResourceType,
					Source: atc.Source{"repository": "rerun"},
				}},
				Jobs: atc.JobConfigs{{
					Name: "some-job",
					PlanSequence: []atc.Step{{Config: &atc.GetStep{
						Name: "some-input", Resource: "some-resource",
					}}},
				}},
			}
		}

		setupOriginalBuild := func(fixture *jobsAPIFixture) db.Build {
			GinkgoHelper()
			version := atc.Version{"ref": "original"}
			fixture.Scenario.Run(fixture.Builder.WithResourceVersions("some-resource", version))
			var original db.Build
			fixture.Scenario.Run(fixture.Builder.WithJobBuild(
				&original,
				"some-job",
				dbtest.JobInputs{{Name: "some-input", Version: version}},
				nil,
			))
			reloadJobsAPIBuild(original)
			Expect(original.InputsReady()).To(BeTrue())
			return original
		}

		rerunPath := func(buildName string) string {
			return fmt.Sprintf(
				"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds/%s",
				buildName,
			)
		}

		BeforeEach(func() {
			fakeAccess.IsAuthorizedReturns(true)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "rerun-user"})
		})

		It("persists a pending rerun linked to the real original build", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			original := setupOriginalBuild(fixture)
			server = fixture.Serve()
			response := jobsAPIPost(server, rerunPath(original.Name()))
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))

			actual := decodeJobsAPIResponse[atc.Build](response)
			job := fixture.Job("some-job")
			persisted, found, err := job.Build(actual.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			expectJobsAPIBuild(actual, persisted)
			Expect(persisted.Status()).To(Equal(db.BuildStatusPending))
			Expect(persisted.RerunOf()).To(Equal(original.ID()))
			Expect(persisted.RerunOfName()).To(Equal(original.Name()))
			Expect(persisted.RerunNumber()).To(Equal(1))
			Expect(persisted.Name()).To(Equal(original.Name() + ".1"))
			Expect(persisted.JobName()).To(Equal(job.Name()))
			Expect(persisted.PipelineID()).To(Equal(fixture.Pipeline.ID()))
			Expect(persisted.PipelineName()).To(Equal(fixture.Pipeline.Name()))
			Expect(persisted.TeamName()).To(Equal(fixture.Team.Name()))
			Expect(actual.CreatedBy).NotTo(BeNil())
			Expect(*actual.CreatedBy).To(Equal("rerun-user"))
			Expect(actual.StartTime).To(BeZero())
			Expect(actual.EndTime).To(BeZero())

			var count int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE rerun_of = $1`, original.ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1))
		})

		It("returns 500 for an ordinary persisted build whose inputs are not ready", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			original := createJobsAPIBuild(fixture.Job("some-job"), "original-user")
			Expect(original.InputsReady()).To(BeFalse())
			server = fixture.Serve()
			response := jobsAPIPost(server, rerunPath(original.Name()))
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))

			var count int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE rerun_of = $1`, original.ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})

		It("returns 404 for a missing persisted build", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			server = fixture.Serve()
			response := jobsAPIPost(server, rerunPath("missing"))
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the real Build lookup fails on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			doomed := fixture.doomedJob("some-job")
			pipeline := jobsAPIPipeline{
				Pipeline: fixture.Pipeline, jobName: "some-job", job: doomed,
			}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIPost(server, rerunPath("some-build"))
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 from the narrow RerunBuild boundary after a real Build lookup", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			original := setupOriginalBuild(fixture)
			job := rerunErrorJob{Job: fixture.Job("some-job"), err: errors.New("rerun failed")}
			pipeline := jobsAPIPipeline{
				Pipeline: fixture.Pipeline, jobName: "some-job", job: job,
			}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIPost(server, rerunPath(original.Name()))
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))

			var count int
			Expect(fixture.Real.Conn.QueryRow(
				`SELECT count(*) FROM builds WHERE rerun_of = $1`, original.ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, rerunConfig())
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIPost(server, rerunPath("some-build"))
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 404 for a missing persisted job", func() {
			fixture := useJobsAPIFixture(
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}},
			)
			server = fixture.Serve()
			response := jobsAPIPost(server, rerunPath("some-build"))
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/pause", func() {
		var server *httptest.Server
		path := "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/pause"
		config := atc.Config{
			Resources: atc.ResourceConfigs{{Name: "repository", Type: dbtest.BaseResourceType}},
			Jobs:      atc.JobConfigs{{Name: "job-name", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "repository-source", Resource: "repository"}}}}},
		}

		It("persists the authenticated user's pause state", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			job := fixture.Job("job-name")
			found, err := job.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.Paused()).To(BeTrue())
			Expect(job.PausedBy()).To(Equal("api-user"))
			Expect(job.PausedAt()).To(BeTemporally("~", time.Now(), time.Second))
		})

		It("returns 404 for a missing configured job", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when pausing a preloaded real job on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipeline := jobsAPIPipeline{Pipeline: fixture.Pipeline, jobName: "job-name", job: fixture.doomedJob("job-name")}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 401 without authentication", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(false)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/unpause", func() {
		var server *httptest.Server
		path := "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/unpause"
		config := atc.Config{
			Resources: atc.ResourceConfigs{{Name: "repository", Type: dbtest.BaseResourceType}},
			Jobs:      atc.JobConfigs{{Name: "job-name", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "repository-source", Resource: "repository"}}}}},
		}

		It("clears a real job's persisted pause state", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			job := fixture.Job("job-name")
			Expect(job.Pause("setup-user")).To(Succeed())
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			found, err := job.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.Paused()).To(BeFalse())
			Expect(job.PausedBy()).To(BeEmpty())
			Expect(job.PausedAt()).To(BeZero())
		})

		It("returns 404 for a missing configured job", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when unpausing a preloaded real job on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipeline := jobsAPIPipeline{Pipeline: fixture.Pipeline, jobName: "job-name", job: fixture.doomedJob("job-name")}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 401 without authentication", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(false)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/tasks/:step_name/cache", func() {
		var server *httptest.Server
		basePath := "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/tasks/compile/cache"
		config := atc.Config{Jobs: atc.JobConfigs{{Name: "job-name"}, {Name: "other-job"}}}

		seedCaches := func(fixture *jobsAPIFixture) (db.TaskCacheFactory, db.Job, db.Job) {
			GinkgoHelper()
			factory := db.NewTaskCacheFactory(fixture.Real.Conn)
			job := fixture.Job("job-name")
			otherJob := fixture.Job("other-job")
			_, err := factory.FindOrCreate(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			_, err = factory.FindOrCreate(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "other-path")
			Expect(err).NotTo(HaveOccurred())
			_, err = factory.FindOrCreate(atc.TaskCacheIdentity{JobID: otherJob.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			return factory, job, otherJob
		}

		It("deletes all matching step caches and preserves another job's cache", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			factory, job, otherJob := seedCaches(fixture)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodDelete, basePath)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response).To(IncludeHeaderEntries(map[string]string{"Content-Type": "application/json"}))
			Expect(decodeJobsAPIResponse[atc.ClearTaskCacheResponse](response).CachesRemoved).To(Equal(int64(2)))
			_, found, err := factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "other-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: otherJob.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("deletes only the selected cache path and preserves decoys", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			factory, job, otherJob := seedCaches(fixture)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodDelete, basePath+"?"+atc.ClearTaskCacheQueryPath+"=cache-path")
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeJobsAPIResponse[atc.ClearTaskCacheResponse](response).CachesRemoved).To(Equal(int64(1)))
			_, found, err := factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "other-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: otherJob.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("reports zero when no persisted cache matches", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			seedCaches(fixture)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodDelete, basePath+"?"+atc.ClearTaskCacheQueryPath+"=missing")
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeJobsAPIResponse[atc.ClearTaskCacheResponse](response).CachesRemoved).To(BeZero())
		})

		It("reports zero for a missing step and preserves persisted caches", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			factory, job, otherJob := seedCaches(fixture)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(
				server,
				http.MethodDelete,
				"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/tasks/missing-step/cache",
			)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(decodeJobsAPIResponse[atc.ClearTaskCacheResponse](response).CachesRemoved).To(BeZero())
			_, found, err := factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: job.ID()}, "compile", "other-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			_, found, err = factory.Find(atc.TaskCacheIdentity{JobID: otherJob.ID()}, "compile", "cache-path")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		})

		It("returns 404 for a missing configured job", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodDelete, basePath)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIRequest(server, http.MethodDelete, basePath)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when clearing a preloaded real job on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipeline := jobsAPIPipeline{Pipeline: fixture.Pipeline, jobName: "job-name", job: fixture.doomedJob("job-name")}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIRequest(server, http.MethodDelete, basePath)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 401 without authentication", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(false)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodDelete, basePath)
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/schedule", func() {
		var server *httptest.Server
		path := "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/schedule"
		config := atc.Config{Jobs: atc.JobConfigs{{Name: "job-name"}}}

		It("advances the persisted schedule-request timestamp", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			job := fixture.Job("job-name")
			before := job.ScheduleRequestedTime()
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			found, err := job.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.ScheduleRequestedTime()).To(BeTemporally(">", before))
		})

		It("returns 404 for a missing configured job", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "other-job"}}})
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("returns 500 when the real pipeline job lookup fails", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server = fixture.ServePipeline(fixture.doomedPipeline())
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 500 when scheduling a preloaded real job on its closed connection", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			pipeline := jobsAPIPipeline{Pipeline: fixture.Pipeline, jobName: "job-name", job: fixture.doomedJob("job-name")}
			server = fixture.ServePipeline(pipeline)
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("returns 401 without authentication", func() {
			fixture := useJobsAPIFixture(atc.PipelineRef{Name: "some-pipeline"}, config)
			fakeAccess.IsAuthenticatedReturns(false)
			server = fixture.Serve()
			response := jobsAPIRequest(server, http.MethodPut, path)
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})
