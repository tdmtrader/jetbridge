package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
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

var _ = Describe("Jobs API", func() {
	var fakeJob *dbfakes.FakeJob
	var versionedResourceTypes atc.ResourceTypes
	var fakePipeline *dbfakes.FakePipeline

	BeforeEach(func() {
		fakeJob = new(dbfakes.FakeJob)
		fakePipeline = new(dbfakes.FakePipeline)
		dbTeamFactory.FindTeamReturns(dbTeam, true, nil)
		dbTeam.PipelineReturns(fakePipeline, true, nil)

		versionedResourceTypes = atc.ResourceTypes{
			atc.ResourceType{
				Name:   "some-resource-1",
				Type:   "some-base-type-1",
				Source: atc.Source{"some": "source-1"},
			},
			atc.ResourceType{
				Name:   "some-resource-2",
				Type:   "some-base-type-2",
				Source: atc.Source{"some": "source-2"},
			},
			atc.ResourceType{
				Name:   "some-resource-3",
				Type:   "some-base-type-3",
				Source: atc.Source{"some": "source-3"},
			},
		}

		fakePipeline.ResourceTypesReturns([]db.ResourceType{
			fakeDBResourceType(versionedResourceTypes[0]),
			fakeDBResourceType(versionedResourceTypes[1]),
			fakeDBResourceType(versionedResourceTypes[2]),
		}, nil)
	})

	Describe("GET /api/v1/jobs", func() {
		var response *http.Response

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/jobs", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		BeforeEach(func() {
			dbJobFactory.VisibleJobsReturns([]atc.JobSummary{
				{
					ID:           1,
					Name:         "some-job",
					Paused:       true,
					PipelineID:   1,
					PipelineName: "some-pipeline",
					TeamName:     "some-team",

					Inputs: []atc.JobInputSummary{
						{
							Name:     "some-input",
							Resource: "some-input",
							Trigger:  false,
						},
						{
							Name:     "some-name",
							Resource: "some-other-input",
							Passed:   []string{"a", "b"},
							Trigger:  true,
						},
					},

					NextBuild: &atc.BuildSummary{
						ID:           3,
						Name:         "2",
						JobName:      "some-job",
						PipelineID:   1,
						PipelineName: "some-pipeline",
						TeamName:     "some-team",
						Status:       "started",
					},
					FinishedBuild: &atc.BuildSummary{
						ID:           1,
						Name:         "1",
						JobName:      "some-job",
						PipelineID:   1,
						PipelineName: "some-pipeline",
						TeamName:     "some-team",
						Status:       "succeeded",
						StartTime:    1,
						EndTime:      100,
					},

					Groups: []string{"group-1", "group-2"},
				},
			}, nil)
		})

		It("returns 200 OK", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("returns application/json", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		It("returns all jobs from public pipelines and pipelines in authenticated teams", func() {
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(body).To(MatchJSON(`[
			{
				"id": 1,
				"name": "some-job",
				"pipeline_id": 1,
				"pipeline_name": "some-pipeline",
				"team_name": "some-team",
				"paused": true,
				"next_build": {
					"id": 3,
					"team_name": "some-team",
					"name": "2",
					"status": "started",
					"job_name": "some-job",
					"pipeline_id": 1,
					"pipeline_name": "some-pipeline"
				},
				"finished_build": {
					"id": 1,
					"team_name": "some-team",
					"name": "1",
					"status": "succeeded",
					"job_name": "some-job",
					"pipeline_id": 1,
					"pipeline_name": "some-pipeline",
					"start_time": 1,
					"end_time": 100
				},
				"inputs": [
					{
						"name": "some-input",
						"resource": "some-input"
					},
					{
						"name": "some-name",
						"resource": "some-other-input",
						"passed": [
							"a",
							"b"
						],
						"trigger": true
					}
				],
				"groups": ["group-1", "group-2"]
			}
			]`))
		})

		Context("when getting the jobs fails", func() {
			BeforeEach(func() {
				dbJobFactory.VisibleJobsReturns(nil, errors.New("nope"))
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})

		Context("when there are no visible jobs", func() {
			BeforeEach(func() {
				dbJobFactory.VisibleJobsReturns(nil, nil)
			})

			It("returns empty array", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`[]`))
			})
		})

		Context("when not authenticated", func() {
			It("populates job factory with no team names", func() {
				Expect(dbJobFactory.VisibleJobsCallCount()).To(Equal(1))
				Expect(dbJobFactory.VisibleJobsArgsForCall(0)).To(BeEmpty())
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"some-team"})
			})

			It("constructs job factory with provided team names", func() {
				Expect(dbJobFactory.VisibleJobsCallCount()).To(Equal(1))
				Expect(dbJobFactory.VisibleJobsArgsForCall(0)).To(ContainElement("some-team"))
			})

			Context("user has the admin privilege", func() {
				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
				})

				It("returns all jobs from public and private pipelines from unauthenticated teams", func() {
					Expect(dbJobFactory.AllActiveJobsCallCount()).To(Equal(1))
				})
			})
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
		var response *http.Response
		var dashboardResponse []atc.JobSummary

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/some-team/pipelines/some-pipeline/jobs")
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when getting the dashboard succeeds", func() {

			BeforeEach(func() {

				dashboardResponse = []atc.JobSummary{
					{
						ID:                   1,
						Name:                 "job-1",
						PipelineID:           2,
						PipelineName:         "another-pipeline",
						PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
						TeamName:             "some-team",
						Paused:               true,
						NextBuild: &atc.BuildSummary{
							ID:                   3,
							Name:                 "2",
							JobName:              "job-1",
							PipelineID:           2,
							PipelineName:         "another-pipeline",
							PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
							TeamName:             "some-team",
							Status:               "started",
						},
						FinishedBuild: &atc.BuildSummary{
							ID:                   1,
							Name:                 "1",
							JobName:              "job-1",
							PipelineID:           2,
							PipelineName:         "another-pipeline",
							PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
							TeamName:             "some-team",
							Status:               "succeeded",
							StartTime:            1,
							EndTime:              100,
						},
						TransitionBuild: &atc.BuildSummary{
							ID:                   5,
							Name:                 "five",
							JobName:              "job-1",
							PipelineID:           2,
							PipelineName:         "another-pipeline",
							PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
							TeamName:             "some-team",
							Status:               "failed",
							StartTime:            101,
							EndTime:              200,
						},
						Inputs: []atc.JobInputSummary{
							{
								Name:     "input-1",
								Resource: "input-1",
							},
						},
						Groups: []string{
							"group-1", "group-2",
						},
					},
					{
						ID:                   2,
						Name:                 "job-2",
						PipelineID:           2,
						PipelineName:         "another-pipeline",
						PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
						TeamName:             "some-team",
						Paused:               true,
						NextBuild:            nil,
						FinishedBuild: &atc.BuildSummary{
							ID:                   4,
							Name:                 "1",
							JobName:              "job-2",
							PipelineID:           2,
							PipelineName:         "another-pipeline",
							PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
							TeamName:             "some-team",
							Status:               "succeeded",
							StartTime:            101,
							EndTime:              200,
						},
						TransitionBuild: nil,
						Inputs: []atc.JobInputSummary{
							{
								Name:     "input-2",
								Resource: "input-2",
							},
						},
						Groups: []string{
							"group-2",
						},
					},
					{
						ID:                   3,
						Name:                 "job-3",
						PipelineID:           2,
						PipelineName:         "another-pipeline",
						PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
						TeamName:             "some-team",
						Paused:               true,
						NextBuild:            nil,
						FinishedBuild:        nil,
						TransitionBuild:      nil,
						Inputs: []atc.JobInputSummary{
							{
								Name:     "input-3",
								Resource: "input-3",
							},
						},
						Groups: []string{},
					},
				}
				fakePipeline.DashboardReturns(dashboardResponse, nil)
			})

			Context("when not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				Context("when not authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(false)
					})

					Context("and the pipeline is private", func() {
						BeforeEach(func() {
							fakePipeline.PublicReturns(false)
						})

						It("returns 401", func() {
							Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
						})
					})

					Context("and the pipeline is public", func() {
						BeforeEach(func() {
							fakePipeline.PublicReturns(true)
						})

						It("returns 200 OK", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})
				})
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
					fakeAccess.IsAuthenticatedReturns(true)
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns each job's name and any running and finished builds", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchJSON(`[
							{
								"id": 1,
								"name": "job-1",
								"pipeline_id": 2,
								"pipeline_name": "another-pipeline",
								"pipeline_instance_vars": {
									"branch": "master"
								},
								"team_name": "some-team",
								"paused": true,
								"next_build": {
									"id": 3,
									"name": "2",
									"job_name": "job-1",
									"status": "started",
									"pipeline_id": 2,
									"pipeline_name": "another-pipeline",
									"pipeline_instance_vars": {
										"branch": "master"
									},
									"team_name": "some-team"
								},
								"finished_build": {
									"id": 1,
									"name": "1",
									"job_name": "job-1",
									"status": "succeeded",
									"pipeline_id": 2,
									"pipeline_name": "another-pipeline",
									"pipeline_instance_vars": {
										"branch": "master"
									},
									"team_name": "some-team",
									"start_time": 1,
									"end_time": 100
								},
								"transition_build": {
									"id": 5,
									"name": "five",
									"job_name": "job-1",
									"status": "failed",
									"pipeline_id": 2,
									"pipeline_name": "another-pipeline",
									"pipeline_instance_vars": {
										"branch": "master"
									},
									"team_name": "some-team",
									"start_time": 101,
									"end_time": 200
								},
								"inputs": [{"name": "input-1", "resource": "input-1"}],
								"groups": ["group-1", "group-2"]
							},
							{
								"id": 2,
								"name": "job-2",
								"pipeline_id": 2,
								"pipeline_name": "another-pipeline",
								"pipeline_instance_vars": {
									"branch": "master"
								},
								"team_name": "some-team",
								"paused": true,
								"finished_build": {
									"id": 4,
									"name": "1",
									"job_name": "job-2",
									"status": "succeeded",
									"pipeline_id": 2,
									"pipeline_name": "another-pipeline",
									"pipeline_instance_vars": {
										"branch": "master"
									},
									"team_name": "some-team",
									"start_time": 101,
									"end_time": 200
								},
								"inputs": [{"name": "input-2", "resource": "input-2"}],
								"groups": ["group-2"]
							},
							{
								"id": 3,
								"name": "job-3",
								"pipeline_id": 2,
								"pipeline_name": "another-pipeline",
								"pipeline_instance_vars": {
									"branch": "master"
								},
								"team_name": "some-team",
								"paused": true,
								"inputs": [{"name": "input-3", "resource": "input-3"}]
							}
						]`))
				})

				Context("when there are no jobs in dashboard", func() {
					BeforeEach(func() {
						dashboardResponse = []atc.JobSummary{}
						fakePipeline.DashboardReturns(dashboardResponse, nil)
					})
					It("should return an empty array", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`[]`))
					})
				})

				Context("when getting the dashboard fails", func() {
					Context("with an unknown error", func() {
						BeforeEach(func() {
							fakePipeline.DashboardReturns(nil, errors.New("oh no!"))
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})
				})
			})
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
		var request *http.Request
		var response *http.Response

		BeforeEach(func() {
			var err error

			request, err = http.NewRequest("POST", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds", nil)
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized and authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(true)
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when getting the job fails", func() {
				BeforeEach(func() {
					fakePipeline.JobReturns(nil, false, errors.New("errorrr"))
				})

				It("returns a 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is not found", func() {
				BeforeEach(func() {
					fakePipeline.JobReturns(nil, false, nil)
				})

				It("returns a 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when getting the job succeeds", func() {
				BeforeEach(func() {
					fakeJob.NameReturns("some-job")
					fakePipeline.JobReturns(fakeJob, true, nil)
				})

				Context("when the pipeline is a base template", func() {
					BeforeEach(func() {
						fakePipeline.TemplateReturns(true)
						fakePipeline.InstanceVarsReturns(nil)
					})

					It("returns 409 with a pointer to run-pipeline", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
						body, err := io.ReadAll(response.Body)
						Expect(err).ToNot(HaveOccurred())
						Expect(string(body)).To(ContainSubstring("fly run-pipeline"))
					})
				})

				Context("when manual triggering is disabled", func() {
					BeforeEach(func() {
						fakeJob.DisableManualTriggerReturns(true)
					})

					It("should return 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})

					It("does not trigger the build", func() {
						Expect(fakeJob.CreateBuildCallCount()).To(Equal(0))
					})
				})

				Context("when manual triggering is enabled", func() {
					BeforeEach(func() {
						fakeJob.DisableManualTriggerReturns(false)
					})

					Context("when the pipeline is owned by a durable workflow run", func() {
						BeforeEach(func() {
							fakeJob.CreateBuildReturns(nil, fmt.Errorf("manual build guard: %w", db.ErrWorkflowRunOwnedPipeline))
						})

						It("returns 409 Conflict", func() {
							Expect(response.StatusCode).To(Equal(http.StatusConflict))
						})

						It("does not schedule resource checks", func() {
							Expect(dbCheckFactory.TryCreateCheckCallCount()).To(BeZero())
						})
					})

					Context("when triggering the build fails", func() {
						BeforeEach(func() {
							fakeJob.CreateBuildReturns(nil, errors.New("nopers"))
						})
						It("returns a 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when triggering the build succeeds", func() {
						BeforeEach(func() {
							build := new(dbfakes.FakeBuild)
							build.IDReturns(42)
							build.NameReturns("1")
							build.JobNameReturns("some-job")
							build.PipelineNameReturns("a-pipeline")
							build.TeamNameReturns("some-team")
							build.StatusReturns(db.BuildStatusStarted)
							build.StartTimeReturns(time.Unix(1, 0))
							build.EndTimeReturns(time.Unix(100, 0))

							fakeJob.CreateBuildReturns(build, nil)
						})

						It("triggers the build", func() {
							Expect(fakeJob.CreateBuildCallCount()).To(Equal(1))
						})

						Context("when finding the pipeline resources fails", func() {
							BeforeEach(func() {
								fakePipeline.ResourcesReturns(nil, errors.New("nope"))
							})

							It("returns a 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})

						Context("when finding the pipeline resources succeeds", func() {
							var fakeResource *dbfakes.FakeResource

							BeforeEach(func() {
								fakeResource = new(dbfakes.FakeResource)
								fakeResource.NameReturns("some-input")
								fakeResource.CurrentPinnedVersionReturns(atc.Version{"some": "version"})

								fakePipeline.ResourcesReturns([]db.Resource{fakeResource}, nil)
							})

							Context("when finding the pipeline resource types fails", func() {
								BeforeEach(func() {
									fakePipeline.ResourceTypesReturns(nil, errors.New("nope"))
								})

								It("returns a 500", func() {
									Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
								})
							})

							Context("when finding the pipeline resources types succeeds", func() {
								var fakeResourceType *dbfakes.FakeResourceType

								BeforeEach(func() {
									fakeResourceType = new(dbfakes.FakeResourceType)
									fakeResourceType.NameReturns("some-input")

									fakePipeline.ResourceTypesReturns([]db.ResourceType{fakeResourceType}, nil)
								})

								It("fetches the job inputs", func() {
									Expect(fakeJob.InputsCallCount()).To(Equal(1))
								})

								Context("when it fails to fetch the job inputs", func() {
									BeforeEach(func() {
										fakeJob.InputsReturns(nil, errors.New("nope"))
									})

									It("returns a 500", func() {
										Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
									})
								})

								Context("when the job inputs are successfully fetched", func() {
									BeforeEach(func() {
										fakeJob.InputsReturns([]atc.JobInput{
											{
												Name:     "some-input",
												Resource: "some-input",
											},
										}, nil)
									})

									It("returns 200 OK", func() {
										Expect(response.StatusCode).To(Equal(http.StatusOK))
									})

									It("returns Content-Type 'application/json'", func() {
										expectedHeaderEntries := map[string]string{
											"Content-Type": "application/json",
										}
										Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
									})

									It("creates a check for the resource", func() {
										Expect(dbCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
									})

									It("runs the check from the current pinned version", func() {
										_, _, _, fromVersion, _, _, toDB := dbCheckFactory.TryCreateCheckArgsForCall(0)
										Expect(fromVersion).To(Equal(atc.Version{"some": "version"}))
										Expect(toDB).To(BeTrue())
									})

									It("returns the build", func() {
										body, err := io.ReadAll(response.Body)
										Expect(err).NotTo(HaveOccurred())

										Expect(body).To(MatchJSON(`{
							"id": 42,
							"name": "1",
							"job_name": "some-job",
							"status": "started",
							"api_url": "/api/v1/builds/42",
							"pipeline_name": "a-pipeline",
							"team_name": "some-team",
							"start_time": 1,
							"end_time": 100
						}`))
									})
								})
							})
						})
					})
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/inputs", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/inputs")
			Expect(err).NotTo(HaveOccurred())
		})

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

			Context("when not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when getting the job fails", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, errors.New("some-error"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job is not found", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, nil)
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when getting the job succeeds", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(fakeJob, true, nil)
					})

					Context("when getting the resources fails", func() {
						BeforeEach(func() {
							fakePipeline.ResourcesReturns(nil, errors.New("some-error"))
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when getting the resources succeeds", func() {
						BeforeEach(func() {
							resource1 := new(dbfakes.FakeResource)
							resource1.IDReturns(1)
							resource1.NameReturns("some-resource")
							resource1.TypeReturns("some-type")
							resource1.SourceReturns(atc.Source{"some": "source"})

							resource2 := new(dbfakes.FakeResource)
							resource1.IDReturns(2)
							resource2.NameReturns("some-other-resource")
							resource2.TypeReturns("some-other-type")
							resource2.SourceReturns(atc.Source{"some": "other-source"})

							fakePipeline.ResourcesReturns([]db.Resource{resource1, resource2}, nil)
						})

						Context("when getting the input versions for the job fails", func() {
							BeforeEach(func() {
								fakeJob.GetFullNextBuildInputsReturns(nil, false, errors.New("oh no!"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})

						Context("when the job has no input versions available", func() {
							BeforeEach(func() {
								fakeJob.GetFullNextBuildInputsReturns(nil, false, nil)
							})

							It("returns 404", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when the job has input versions", func() {
							BeforeEach(func() {
								inputs := []db.BuildInput{
									{
										Name:       "some-input",
										Version:    atc.Version{"some": "version"},
										ResourceID: 1,
									},
									{
										Name:       "some-other-input",
										Version:    atc.Version{"some": "other-version"},
										ResourceID: 2,
									},
								}

								fakeJob.GetFullNextBuildInputsReturns(inputs, true, nil)
							})

							It("fetches the job config", func() {
								Expect(fakeJob.ConfigCallCount()).To(Equal(1))
							})

							Context("when it fails to fetch the job config", func() {
								BeforeEach(func() {
									fakeJob.ConfigReturns(atc.JobConfig{}, errors.New("nope"))
								})

								It("returns a 500", func() {
									Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
								})
							})

							Context("when the job inputs are successfully fetched", func() {
								BeforeEach(func() {
									fakeJob.ConfigReturns(atc.JobConfig{
										Name: "some-job",
										PlanSequence: []atc.Step{
											{
												Config: &atc.GetStep{
													Name:     "some-input",
													Resource: "some-resource",
													Passed:   []string{"job-a", "job-b"},
													Params:   atc.Params{"some": "params"},
												},
											},
											{
												Config: &atc.GetStep{
													Name:     "some-other-input",
													Resource: "some-other-resource",
													Passed:   []string{"job-c", "job-d"},
													Params:   atc.Params{"some": "other-params"},
													Tags:     []string{"some-tag"},
												},
											},
										},
									}, nil)
								})

								It("returns 200 OK", func() {
									Expect(response.StatusCode).To(Equal(http.StatusOK))
								})

								It("returns Content-Type 'application/json'", func() {
									expectedHeaderEntries := map[string]string{
										"Content-Type": "application/json",
									}
									Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
								})

								It("returns the inputs", func() {
									body, err := io.ReadAll(response.Body)
									Expect(err).NotTo(HaveOccurred())

									Expect(body).To(MatchJSON(`[
									{
										"name": "some-input",
										"resource": "some-resource",
										"type": "some-type",
										"source": {"some": "source"},
										"version": {"some": "version"},
										"params": {"some": "params"}
									},
									{
										"name": "some-other-input",
										"resource": "some-other-resource",
										"type": "some-other-type",
										"source": {"some": "other-source"},
										"version": {"some": "other-version"},
										"params": {"some": "other-params"},
										"tags": ["some-tag"]
									}
								]`))
								})
							})
						})
					})
				})
			})
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
		var request *http.Request
		var response *http.Response

		BeforeEach(func() {
			var err error

			request, err = http.NewRequest("POST", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/some-job/builds/some-build", nil)
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized and authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(true)
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when getting the job fails", func() {
				BeforeEach(func() {
					fakePipeline.JobReturns(nil, false, errors.New("errorrr"))
				})

				It("returns a 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the job is not found", func() {
				BeforeEach(func() {
					fakePipeline.JobReturns(nil, false, nil)
				})

				It("returns a 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when getting the job succeeds", func() {
				BeforeEach(func() {
					fakeJob.NameReturns("some-job")
					fakePipeline.JobReturns(fakeJob, true, nil)
				})

				It("tries to get the build to rerun", func() {
					Expect(fakeJob.BuildCallCount()).To(Equal(1))
				})

				Context("when getting the build to rerun fails", func() {
					BeforeEach(func() {
						fakeJob.BuildReturns(nil, false, errors.New("oops"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the build to rerun is not found", func() {
					BeforeEach(func() {
						fakeJob.BuildReturns(nil, false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when getting the build to rerun succeeds", func() {
					var fakeBuild *dbfakes.FakeBuild
					BeforeEach(func() {
						fakeBuild = new(dbfakes.FakeBuild)
						fakeBuild.IDReturns(1)
						fakeBuild.NameReturns("1")
						fakeBuild.JobNameReturns("some-job")
						fakeBuild.PipelineNameReturns("a-pipeline")
						fakeBuild.TeamNameReturns("some-team")
						fakeBuild.StatusReturns(db.BuildStatusStarted)
						fakeBuild.StartTimeReturns(time.Unix(1, 0))
						fakeBuild.EndTimeReturns(time.Unix(100, 0))

						fakeJob.BuildReturns(fakeBuild, true, nil)
					})

					Context("when the build has no inputs", func() {
						BeforeEach(func() {
							fakeBuild.InputsReadyReturns(false)
						})

						It("returns a 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})

					Context("when the build is input ready", func() {
						BeforeEach(func() {
							fakeBuild.InputsReadyReturns(true)
						})
						Context("when the pipeline is owned by a durable workflow run", func() {
							BeforeEach(func() {
								fakeJob.RerunBuildReturns(nil, fmt.Errorf("rerun guard: %w", db.ErrWorkflowRunOwnedPipeline))
							})

							It("returns 409 Conflict", func() {
								Expect(response.StatusCode).To(Equal(http.StatusConflict))
							})
						})

						Context("when creating the rerun build fails", func() {
							BeforeEach(func() {
								fakeJob.RerunBuildReturns(nil, errors.New("nopers"))
							})

							It("returns a 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})

						Context("when creating the rerun build succeeds", func() {
							BeforeEach(func() {
								build := new(dbfakes.FakeBuild)
								build.IDReturns(2)
								build.NameReturns("1.1")
								build.JobNameReturns("some-job")
								build.PipelineNameReturns("a-pipeline")
								build.TeamNameReturns("some-team")
								build.StatusReturns(db.BuildStatusStarted)
								build.StartTimeReturns(time.Unix(1, 0))
								build.EndTimeReturns(time.Unix(100, 0))

								fakeJob.RerunBuildReturns(build, nil)
							})

							It("returns 200 OK", func() {
								Expect(response.StatusCode).To(Equal(http.StatusOK))
							})

							It("returns Content-Type 'application/json'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/json",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("returns the build", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchJSON(`{
							"id": 2,
							"name": "1.1",
							"job_name": "some-job",
							"status": "started",
							"api_url": "/api/v1/builds/2",
							"pipeline_name": "a-pipeline",
							"team_name": "some-team",
							"start_time": 1,
							"end_time": 100
						}`))
							})
						})
					})
				})
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/pause", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/pause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)

					fakePipeline.JobReturns(fakeJob, true, nil)
					fakeJob.PauseReturns(nil)
				})

				It("finds the job on the pipeline and pauses it", func() {
					jobName := fakePipeline.JobArgsForCall(0)
					Expect(jobName).To(Equal("job-name"))

					Expect(fakeJob.PauseCallCount()).To(Equal(1))

					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when the job is not found", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when finding the job fails", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job fails to be paused", func() {
					BeforeEach(func() {
						fakeJob.PauseReturns(errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job belongs to a server-owned source-selection pipeline", func() {
					BeforeEach(func() {
						fakeJob.PauseReturns(db.ErrAgentWorkflowResourceSourceImmutable)
					})

					It("returns a 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns Status Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/unpause", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/unpause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)

					fakePipeline.JobReturns(fakeJob, true, nil)
					fakeJob.UnpauseReturns(nil)
				})

				It("finds the job on the pipeline and unpauses it", func() {
					jobName := fakePipeline.JobArgsForCall(0)
					Expect(jobName).To(Equal("job-name"))

					Expect(fakeJob.UnpauseCallCount()).To(Equal(1))

					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when the job is not found", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when finding the job fails", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job fails to be unpaused", func() {
					BeforeEach(func() {
						fakeJob.UnpauseReturns(errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job belongs to a server-owned source-selection pipeline", func() {
					BeforeEach(func() {
						fakeJob.UnpauseReturns(db.ErrAgentWorkflowResourceSourceImmutable)
					})

					It("returns a 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns Status Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/tasks/:step_name/cache", func() {
		var (
			request  *http.Request
			response *http.Response
		)

		BeforeEach(func() {
			var err error

			request, err = http.NewRequest("DELETE", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/tasks/:step_name/cache", nil)
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthenticatedReturns(true)

					fakePipeline.JobReturns(fakeJob, true, nil)
					fakeJob.ClearTaskCacheReturns(1, nil)

				})

				Context("when no cachePath is passed", func() {
					It("it finds the right job", func() {
						jobName := fakePipeline.JobArgsForCall(0)
						Expect(jobName).To(Equal("job-name"))
					})

					It("it clears the db cache entries successfully", func() {
						Expect(fakeJob.ClearTaskCacheCallCount()).To(Equal(1))
						_, cachePath := fakeJob.ClearTaskCacheArgsForCall(0)
						Expect(cachePath).To(Equal(""))
					})

					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("it returns the number of rows deleted", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`{"caches_removed": 1}`))
					})

					Context("but no rows were deleted", func() {
						BeforeEach(func() {
							fakeJob.ClearTaskCacheReturns(0, nil)
						})

						It("it returns that 0 rows were deleted", func() {
							body, err := io.ReadAll(response.Body)
							Expect(err).NotTo(HaveOccurred())

							Expect(body).To(MatchJSON(`{"caches_removed": 0}`))
						})

					})
				})

				Context("when a cachePath is passed", func() {
					BeforeEach(func() {
						query := request.URL.Query()
						query.Add(atc.ClearTaskCacheQueryPath, "cache-path")
						request.URL.RawQuery = query.Encode()
					})

					It("it finds the right job", func() {
						jobName := fakePipeline.JobArgsForCall(0)
						Expect(jobName).To(Equal("job-name"))
					})

					It("it clears the db cache entries successfully", func() {
						Expect(fakeJob.ClearTaskCacheCallCount()).To(Equal(1))
						_, cachePath := fakeJob.ClearTaskCacheArgsForCall(0)
						Expect(cachePath).To(Equal("cache-path"))
					})

					It("returns 200 OK", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("it returns the number of rows deleted", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`{"caches_removed": 1}`))
					})

					Context("but no rows corresponding to the cachePath are deleted", func() {
						BeforeEach(func() {
							fakeJob.ClearTaskCacheReturns(0, nil)
						})

						It("it returns that 0 rows were deleted", func() {
							body, err := io.ReadAll(response.Body)
							Expect(err).NotTo(HaveOccurred())

							Expect(body).To(MatchJSON(`{"caches_removed": 0}`))
						})
					})
				})

				Context("when the job is not found", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when finding the job fails", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when there are problems removing the db cache entries", func() {
					BeforeEach(func() {
						fakeJob.ClearTaskCacheReturns(-1, errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns Status Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/schedule", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/some-team/pipelines/some-pipeline/jobs/job-name/schedule", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)

					fakePipeline.JobReturns(fakeJob, true, nil)
					fakeJob.RequestScheduleReturns(nil)
				})

				It("finds the job on the pipeline and schedules it", func() {
					jobName := fakePipeline.JobArgsForCall(0)
					Expect(jobName).To(Equal("job-name"))

					Expect(fakeJob.RequestScheduleCallCount()).To(Equal(1))

					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when the job is not found", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when finding the job fails", func() {
					BeforeEach(func() {
						fakePipeline.JobReturns(nil, false, errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job fails to be scheduled", func() {
					BeforeEach(func() {
						fakeJob.RequestScheduleReturns(errors.New("some-error"))
					})

					It("returns a 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns Status Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})
})

func fakeDBResourceType(t atc.ResourceType) *dbfakes.FakeResourceType {
	fake := new(dbfakes.FakeResourceType)
	fake.NameReturns(t.Name)
	fake.TypeReturns(t.Type)
	fake.SourceReturns(t.Source)
	return fake
}
