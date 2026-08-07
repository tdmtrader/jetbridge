package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipelines API", func() {
	var (
		dbPipeline *dbfakes.FakePipeline
		fakeTeam   *dbfakes.FakeTeam
	)
	BeforeEach(func() {
		// Retained for selective post-lookup mutation failure seams.
		dbPipeline = new(dbfakes.FakePipeline)
		// Retained for selective team lookup/list failure seams.
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
			another, err := listingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "another"})
			Expect(err).NotTo(HaveOccurred())
			pipelines = map[string]db.Pipeline{
				"public-main": listingDB.SavePipeline(listingDB.Main, "public-pipeline", atc.Config{
					Groups:  atc.GroupConfigs{{Name: "group2", Jobs: []string{"job3", "job4"}, Resources: []string{"resource3", "resource4"}}},
					Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
				}),
				"private-main": listingDB.SavePipeline(listingDB.Main, "private-pipeline", atc.Config{
					Groups: atc.GroupConfigs{{Name: "group1", Jobs: []string{"job1", "job2"}, Resources: []string{"resource1", "resource2"}}},
				}),
				"public-other":  listingDB.SavePipeline(another, "another-pipeline", atc.Config{}),
				"private-other": listingDB.SavePipeline(another, "another-private-pipeline", atc.Config{}),
			}
			Expect(pipelines["public-main"].Expose()).To(Succeed())
			Expect(pipelines["public-other"].Expose()).To(Succeed())
			Expect(pipelines["public-main"].Pause("fixture")).To(Succeed())
			Expect(pipelines["public-other"].Pause("fixture")).To(Succeed())
			Expect(pipelines["private-main"].Archive()).To(Succeed())
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
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			var actual []atc.Pipeline
			Expect(json.Unmarshal(body, &actual)).To(Succeed())
			Expect(actual).To(HaveLen(2))
			byID := map[int]atc.Pipeline{}
			for _, pipeline := range actual {
				byID[pipeline.ID] = pipeline
			}
			main := byID[pipelines["public-main"].ID()]
			Expect(main.Name).To(Equal("public-pipeline"))
			Expect(main.TeamName).To(Equal("main"))
			Expect(main.Paused).To(BeTrue())
			Expect(main.Public).To(BeTrue())
			Expect(main.Groups).To(Equal(pipelines["public-main"].Groups()))
			Expect(main.Display).To(Equal(pipelines["public-main"].Display()))
			other := byID[pipelines["public-other"].ID()]
			Expect(other.Name).To(Equal("another-pipeline"))
			Expect(other.TeamName).To(Equal("another"))
			Expect(other.Paused).To(BeTrue())
			Expect(other.Public).To(BeTrue())
		})

		Context("when team is set in user context", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"some-team"})
			})

			It("does not grant visibility to an unrelated team", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []atc.Pipeline
				Expect(json.Unmarshal(body, &actual)).To(Succeed())
				Expect(actual).To(HaveLen(2))
			})
		})

		Context("when not authenticated", func() {
			It("returns only public pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				var actual []map[string]any
				err = json.Unmarshal(body, &actual)
				Expect(err).NotTo(HaveOccurred())
				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-other"].ID())),
				))
			})
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.TeamNamesReturns([]string{"main"})
			})

			It("returns all pipelines of the team + all public pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				var actual []map[string]any
				err = json.Unmarshal(body, &actual)
				Expect(err).NotTo(HaveOccurred())
				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
					HaveKeyWithValue("id", BeNumerically("==", pipelines["private-main"].ID())),
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-other"].ID())),
				))
			})

			Context("user has the Admin privilege", func() {
				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
				})

				It("user can see all private and public pipelines from all teams", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					var pipelinesResponse []atc.Pipeline
					err = json.Unmarshal(body, &pipelinesResponse)
					Expect(err).NotTo(HaveOccurred())
					Expect(len(pipelinesResponse)).To(Equal(4))
				})
			})

			Context("when the call to get active pipelines fails", func() {
				BeforeEach(func() {
					server = newAPIServer(fakeDBDeps())
					DeferCleanup(server.Close)
					dbPipelineFactory.VisiblePipelinesReturns(nil, errors.New("disaster"))
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
			another, err := listingDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "another"})
			Expect(err).NotTo(HaveOccurred())
			pipelines = map[string]db.Pipeline{
				"public-main": listingDB.SavePipeline(listingDB.Main, "public-pipeline", atc.Config{
					Groups:  atc.GroupConfigs{{Name: "group2", Jobs: []string{"job3", "job4"}, Resources: []string{"resource3", "resource4"}}},
					Display: &atc.DisplayConfig{BackgroundImage: "background.jpg"},
				}),
				"private-main": listingDB.SavePipeline(listingDB.Main, "private-pipeline", atc.Config{
					Groups: atc.GroupConfigs{{Name: "group1", Jobs: []string{"job1", "job2"}, Resources: []string{"resource1", "resource2"}}},
				}),
				"public-other":  listingDB.SavePipeline(another, "another-pipeline", atc.Config{}),
				"private-other": listingDB.SavePipeline(another, "another-private-pipeline", atc.Config{}),
			}
			Expect(pipelines["public-main"].Expose()).To(Succeed())
			Expect(pipelines["public-other"].Expose()).To(Succeed())
			Expect(pipelines["public-main"].Pause("fixture")).To(Succeed())
			Expect(pipelines["public-other"].Pause("fixture")).To(Succeed())
			Expect(pipelines["private-main"].Archive()).To(Succeed())
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
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []atc.Pipeline
				Expect(json.Unmarshal(body, &actual)).To(Succeed())
				Expect(actual).To(HaveLen(2))
				byID := map[int]atc.Pipeline{}
				for _, pipeline := range actual {
					byID[pipeline.ID] = pipeline
				}
				private := byID[pipelines["private-main"].ID()]
				Expect(private.Name).To(Equal("private-pipeline"))
				Expect(private.Archived).To(BeTrue())
				Expect(private.Public).To(BeFalse())
				Expect(private.Groups).To(Equal(pipelines["private-main"].Groups()))
				public := byID[pipelines["public-main"].ID()]
				Expect(public.Name).To(Equal("public-pipeline"))
				Expect(public.Paused).To(BeTrue())
				Expect(public.Public).To(BeTrue())
				Expect(public.Groups).To(Equal(pipelines["public-main"].Groups()))
				Expect(public.Display).To(Equal(pipelines["public-main"].Display()))
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
					fakeTeam.PipelinesReturns(nil, errors.New("disaster"))
				})

				It("returns 500 internal server error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when authenticated as another team", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []map[string]any
				Expect(json.Unmarshal(body, &actual)).To(Succeed())

				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
				))
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns only team's public pipelines", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				var actual []map[string]any
				Expect(json.Unmarshal(body, &actual)).To(Succeed())

				Expect(actual).To(ConsistOf(
					HaveKeyWithValue("id", BeNumerically("==", pipelines["public-main"].ID())),
				))
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
			dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
			dbPipeline.NameReturns("some-pipeline")
			fakeTeam.PipelineReturns(dbPipeline, true, nil)
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
					dbPipeline.PublicReturns(false)
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
					dbPipeline.PublicReturns(true)
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

			Context("when the pipeline has no finished builds", func() {
				BeforeEach(func() {
					persistBadgePipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "no-build"}}}, nil)
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
		var response *http.Response

		JustBeforeEach(func() {
			pipelineName := "a-pipeline-name"
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/teams/a-team/pipelines/"+pipelineName, nil)
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

					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					dbPipeline.NameReturns("a-pipeline-name")
					fakeTeam.PipelineReturns(dbPipeline, true, nil)
				})

				It("returns 204 No Content", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNoContent))
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("injects the proper pipelineDB", func() {
					pipelineRef := fakeTeam.PipelineArgsForCall(0)
					Expect(pipelineRef).To(Equal(atc.PipelineRef{Name: "a-pipeline-name"}))
				})

				It("deletes the named pipeline from the database", func() {
					Expect(dbPipeline.DestroyCallCount()).To(Equal(1))
				})

				Context("when an error occurs destroying the pipeline", func() {
					BeforeEach(func() {
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						err := errors.New("disaster!")
						dbPipeline.DestroyReturns(err)
					})

					It("returns a 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("injects the proper pipelineDB", func() {
					pipelineRef := fakeTeam.PipelineArgsForCall(0)
					Expect(pipelineRef).To(Equal(atc.PipelineRef{Name: "a-pipeline"}))
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
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						dbPipeline.PauseReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
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
			requestTeam      = "a-team"
		)

		BeforeEach(func() {
			requestTeam = "a-team"
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
			fakeTeam.PipelineReturns(dbPipeline, true, nil)
		})

		JustBeforeEach(func() {
			request, _ := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+requestTeam+"/pipelines/a-pipeline/archive", nil)
			var err error
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns 200", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		Context("when archiving succeeds", func() {
			BeforeEach(func() {
				archiveDB = useRealDB()
				archivedPipeline = archiveDB.SavePipeline(archiveDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
				server = archiveDB.Serve()
				requestTeam = "main"
			})

			It("archives the pipeline in PostgreSQL", func() {
				found, err := archivedPipeline.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(archivedPipeline.Archived()).To(BeTrue())
				Expect(archivedPipeline.Paused()).To(BeTrue())
			})
		})

		Context("when archiving the pipeline fails due to the DB", func() {
			BeforeEach(func() {
				dbPipeline.ArchiveReturns(errors.New("pq: a db error"))
			})

			It("gives a server error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})

		Context("when the pipeline is an immutable workflow-run template", func() {
			BeforeEach(func() {
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

					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					fakeTeam.PipelineReturns(dbPipeline, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("injects the proper pipelineDB", func() {
					pipelineRef := fakeTeam.PipelineArgsForCall(0)
					Expect(pipelineRef).To(Equal(atc.PipelineRef{Name: "a-pipeline"}))
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
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						dbPipeline.UnpauseReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("injects the proper pipelineDB", func() {
					Expect(fakeTeam.PipelineCallCount()).To(Equal(1))
					pipelineRef := fakeTeam.PipelineArgsForCall(0)
					Expect(pipelineRef).To(Equal(atc.PipelineRef{Name: "a-pipeline"}))
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
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						dbPipeline.ExposeReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				It("injects the proper pipeline", func() {
					pipelineRef := fakeTeam.PipelineArgsForCall(0)
					Expect(pipelineRef).To(Equal(atc.PipelineRef{Name: "a-pipeline"}))
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
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
						dbPipeline.HideReturns(errors.New("welp"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						fakeTeam.PipelineReturns(dbPipeline, true, nil)
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						orderingDB = useRealDB()
						for _, name := range pipelineNames {
							orderingDB.SavePipeline(orderingDB.Main, name, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						}
						server = orderingDB.Serve()
						requestTeam = "main"
					})

					It("orders the pipelines", func() {
						pipelines, err := orderingDB.Main.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualNames := make([]string, len(pipelines))
						for i, pipeline := range pipelines {
							actualNames[i] = pipeline.Name()
						}
						Expect(actualNames).To(Equal(pipelineNames))
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						fakeTeam.OrderPipelinesReturns(db.ErrPipelineNotFound{Name: "a-pipeline"})
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline' not found"))
					})
				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
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
			response     *http.Response
			instanceVars []atc.InstanceVars
			withinDB     *realDB
			requestTeam  = "a-team"
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				})

				It("constructs team with provided team name", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				Context("when ordering the pipelines succeeds", func() {
					BeforeEach(func() {
						withinDB = useRealDB()
						for _, vars := range []atc.InstanceVars{{"branch": "test-2"}, nil, {"branch": "test"}} {
							_, _, err := withinDB.Main.SavePipeline(atc.PipelineRef{Name: "a-pipeline", InstanceVars: vars}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, db.ConfigVersion(0), false)
							Expect(err).NotTo(HaveOccurred())
						}
						server = withinDB.Serve()
						requestTeam = "main"
					})

					It("orders the pipelines", func() {
						pipelines, err := withinDB.Main.Pipelines()
						Expect(err).NotTo(HaveOccurred())
						actualInstanceVars := make([]atc.InstanceVars, len(pipelines))
						for i, pipeline := range pipelines {
							actualInstanceVars[i] = pipeline.InstanceVars()
						}
						Expect(actualInstanceVars).To(Equal([]atc.InstanceVars{{"branch": "test"}, nil, {"branch": "test-2"}}))
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})
				})

				Context("when a pipeline does not exist", func() {
					BeforeEach(func() {
						fakeTeam.OrderPipelinesWithinGroupReturns(db.ErrPipelineNotFound{Name: "a-pipeline"})
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						Expect(io.ReadAll(response.Body)).To(ContainSubstring("pipeline 'a-pipeline' not found"))
					})
				})

				Context("when ordering the pipelines fails", func() {
					BeforeEach(func() {
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

					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					fakeTeam.RenamePipelineReturns(true, nil)
				})

				It("finds the correct team", func() {
					Expect(dbTeamFactory.FindTeamCallCount()).To(Equal(1))
					Expect(dbTeamFactory.FindTeamArgsForCall(0)).To(Equal("a-team"))
				})

				Context("when renaming succeeds", func() {
					BeforeEach(func() {
						renameDB = useRealDB()
						renameDB.SavePipeline(renameDB.Main, "a-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}})
						server = renameDB.Serve()
						requestTeam = "main"
					})

					It("renames the pipeline in PostgreSQL", func() {
						_, found, err := renameDB.Main.Pipeline(atc.PipelineRef{Name: "a-pipeline"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeFalse())
						renamed, found, err := renameDB.Main.Pipeline(atc.PipelineRef{Name: "some-new-name"})
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(renamed.Name()).To(Equal("some-new-name"))
					})
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				Context("when the pipeline does not exist", func() {
					BeforeEach(func() {
						fakeTeam.RenamePipelineReturns(false, nil)
					})

					It("returns a 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

				Context("when renaming the pipeline errors", func() {
					BeforeEach(func() {
						fakeTeam.RenamePipelineReturns(false, errors.New("whoops"))
					})

					It("returns a 500 internal server error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is an immutable workflow-run template", func() {
					BeforeEach(func() {
						fakeTeam.RenamePipelineReturns(false, db.ErrWorkflowRunTemplateImmutable)
					})

					It("returns 409 Conflict", func() {
						Expect(response.StatusCode).To(Equal(http.StatusConflict))
					})
				})

				Context("when the new name is an invalid identifier", func() {
					Context("and is a string", func() {
						BeforeEach(func() {
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

		BeforeEach(func() {
			requestTeam = "some-team"
			requestPipe = "some-pipeline"
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

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when no params are passed", func() {
				It("does not set defaults for since and until", func() {
					Expect(fakePipeline.BuildsCallCount()).To(Equal(1))

					page := fakePipeline.BuildsArgsForCall(0)
					Expect(page).To(Equal(db.Page{
						Limit: 100,
					}))
				})
			})

			Context("when all the params are passed", func() {
				BeforeEach(func() {
					queryParams = "?from=2&to=3&limit=8"
				})

				It("passes them through", func() {
					Expect(fakePipeline.BuildsCallCount()).To(Equal(1))

					page := fakePipeline.BuildsArgsForCall(0)
					Expect(page).To(Equal(db.Page{
						From:  db.NewIntPtr(2),
						To:    db.NewIntPtr(3),
						Limit: 8,
					}))
				})
			})

			Context("when getting the builds succeeds", func() {
				BeforeEach(func() {
					listDB = useRealDB()
					listPipeline = listDB.SavePipeline(listDB.Main, "some-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}})
					job, found, err := listPipeline.Job("some-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					listBuild1, err = job.CreateBuild("api-test")
					Expect(err).NotTo(HaveOccurred())
					started, err := listBuild1.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					Expect(listBuild1.Finish(db.BuildStatusSucceeded)).To(Succeed())
					listBuild2, err = job.CreateBuild("api-test")
					Expect(err).NotTo(HaveOccurred())
					started, err = listBuild2.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					server = listDB.Serve()
					requestTeam = "main"
					requestPipe = "some-pipeline"
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
					BeforeEach(func() {
						queryParams = "?limit=1"
					})

					It("returns Link headers per rfc5988", func() {
						Expect(response.Header["Link"]).To(ConsistOf([]string{
							fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?to=%d&limit=1>; rel="next"`, externalURL, listBuild1.ID()),
						}))
					})

					Context("and pipeline is instanced", func() {
						BeforeEach(func() {
							fakePipeline.InstanceVarsReturns(atc.InstanceVars{"branch": "master"})
						})

						It("returns Link headers per rfc5988", func() {
							Expect(response.Header["Link"]).To(ConsistOf([]string{
								fmt.Sprintf(`<%s/api/v1/teams/main/pipelines/some-pipeline/builds?to=%d&limit=1>; rel="next"`, externalURL, listBuild1.ID()),
							}))
						})
					})
				})
			})

			Context("when getting the build fails", func() {
				BeforeEach(func() {
					fakePipeline.BuildsReturns(nil, db.Pagination{}, errors.New("oh no!"))
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
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not trigger a build", func() {
				Expect(dbPipeline.CreateOneOffBuildCallCount()).To(BeZero())
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
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					fakeTeam.PipelineReturns(dbPipeline, true, nil)
				})

				Context("when creating a started build fails", func() {
					BeforeEach(func() {
						dbPipeline.CreateStartedBuildReturns(nil, errors.New("oh no!"))
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the pipeline is owned by a durable workflow run", func() {
					BeforeEach(func() {
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
						Expect(builds[0].Status()).To(Equal(db.BuildStatusStarted))
					})

					It("returns the created build", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						var actual atc.Build
						Expect(json.Unmarshal(body, &actual)).To(Succeed())
						builds, _, err := postPipeline.Builds(db.Page{Limit: 1})
						Expect(err).NotTo(HaveOccurred())
						Expect(builds).To(HaveLen(1))
						build := builds[0]
						Expect(actual.ID).To(Equal(build.ID()))
						Expect(actual.Name).To(Equal(build.Name()))
						Expect(actual.TeamName).To(Equal(build.TeamName()))
						Expect(actual.PipelineName).To(Equal("a-pipeline"))
						Expect(actual.Status).To(Equal(atc.BuildStatus(db.BuildStatusStarted)))
						Expect(actual.APIURL).To(Equal(fmt.Sprintf("/api/v1/builds/%d", build.ID())))
						Expect(actual.StartTime).To(Equal(build.StartTime().Unix()))
						found, err := postPipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect([]byte(*build.PublicPlan())).To(MatchJSON([]byte(*plan.Public())))
					})
				})
			})
		})
	})
})
