package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type pipelineListingFixture struct {
	pipelines map[string]db.Pipeline
}

func expectPersistedPipelineShape(pipeline db.Pipeline, expected atc.Config) {
	GinkgoHelper()

	Expect(pipeline.Groups()).To(Equal(expected.Groups))
	Expect(pipeline.Display()).To(Equal(expected.Display))
	jobs, err := pipeline.Jobs()
	Expect(err).NotTo(HaveOccurred())
	Expect(jobs).To(HaveLen(len(expected.Jobs)))
	for _, expectedJob := range expected.Jobs {
		job, found, err := pipeline.Job(expectedJob.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "job %q was absent", expectedJob.Name)
		Expect(job.Name()).To(Equal(expectedJob.Name))
	}
	resources, err := pipeline.Resources()
	Expect(err).NotTo(HaveOccurred())
	Expect(resources).To(HaveLen(len(expected.Resources)))
	for _, expectedResource := range expected.Resources {
		resource, found, err := pipeline.Resource(expectedResource.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "resource %q was absent", expectedResource.Name)
		Expect(resource.Name()).To(Equal(expectedResource.Name))
	}
	Expect(pipeline.LastUpdated()).NotTo(BeZero())
}

func persistPipelineListingFixture(realdb *realDB) pipelineListingFixture {
	GinkgoHelper()

	anotherTeam, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "another"})
	Expect(err).NotTo(HaveOccurred())

	mainPublicConfig := atc.Config{
		Groups: atc.GroupConfigs{{
			Name:      "group2",
			Jobs:      []string{"job3", "job4"},
			Resources: []string{"resource3", "resource4"},
		}},
		Jobs: atc.JobConfigs{{Name: "job3"}, {Name: "job4"}},
		Resources: atc.ResourceConfigs{
			{Name: "resource3", Type: "mock", Source: atc.Source{"key": "three"}},
			{Name: "resource4", Type: "mock", Source: atc.Source{"key": "four"}},
		},
		Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
	}
	mainPrivateConfig := atc.Config{
		Groups: atc.GroupConfigs{{
			Name:      "group1",
			Jobs:      []string{"job1", "job2"},
			Resources: []string{"resource1", "resource2"},
		}},
		Jobs: atc.JobConfigs{{Name: "job1"}, {Name: "job2"}},
		Resources: atc.ResourceConfigs{
			{Name: "resource1", Type: "mock", Source: atc.Source{"key": "one"}},
			{Name: "resource2", Type: "mock", Source: atc.Source{"key": "two"}},
		},
	}
	anotherPublicConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}
	anotherPrivateConfig := atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}

	pipelines := map[string]db.Pipeline{
		"public-main":   realdb.SavePipeline(realdb.Main, "public-pipeline", mainPublicConfig),
		"private-main":  realdb.SavePipeline(realdb.Main, "private-pipeline", mainPrivateConfig),
		"public-other":  realdb.SavePipeline(anotherTeam, "another-pipeline", anotherPublicConfig),
		"private-other": realdb.SavePipeline(anotherTeam, "another-private-pipeline", anotherPrivateConfig),
	}
	configs := map[string]atc.Config{
		"public-main":   mainPublicConfig,
		"private-main":  mainPrivateConfig,
		"public-other":  anotherPublicConfig,
		"private-other": anotherPrivateConfig,
	}
	Expect(pipelines["public-main"].Expose()).To(Succeed())
	Expect(pipelines["public-other"].Expose()).To(Succeed())
	Expect(pipelines["public-main"].Pause("api-test")).To(Succeed())
	Expect(pipelines["public-other"].Pause("api-test")).To(Succeed())

	archiveRequestedAt := time.Now()
	Expect(pipelines["private-main"].Archive()).To(Succeed())

	for name, pipeline := range pipelines {
		found, err := pipeline.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		expectPersistedPipelineShape(pipeline, configs[name])
	}

	Expect(pipelines["public-main"].Public()).To(BeTrue())
	Expect(pipelines["public-main"].Paused()).To(BeTrue())
	Expect(pipelines["public-main"].PausedBy()).To(Equal("api-test"))
	Expect(pipelines["public-main"].Archived()).To(BeFalse())
	Expect(pipelines["public-main"].Groups()).To(Equal(mainPublicConfig.Groups))
	Expect(pipelines["public-main"].Display()).To(Equal(mainPublicConfig.Display))
	Expect(pipelines["private-main"].Public()).To(BeFalse())
	Expect(pipelines["private-main"].Archived()).To(BeTrue())
	Expect(pipelines["private-main"].Paused()).To(BeTrue())
	Expect(pipelines["private-main"].PausedAt()).To(BeTemporally(">=", archiveRequestedAt))
	Expect(pipelines["private-main"].PausedBy()).To(Equal("automatic-pipeline-archiver"))
	Expect(pipelines["private-main"].Groups()).To(Equal(mainPrivateConfig.Groups))
	Expect(pipelines["private-main"].Display()).To(Equal(mainPrivateConfig.Display))
	Expect(pipelines["public-other"].Public()).To(BeTrue())
	Expect(pipelines["public-other"].Paused()).To(BeTrue())
	Expect(pipelines["public-other"].PausedBy()).To(Equal("api-test"))
	Expect(pipelines["public-other"].Archived()).To(BeFalse())
	Expect(pipelines["private-other"].Public()).To(BeFalse())
	Expect(pipelines["private-other"].Paused()).To(BeFalse())
	Expect(pipelines["private-other"].Archived()).To(BeFalse())

	return pipelineListingFixture{pipelines: pipelines}
}

func expectPresentedPipeline(actual atc.Pipeline, expected db.Pipeline) {
	GinkgoHelper()

	Expect(actual.ID).To(Equal(expected.ID()))
	Expect(actual.Name).To(Equal(expected.Name()))
	Expect(actual.InstanceVars).To(Equal(expected.InstanceVars()))
	Expect(actual.TeamName).To(Equal(expected.TeamName()))
	Expect(actual.Paused).To(Equal(expected.Paused()))
	Expect(actual.PausedBy).To(Equal(expected.PausedBy()))
	if expected.PausedAt().IsZero() {
		Expect(actual.PausedAt).To(BeZero())
	} else {
		Expect(actual.PausedAt).To(Equal(expected.PausedAt().Unix()))
	}
	Expect(actual.Public).To(Equal(expected.Public()))
	Expect(actual.Archived).To(Equal(expected.Archived()))
	Expect(actual.Groups).To(Equal(expected.Groups()))
	Expect(actual.Display).To(Equal(expected.Display()))
	Expect(actual.LastUpdated).To(Equal(expected.LastUpdated().Unix()))
}

func expectPipelineResponse(response *http.Response, expected ...db.Pipeline) {
	GinkgoHelper()

	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	var actual []atc.Pipeline
	Expect(json.Unmarshal(body, &actual)).To(Succeed())
	Expect(actual).To(HaveLen(len(expected)))

	actualByID := map[int]atc.Pipeline{}
	for _, pipeline := range actual {
		actualByID[pipeline.ID] = pipeline
	}
	Expect(actualByID).To(HaveLen(len(expected)))
	for _, pipeline := range expected {
		presented, found := actualByID[pipeline.ID()]
		Expect(found).To(BeTrue(), "pipeline ID %d was absent", pipeline.ID())
		expectPresentedPipeline(presented, pipeline)
	}
}

func normalizedInstanceVars(pipelines []db.Pipeline, pipelineName string) []atc.InstanceVars {
	GinkgoHelper()

	var normalized []atc.InstanceVars
	for _, pipeline := range pipelines {
		if pipeline.Name() != pipelineName {
			continue
		}
		instanceVars := pipeline.InstanceVars()
		if instanceVars == nil {
			instanceVars = atc.InstanceVars{}
		}
		normalized = append(normalized, instanceVars)
	}
	return normalized
}

var _ = Describe("Pipelines API", func() {
	var (
		dbPipeline *dbfakes.FakePipeline
		fakeTeam   *dbfakes.FakeTeam
	)
	BeforeEach(func() {
		// Reused only by the method-specific post-lookup fault seams below.
		dbPipeline = new(dbfakes.FakePipeline)
		// Reused only by the method-specific post-lookup team fault seams below.
		fakeTeam = new(dbfakes.FakeTeam)
	})

	Describe("GET /api/v1/pipelines", func() {
		var (
			response  *http.Response
			listingDB *realDB
			pipelines map[string]db.Pipeline
		)

		BeforeEach(func() {
			listingDB = useRealDB()
			fixture := persistPipelineListingFixture(listingDB)
			pipelines = fixture.pipelines
			server = listingDB.Serve()
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/pipelines", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
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

		It("returns public pipeline objects from both teams", func() {
			expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
		})

		Context("when team is set in user context", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"some-team"})
			})

			It("does not grant visibility to an unrelated team", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when not authenticated", func() {
			It("returns only public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["public-other"])
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"main"})
			})

			It("returns all pipelines of the team + all public pipelines", func() {
				expectPipelineResponse(response,
					pipelines["public-main"],
					pipelines["private-main"],
					pipelines["public-other"],
				)
			})

			Context("user has the Admin privilege", func() {
				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
				})

				It("user can see all private and public pipelines from all teams", func() {
					expectPipelineResponse(response,
						pipelines["public-main"],
						pipelines["private-main"],
						pipelines["public-other"],
						pipelines["private-other"],
					)
				})
			})

			Context("when the call to get active pipelines fails", func() {
				BeforeEach(func() {
					doomed := postgresRunner.OpenConn()
					Expect(doomed.Close()).To(Succeed())
					deps := listingDB.Deps
					deps.pipelineFactory = db.NewPipelineFactory(doomed, listingDB.LockFactory)
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})

				It("returns 500 internal server error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines", func() {
		var (
			response  *http.Response
			listingDB *realDB
			pipelines map[string]db.Pipeline
		)

		BeforeEach(func() {
			listingDB = useRealDB()
			fixture := persistPipelineListingFixture(listingDB)
			pipelines = fixture.pipelines
			server = listingDB.Serve()
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/teams/main/pipelines", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated as requested team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(true)
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

			It("returns the persisted team pipeline objects", func() {
				expectPipelineResponse(response, pipelines["public-main"], pipelines["private-main"])
				Expect(pipelines["private-main"].PausedAt()).NotTo(BeZero())
				Expect(pipelines["private-main"].PausedBy()).To(Equal("automatic-pipeline-archiver"))
			})

			It("returns all team's pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []map[string]any
				Expect(json.Unmarshal(body, &actual)).To(Succeed())

				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
					HaveKeyWithValue("id", BeNumerically("==", pipelines["private-main"].ID())),
				))
			})

			Context("when the call to get active pipelines fails", func() {
				BeforeEach(func() {
					server = newAPIServer(fakeDBDeps())
					DeferCleanup(server.Close)
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					// Retained fault seam: Team.Pipelines must fail after FindTeam
					// succeeds; a closed TeamFactory cannot reach the nested call.
					fakeTeam.PipelinesReturns(nil, errors.New("disaster"))
				})

				It("returns 500 internal server error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"])
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				expectPipelineResponse(response, pipelines["public-main"])
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name", func() {
		var (
			response       *http.Response
			detailDB       *realDB
			detailPipeline db.Pipeline
			requestTeam    = "main"
		)

		BeforeEach(func() {
			detailDB = useRealDB()
			detailPipeline = detailDB.SavePipeline(detailDB.Main, "some-specific-pipeline", atc.Config{
				Groups: atc.GroupConfigs{
					{Name: "group1", Jobs: []string{"job1", "job2"}, Resources: []string{"resource1", "resource2"}},
					{Name: "group2", Jobs: []string{"job3", "job4"}, Resources: []string{"resource3", "resource4"}},
				},
				Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
			})
			Expect(detailPipeline.Expose()).To(Succeed())
			server = detailDB.Serve()
			requestTeam = "main"
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/some-specific-pipeline", nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
				Expect(detailPipeline.Hide()).To(Succeed())
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("when authenticated as requested team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			It("returns 200 ok", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns application/json", func() {
				expectedHeaderEntries := map[string]string{
					"Content-Type": "application/json",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
			})

			It("returns a pipeline JSON", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				var pipeline atc.Pipeline
				Expect(json.Unmarshal(body, &pipeline)).To(Succeed())
				Expect(pipeline.ID).To(Equal(detailPipeline.ID()))
				Expect(pipeline.Name).To(Equal(detailPipeline.Name()))
				Expect(pipeline.TeamName).To(Equal("main"))
				Expect(pipeline.Public).To(BeTrue())
				Expect(pipeline.Groups).To(Equal(detailPipeline.Groups()))
				Expect(pipeline.Display).To(Equal(detailPipeline.Display()))
			})
		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(false)

			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when not authenticated at all", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Hide()).To(Succeed())
				})

				It("returns 401", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					Expect(detailPipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/badge", func() {
		var response *http.Response
		var (
			badgeDB       *realDB
			badgePipeline db.Pipeline
			teamName      = "some-team"
		)

		persistBadgePipeline := func(config atc.Config, statuses map[string]db.BuildStatus) {
			badgeDB = useRealDB()
			badgePipeline = badgeDB.SavePipeline(badgeDB.Main, "some-pipeline", config)
			server = badgeDB.Serve()
			teamName = "main"
			for jobName, status := range statuses {
				job, found, err := badgePipeline.Job(jobName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				build, err := job.CreateBuild("api-badge-test")
				Expect(err).NotTo(HaveOccurred())
				started, err := build.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				Expect(build.Finish(status)).To(Succeed())
			}
		}

		BeforeEach(func() {
			teamName = "some-team"
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/" + teamName + "/pipelines/some-pipeline/badge")
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthorizedReturns(false)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "private-job"}}}, nil)
				})

				Context("when user is authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(true)
					})
					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})

				Context("when user is not authenticated", func() {
					BeforeEach(func() {
						fakeAccess.IsAuthenticatedReturns(false)
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "public-job"}}}, nil)
					Expect(badgePipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when the pipeline has no finished builds", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "no-build"}}}, nil)
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type as image/svg+xml and disables caching", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type":  "image/svg+xml",
						"Cache-Control": "no-cache, no-store, must-revalidate",
						"Expires":       "0",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns an unknown badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="98" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="98" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#9f9f9f" d="M37 0h61v20H37z" />
      <path fill="url(#b)" d="M0 0h98v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="66.5" y="15" fill="#010101" fill-opacity=".3">unknown</text>
      <text x="66.5" y="14">unknown</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has a successful build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded})
				})

				It("returns a successful badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="88" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#44cc11" d="M37 0h51v20H37z" />
      <path fill="url(#b)" d="M0 0h88v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="61.5" y="15" fill="#010101" fill-opacity=".3">passing</text>
      <text x="61.5" y="14">passing</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has an aborted build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted})
				})

				It("returns an aborted badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="90" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="90" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#8f4b2d" d="M37 0h53v20H37z" />
      <path fill="url(#b)" d="M0 0h90v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="62.5" y="15" fill="#010101" fill-opacity=".3">aborted</text>
      <text x="62.5" y="14">aborted</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has an errored build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}, {Name: "errored"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted, "errored": db.BuildStatusErrored})
				})

				It("returns an errored badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="88" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="88" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#fe7d37" d="M37 0h51v20H37z" />
      <path fill="url(#b)" d="M0 0h88v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="61.5" y="15" fill="#010101" fill-opacity=".3">errored</text>
      <text x="61.5" y="14">errored</text>
   </g>
</svg>`))
				})
			})

			Context("when the pipeline has a failed build", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "succeeded"}, {Name: "aborted"}, {Name: "errored"}, {Name: "failed"}}}, map[string]db.BuildStatus{"succeeded": db.BuildStatusSucceeded, "aborted": db.BuildStatusAborted, "errored": db.BuildStatusErrored, "failed": db.BuildStatusFailed})
				})

				It("returns a failed badge", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="80" height="20">
   <linearGradient id="b" x2="0" y2="100%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1" />
      <stop offset="1" stop-opacity=".1" />
   </linearGradient>
   <mask id="a">
      <rect width="80" height="20" rx="3" fill="#fff" />
   </mask>
   <g mask="url(#a)">
      <path fill="#555" d="M0 0h37v20H0z" />
      <path fill="#e05d44" d="M37 0h43v20H37z" />
      <path fill="url(#b)" d="M0 0h80v20H0z" />
   </g>
   <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text>
      <text x="18.5" y="14">build</text>
      <text x="57.5" y="15" fill="#010101" fill-opacity=".3">failing</text>
      <text x="57.5" y="14">failing</text>
   </g>
</svg>`))
				})
			})
		})
	})

	Describe("DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name", func() {
		var (
			response    *http.Response
			deleteDB    *realDB
			requestTeam string
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			pipelineName := "a-pipeline-name"
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/"+pipelineName, nil)
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when deleting succeeds", func() {
					BeforeEach(func() {
						deleteDB = useRealDB()
						deleteDB.SavePipeline(deleteDB.Main, "a-pipeline-name", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = deleteDB.Serve()
						requestTeam = "main"
					})

					It("returns 204 and removes the named pipeline from PostgreSQL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNoContent))
						pipeline, found, err := deleteDB.Main.Pipeline(atc.PipelineRef{Name: "a-pipeline-name"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeFalse())
						Expect(pipeline).To(BeNil())
					})
				})

				Context("when an error occurs destroying the pipeline", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.Destroy must fail after
						// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.DestroyReturns(errors.New("disaster!"))
					})

					It("returns a 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.Destroy must return the
						// template conflict after Team.Pipeline succeeds; a closed
						// TeamFactory cannot reach the nested method.
						dbPipeline.DestroyReturns(db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409 Conflict", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when the user is not logged in", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/pause", func() {
		var (
			response          *http.Response
			realdb            *realDB
			persistedPipeline db.Pipeline
			requestTeam       = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/pause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when pausing the pipeline succeeds", func() {
					BeforeEach(func() {
						realdb = useRealDB()
						persistedPipeline = realdb.SavePipeline(realdb.Main, "a-pipeline", atc.Config{
							Jobs: atc.JobConfigs{{Name: "job"}},
						})
						server = realdb.Serve()
						requestTeam = "main"
						fakeAccess.UserInfoReturns(atc.UserInfo{DisplayUserId: "api-user"})
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists pipeline pause through PostgreSQL", func() {
						found, err := persistedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(persistedPipeline.Paused()).To(BeTrue())
						Expect(persistedPipeline.PausedBy()).To(Equal("api-user"))
						Expect(persistedPipeline.PausedAt()).NotTo(BeZero())
					})
				})

				Context("when pausing the pipeline fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.Pause must fail after
						// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.PauseReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.Pause must return the
						// template conflict after Team.Pipeline succeeds; a closed
						// TeamFactory cannot reach the nested method.
						dbPipeline.PauseReturns(db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/archive", func() {
		var (
			response         *http.Response
			archiveDB        *realDB
			archivedPipeline db.Pipeline
			archiveConfig    atc.Config
			requestedAt      time.Time
			requestTeam      = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		JustBeforeEach(func() {
			request, _ := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/archive", nil)
			var err error
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when archiving succeeds", func() {
			BeforeEach(func() {
				archiveDB = useRealDB()
				archiveConfig = atc.Config{
					Groups: atc.GroupConfigs{{Name: "release", Jobs: []string{"ship"}, Resources: []string{"artifact"}}},
					Jobs:   atc.JobConfigs{{Name: "ship"}},
					Resources: atc.ResourceConfigs{{
						Name: "artifact", Type: "mock", Source: atc.Source{"uri": "archive://artifact"},
					}},
					Display: &atc.DisplayConfig{BackgroundImage: "archive.jpg"},
				}
				archivedPipeline = archiveDB.SavePipeline(archiveDB.Main, "a-pipeline", archiveConfig)
				server = archiveDB.Serve()
				requestTeam = "main"
				requestedAt = time.Now()
			})

			It("returns 200 and archives the pipeline in PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				found, err := archivedPipeline.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(archivedPipeline.Archived()).To(BeTrue())
				Expect(archivedPipeline.Paused()).To(BeTrue())
				Expect(archivedPipeline.PausedBy()).To(Equal("automatic-pipeline-archiver"))
				Expect(archivedPipeline.PausedAt()).To(BeTemporally(">=", requestedAt))
				Expect(archivedPipeline.LastUpdated()).To(BeTemporally(">=", requestedAt))
				Expect(archivedPipeline.Groups()).To(Equal(archiveConfig.Groups))
				Expect(archivedPipeline.Display()).To(Equal(archiveConfig.Display))
				expectPersistedPipelineShape(archivedPipeline, archiveConfig)
			})
		})

		Context("when archiving the pipeline fails due to the DB", func() {
			BeforeEach(func() {
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				fakeTeam.PipelineReturns(dbPipeline, true, nil)
				// Retained fault seam: Pipeline.Archive must fail after
				// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
				dbPipeline.ArchiveReturns(errors.New("pq: a db error"))
			})

			It("gives a server error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})

		Context("when the pipeline is an immutable workflow-run template", func() {
			BeforeEach(func() {
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				fakeTeam.PipelineReturns(dbPipeline, true, nil)
				// Retained semantic seam: Pipeline.Archive must return the
				// template conflict after Team.Pipeline succeeds; a closed
				// TeamFactory cannot reach the nested method.
				dbPipeline.ArchiveReturns(db.ErrWorkflowRunTemplateImmutable)
			})

			It("returns 409 Conflict", func() {
				Expect(response.StatusCode).To(Equal(http.StatusConflict))
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/unpause", func() {
		var (
			response         *http.Response
			unpauseDB        *realDB
			unpausedPipeline db.Pipeline
			requestTeam      = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/unpause", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when unpausing the pipeline succeeds", func() {
					BeforeEach(func() {
						unpauseDB = useRealDB()
						unpausedPipeline = unpauseDB.SavePipeline(unpauseDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(unpausedPipeline.Pause("fixture")).To(Succeed())
						server = unpauseDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists the unpaused state", func() {
						found, err := unpausedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(unpausedPipeline.Paused()).To(BeFalse())
						Expect(unpausedPipeline.PausedBy()).To(BeEmpty())
						Expect(unpausedPipeline.PausedAt()).To(BeZero())
					})
				})

				Context("when unpausing the pipeline fails for an unknown reason", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.Unpause must fail after
						// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.UnpauseReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.Unpause must return the
						// template conflict after Team.Pipeline succeeds; a closed
						// TeamFactory cannot reach the nested method.
						dbPipeline.UnpauseReturns(db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/expose", func() {
		var (
			response        *http.Response
			exposeDB        *realDB
			exposedPipeline db.Pipeline
			requestTeam     = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/expose", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when exposing the pipeline succeeds", func() {
					BeforeEach(func() {
						exposeDB = useRealDB()
						exposedPipeline = exposeDB.SavePipeline(exposeDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = exposeDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists public visibility", func() {
						found, err := exposedPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(exposedPipeline.Public()).To(BeTrue())
					})
				})

				Context("when exposing the pipeline fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.Expose must fail after
						// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.ExposeReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.Expose must return the
						// template conflict after Team.Pipeline succeeds; a closed
						// TeamFactory cannot reach the nested method.
						dbPipeline.ExposeReturns(db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/hide", func() {
		var (
			response       *http.Response
			hideDB         *realDB
			hiddenPipeline db.Pipeline
			requestTeam    = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/hide", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})
			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when hiding the pipeline succeeds", func() {
					BeforeEach(func() {
						hideDB = useRealDB()
						hiddenPipeline = hideDB.SavePipeline(hideDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						Expect(hiddenPipeline.Expose()).To(Succeed())
						server = hideDB.Serve()
						requestTeam = "main"
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("persists private visibility", func() {
						found, err := hiddenPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(hiddenPipeline.Public()).To(BeFalse())
					})
				})

				Context("when hiding the pipeline fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.Hide must fail after
						// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.HideReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.Hide must return the
						// template conflict after Team.Pipeline succeeds; a closed
						// TeamFactory cannot reach the nested method.
						dbPipeline.HideReturns(db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/ordering", func() {
		var (
			response      *http.Response
			pipelineNames []string
			orderingDB    *realDB
			orderingTeam  db.Team
			initialNames  []string
			requestTeam   = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			pipelineNames = []string{
				"a-pipeline",
				"another-pipeline",
				"yet-another-pipeline",
				"one-final-pipeline",
				"just-kidding",
			}
		})

		JustBeforeEach(func() {
			requestPayload, err := json.Marshal(pipelineNames)
			Expect(err).NotTo(HaveOccurred())

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/ordering", bytes.NewBuffer(requestPayload))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						orderingDB = useRealDB()
						var err error
						orderingTeam, err = orderingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						for _, name := range []string{
							"just-kidding",
							"a-pipeline",
							"one-final-pipeline",
							"yet-another-pipeline",
							"another-pipeline",
						} {
							orderingDB.SavePipeline(orderingTeam, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						initialPipelines, err := orderingTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						initialNames = make([]string, len(initialPipelines))
						for i, pipeline := range initialPipelines {
							initialNames[i] = pipeline.Name()
						}
						server = orderingDB.Serve()
					})

					It("persists the requested order from a deliberately different initial order", func() {
						Expect(initialNames).To(Equal([]string{
							"just-kidding",
							"a-pipeline",
							"one-final-pipeline",
							"yet-another-pipeline",
							"another-pipeline",
						}))
						pipelines, err := orderingTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualNames := make([]string, len(pipelines))
						for i, pipeline := range pipelines {
							actualNames[i] = pipeline.Name()
						}
						Expect(actualNames).To(Equal(pipelineNames))
						Expect(actualNames).NotTo(Equal(initialNames))
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained semantic seam: Team.OrderPipelines must return
						// ErrPipelineNotFound after FindTeam succeeds; a closed
						// TeamFactory cannot reach the nested method.
						fakeTeam.OrderPipelinesReturns(db.ErrPipelineNotFound{Name: "a-pipeline"})
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline' not found"))
					})
				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained fault seam: Team.OrderPipelines must fail after
						// FindTeam succeeds; a closed TeamFactory fails earlier.
						fakeTeam.OrderPipelinesReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/ordering", func() {
		var (
			response            *http.Response
			instanceVars        []atc.InstanceVars
			withinDB            *realDB
			withinTeam          db.Team
			initialInstanceVars []atc.InstanceVars
			requestTeam         = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			instanceVars = []atc.InstanceVars{
				{"branch": "test"},
				{},
				{"branch": "test-2"},
			}
		})

		JustBeforeEach(func() {
			requestPayload, err := json.Marshal(instanceVars)
			Expect(err).NotTo(HaveOccurred())

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/ordering", bytes.NewBuffer(requestPayload))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when requester belongs to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						withinDB = useRealDB()
						var err error
						withinTeam, err = withinDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}} {
							_, _, err := withinTeam.SavePipeline(atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())
						}
						initialPipelines, err := withinTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						initialInstanceVars = normalizedInstanceVars(initialPipelines, "a-pipeline")
						Expect(initialInstanceVars).To(Equal([]atc.InstanceVars{
							{"branch": "test-2"},
							{},
							{"branch": "test"},
						}))
						server = withinDB.Serve()
					})

					It("persists the requested instance order from a deliberately different initial order", func() {
						pipelines, err := withinTeam.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualInstanceVars := normalizedInstanceVars(pipelines, "a-pipeline")
						Expect(actualInstanceVars).To(Equal(instanceVars))
						Expect(actualInstanceVars).NotTo(Equal(initialInstanceVars))
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained semantic seam: Team.OrderPipelinesWithinGroup must
						// return ErrPipelineNotFound after FindTeam succeeds; a closed
						// TeamFactory cannot reach the nested method.
						fakeTeam.OrderPipelinesWithinGroupReturns(db.ErrPipelineNotFound{Name: "a-pipeline"})
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline' not found"))
					})
				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained fault seam: Team.OrderPipelinesWithinGroup must fail
						// after FindTeam succeeds; a closed TeamFactory fails earlier.
						fakeTeam.OrderPipelinesWithinGroupReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/versions-db", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("GET", server.URL+"/api/v1/teams/a-team/pipelines/a-pipeline/versions-db", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				fakeTeam.PipelineReturns(dbPipeline, true, nil)
			})

			Context("when getting the debug versions db works", func() {
				BeforeEach(func() {
					scopeID := 789

					// Retained data seam: Pipeline.LoadDebugVersionsDB returns this
					// purpose-built graph after Team.Pipeline succeeds; a closed
					// TeamFactory cannot reach the nested method.
					dbPipeline.LoadDebugVersionsDBReturns(
						&atc.DebugVersionsDB{
							ResourceVersions: []atc.DebugResourceVersion{
								{
									VersionID:  73,
									ResourceID: 127,
									CheckOrder: 123,
									ScopeID:    111,
								},
							},
							BuildOutputs: []atc.DebugBuildOutput{
								{
									DebugResourceVersion: atc.DebugResourceVersion{
										VersionID:  73,
										ResourceID: 127,
										CheckOrder: 123,
										ScopeID:    111,
									},
									BuildID: 66,
									JobID:   13,
								},
							},
							BuildInputs: []atc.DebugBuildInput{
								{
									DebugResourceVersion: atc.DebugResourceVersion{
										VersionID:  66,
										ResourceID: 77,
										CheckOrder: 88,
										ScopeID:    222,
									},
									BuildID:   66,
									JobID:     13,
									InputName: "some-input-name",
								},
							},
							BuildReruns: []atc.DebugBuildRerun{
								{
									JobID:   13,
									BuildID: 111,
									RerunOf: 222,
								},
							},
							Jobs: []atc.DebugJob{
								{
									ID:   13,
									Name: "bad-luck-job",
								},
							},
							Resources: []atc.DebugResource{
								{
									ID:      127,
									Name:    "resource-127",
									ScopeID: nil,
								},
								{
									ID:      128,
									Name:    "resource-128",
									ScopeID: &scopeID,
								},
							},
						},
						nil,
					)
				})

				It("constructs teamDB with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns application/json", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns a json representation of all the versions in the pipeline", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchJSON(`{
				"ResourceVersions": [
					{
						"VersionID": 73,
						"ResourceID": 127,
						"CheckOrder": 123,
						"ScopeID": 111
			    }
				],
				"BuildOutputs": [
					{
						"VersionID": 73,
						"ResourceID": 127,
						"BuildID": 66,
						"JobID": 13,
						"CheckOrder": 123,
						"ScopeID": 111
					}
				],
				"BuildInputs": [
					{
						"VersionID": 66,
						"ResourceID": 77,
						"BuildID": 66,
						"JobID": 13,
						"CheckOrder": 88,
						"ScopeID": 222,
						"InputName": "some-input-name"
					}
				],
				"BuildReruns": [
					{
						"JobID": 13,
						"BuildID": 111,
						"RerunOf": 222
					}
				],
				"Jobs": [
					{
						"ID": 13,
						"Name": "bad-luck-job"
					}
				],
				"Resources": [
					{
						"ID": 127,
						"Name": "resource-127",
						"ScopeID": null
					},
					{
						"ID": 128,
						"Name": "resource-128",
						"ScopeID": 789
					}
				]
				}`))
				})
			})

			Context("when getting the debug versions db fails", func() {
				BeforeEach(func() {
					// Retained fault seam: Pipeline.LoadDebugVersionsDB must fail
					// after Team.Pipeline succeeds; a closed TeamFactory fails earlier.
					dbPipeline.LoadDebugVersionsDBReturns(nil, errors.New("nope"))
				})

				It("constructs teamDB with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})

				It("does not return application/json", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "",
					}
					Expect(response).ShouldNot(IncludeHeaderEntries(expectedHeaderEntries))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/rename", func() {
		var (
			response    *http.Response
			requestBody string
			renameDB    *realDB
			renameTeam  db.Team
			requestTeam = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			requestBody = `{"name":"some-new-name"}`
		})

		JustBeforeEach(func() {
			var err error

			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/rename", bytes.NewBufferString(requestBody))
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
				})

				Context("when renaming succeeds", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						var err error
						renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
					})

					It("returns 200 and renames the pipeline in PostgreSQL", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
						_, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "a-pipeline"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeFalse())
						renamed, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "some-new-name"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(renamed.Name()).To(Equal("some-new-name"))
					})
				})

				Context("when the pipeline does not exist", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained semantic seam: Team.RenamePipeline must report not
						// found after FindTeam succeeds; a closed TeamFactory cannot
						// reach the nested method.
						fakeTeam.RenamePipelineReturns(false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when renaming the pipeline errors", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained fault seam: Team.RenamePipeline must fail after
						// FindTeam succeeds; a closed TeamFactory fails earlier.
						fakeTeam.RenamePipelineReturns(false, errors.New("whoops"))
					})

					It("returns a 500 internal server error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						// Retained semantic seam: Team.RenamePipeline must return the
						// template conflict after FindTeam succeeds; a closed
						// TeamFactory cannot reach the nested method.
						fakeTeam.RenamePipelineReturns(false, db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409 Conflict", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})

				Context("when the new name is an invalid identifier", func() {
					Context("and is a string", func() {
						BeforeEach(func() {
							renameDB = useRealDB()
							var err error
							renameTeam, err = renameDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
							Expect(err).NotTo(HaveOccurred())
							renameDB.SavePipeline(renameTeam, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
							server = renameDB.Serve()
							requestBody = `{"name":"_some-new-name"}`
						})

						It("returns a warning in the response body", func() {
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"warnings": [
									{
										"type": "invalid_identifier",
										"message": "pipeline: '_some-new-name' is not a valid identifier: must start with a lowercase letter or a number"
									}
								]
							}`))
							renamed, found, err := renameTeam.Pipeline(atc.PipelineRef{Name: "_some-new-name"})
							Expect(err).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							Expect(renamed.Name()).To(Equal("_some-new-name"))
						})
					})
					Context("and is an empty string", func() {
						BeforeEach(func() {
							requestBody = `{"name":""}`
						})

						It("returns 400 Bad Request and an error in the response body", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"errors": [
										"pipeline: identifier cannot be an empty string"
								]
							}`))
						})
					})
				})
			})

			Context("when requester does not belong to the team", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401 Unauthorized", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:pipeline_name/builds", func() {
		var (
			response     *http.Response
			queryParams  string
			requestTeam  = "some-team"
			requestPipe  = "some-pipeline"
			listDB       *realDB
			listPipeline db.Pipeline
			listBuild1   db.Build
			listBuild2   db.Build
		)

		persistPipelineWithBuilds := func(pipelineRef atc.PipelineRef, count int) (db.Pipeline, []db.Build) {
			GinkgoHelper()

			listDB = useRealDB()
			pipeline, _, err := listDB.Main.SavePipeline(
				pipelineRef,
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
				db.ConfigVersion(0),
				false,
			)
			Expect(err).NotTo(HaveOccurred())
			job, found, err := pipeline.Job("some-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			builds := make([]db.Build, 0, count)
			for range count {
				build, err := job.CreateBuild("api-test")
				Expect(err).NotTo(HaveOccurred())
				builds = append(builds, build)
			}
			server = listDB.Serve()
			requestTeam = "main"
			requestPipe = pipelineRef.Name
			return pipeline, builds
		}

		decodeBuilds := func() []atc.Build {
			GinkgoHelper()
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			var builds []atc.Build
			Expect(json.Unmarshal(body, &builds)).To(Succeed())
			return builds
		}

		BeforeEach(func() {
			requestTeam = "some-team"
			requestPipe = "some-pipeline"
			queryParams = ""
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/teams/" + requestTeam + "/pipelines/" + requestPipe + "/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			Context("and the pipeline is private", func() {
				BeforeEach(func() {
					persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
				})

				It("returns 401", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Context("and the pipeline is public", func() {
				BeforeEach(func() {
					pipeline, _ := persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 0)
					Expect(pipeline.Expose()).To(Succeed())
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when no params are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					_, persistedBuilds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 101)
				})

				It("applies the default limit without implicit range boundaries", func() {
					actual := decodeBuilds()
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

			Context("when all the params are passed", func() {
				var persistedBuilds []db.Build

				BeforeEach(func() {
					_, persistedBuilds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 7)
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=3",
						persistedBuilds[1].ID(),
						persistedBuilds[5].ID(),
					)
				})

				It("applies each of from, to, and limit to persisted builds", func() {
					By("using limit to truncate a wider inclusive range")
					actual := decodeBuilds()
					Expect(actual).To(HaveLen(3))
					Expect([]int{actual[0].ID, actual[1].ID, actual[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))

					By("using from and to to bound a range narrower than limit")
					boundedResponse, err := client.Get(fmt.Sprintf(
						"%s/api/v1/teams/main/pipelines/some-pipeline/builds?from=%d&to=%d&limit=6",
						server.URL,
						persistedBuilds[1].ID(),
						persistedBuilds[3].ID(),
					))
					Expect(err).NotTo(HaveOccurred())
					Expect(boundedResponse.StatusCode).To(Equal(http.StatusOK))
					DeferCleanup(boundedResponse.Body.Close)
					body, err := io.ReadAll(boundedResponse.Body)
					Expect(err).NotTo(HaveOccurred())
					var bounded []atc.Build
					Expect(json.Unmarshal(body, &bounded)).To(Succeed())
					Expect(bounded).To(HaveLen(3))
					Expect([]int{bounded[0].ID, bounded[1].ID, bounded[2].ID}).To(Equal([]int{
						persistedBuilds[1].ID(),
						persistedBuilds[2].ID(),
						persistedBuilds[3].ID(),
					}))
				})
			})

			Context("when getting the builds succeeds", func() {
				BeforeEach(func() {
					var builds []db.Build
					listPipeline, builds = persistPipelineWithBuilds(atc.PipelineRef{Name: "some-pipeline"}, 2)
					listBuild1 = builds[0]
					listBuild2 = builds[1]
					started, err := listBuild1.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					Expect(listBuild1.Finish(db.BuildStatusSucceeded)).To(Succeed())
					started, err = listBuild2.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					queryParams = "?limit=2"
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

				It("returns the builds", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					var builds []atc.Build
					Expect(json.Unmarshal(body, &builds)).To(Succeed())
					Expect(builds).To(HaveLen(2))
					byID := map[int]atc.Build{}
					for _, build := range builds {
						byID[build.ID] = build
					}
					for _, build := range []db.Build{listBuild1, listBuild2} {
						persisted, found, err := listDB.Deps.buildFactory.Build(build.ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						actual, ok := byID[build.ID()]
						Expect(ok).To(BeTrue())
						Expect(actual.Name).To(Equal(persisted.Name()))
						Expect(actual.Status).To(Equal(atc.BuildStatus(persisted.Status())))
						Expect(actual.TeamName).To(Equal(persisted.TeamName()))
						Expect(actual.PipelineName).To(Equal("some-pipeline"))
						Expect(actual.JobName).To(Equal("some-job"))
						Expect(actual.StartTime).To(Equal(persisted.StartTime().Unix()))
						if persisted.EndTime().IsZero() {
							Expect(actual.EndTime).To(BeZero())
						} else {
							Expect(actual.EndTime).To(Equal(persisted.EndTime().Unix()))
						}
					}
				})

				Context("when next/previous pages are available", func() {
					var (
						olderBuild  db.Build
						middleBuild db.Build
						newerBuild  db.Build
					)

					BeforeEach(func() {
						olderBuild = listBuild1
						middleBuild = listBuild2
						job, found, err := listPipeline.Job("some-job")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						newerBuild, err = job.CreateBuild("api-test")
						Expect(err).NotTo(HaveOccurred())
						queryParams = fmt.Sprintf("?from=%d&to=%d&limit=1", middleBuild.ID(), middleBuild.ID())
					})

					It("returns Link headers per rfc5988", func() {
						Expect(response.Header["Link"]).To(ConsistOf([]string{
							fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?to=%d&limit=1>; rel="next"`, externalURL, olderBuild.ID()),
							fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?from=%d&limit=1>; rel="previous"`, externalURL, newerBuild.ID()),
						}))
					})

					Context("and pipeline is instanced", func() {
						BeforeEach(func() {
							instancedPipeline, _, err := listDB.Main.SavePipeline(
								atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}},
								atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
								db.ConfigVersion(0),
								false,
							)
							Expect(err).NotTo(HaveOccurred())
							job, found, err := instancedPipeline.Job("some-job")
							Expect(err).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							olderBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							middleBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							newerBuild, err = job.CreateBuild("api-test")
							Expect(err).NotTo(HaveOccurred())
							queryParams = fmt.Sprintf(
								"?from=%d&to=%d&limit=1&vars.branch=%%22master%%22",
								middleBuild.ID(),
								middleBuild.ID(),
							)
						})

						It("returns Link headers per rfc5988", func() {
							link := fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?`, externalURL)
							Expect(response.Header["Link"]).To(ConsistOf([]string{
								fmt.Sprintf(`%sto=%d&limit=1&vars.branch=%%22master%%22>; rel="next"`, link, olderBuild.ID()),
								fmt.Sprintf(`%sfrom=%d&limit=1&vars.branch=%%22master%%22>; rel="previous"`, link, newerBuild.ID()),
							}))
						})
					})
				})
			})

			Context("when getting the build fails", func() {
				BeforeEach(func() {
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					fakeTeam.PipelineReturns(dbPipeline, true, nil)
					// Retained fault seam: Pipeline.Builds must fail after
					// Team.Pipeline succeeds; a closed TeamFactory fails earlier.
					dbPipeline.BuildsReturns(nil, db.Pagination{}, errors.New("oh no!"))
				})

				It("returns 404 Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})
		})
	})

	Describe("POST /api/v1/teams/:team_name/pipelines/:pipeline_name/builds", func() {
		var (
			plan         atc.Plan
			response     *http.Response
			postDB       *realDB
			postPipeline db.Pipeline
			postTeam     = "a-team"
		)

		BeforeEach(func() {
			postTeam = "a-team"
			plan = atc.Plan{
				ID: atc.PlanID("api-manual"),
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
			reqPayload, err := json.Marshal(plan)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("POST", server.URL+"/api/v1/teams/"+postTeam+"/pipelines/a-pipeline/builds", bytes.NewBuffer(reqPayload))
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
				postDB = useRealDB()
				postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
				server = postDB.Serve()
				postTeam = "main"
			})

			It("returns 401 without creating a build", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				builds, _, err := postPipeline.Builds(db.Page{Limit: 10})
				Expect(err).NotTo(HaveOccurred())
				Expect(builds).To(BeEmpty())
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("when not authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(false)
					postDB = useRealDB()
					postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
					server = postDB.Serve()
					postTeam = "main"
				})

				It("returns 403 without creating a build", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					builds, _, err := postPipeline.Builds(db.Page{Limit: 10})
					Expect(err).NotTo(HaveOccurred())
					Expect(builds).To(BeEmpty())
				})
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					fakeAccess.IsAuthorizedReturns(true)
				})

				Context("when creating a started build fails", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained fault seam: Pipeline.CreateStartedBuild must fail
						// after Team.Pipeline succeeds; a closed TeamFactory fails earlier.
						dbPipeline.CreateStartedBuildReturns(nil, errors.New("oh no!"))
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is owned by a durable workflow run", func() {
					BeforeEach(func() {
						dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						// Retained semantic seam: Pipeline.CreateStartedBuild must
						// return the durable-run conflict after Team.Pipeline succeeds;
						// a closed TeamFactory cannot reach the nested method.
						dbPipeline.CreateStartedBuildReturns(nil, fmt.Errorf("one-off build guard: %w", db.ErrWorkflowRunOwnedPipeline))
					})

					It("returns 409 Conflict", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})

				Context("when creating a started build succeeds", func() {
					BeforeEach(func() {
						postDB = useRealDB()
						postPipeline = postDB.SavePipeline(postDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = postDB.Serve()
						postTeam = "main"
					})

					It("returns 201 Created", func() {
						Expect(response.StatusCode).To(Equal(http.StatusCreated))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("creates a started build", func() {
						builds, _, err := postPipeline.Builds(db.Page{Limit: 1})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
						build, found, err := postDB.Deps.buildFactory.Build(builds[0].ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						found, err = build.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(build.Status()).To(Equal(db.BuildStatusStarted))
						Expect([]byte(*build.PublicPlan())).To(MatchJSON([]byte(*plan.Public())))
					})

					It("returns the created build", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						var actual atc.Build
						Expect(json.Unmarshal(body, &actual)).To(Succeed())
						builds, _, err := postPipeline.Builds(db.Page{Limit: 1})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
						build, found, err := postDB.Deps.buildFactory.Build(builds[0].ID())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						found, err = build.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(actual.ID).To(Equal(build.ID()))
						Expect(actual.Name).To(Equal(build.Name()))
						Expect(actual.TeamName).To(Equal(build.TeamName()))
						Expect(actual.PipelineName).To(Equal("a-pipeline"))
						Expect(actual.Status).To(Equal(atc.BuildStatus(db.BuildStatusStarted)))
						Expect(actual.APIURL).To(Equal(fmt.Sprintf("/api/v1/builds/%d", build.ID())))
						Expect(actual.StartTime).To(Equal(build.StartTime().Unix()))
						Expect([]byte(*build.PublicPlan())).To(MatchJSON([]byte(*plan.Public())))
					})
				})
			})
		})
	})
})
