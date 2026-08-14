package api_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/testhelpers"
	"github.com/tedsuo/rata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type ccAPIFixture struct {
	team     db.Team
	pipeline db.Pipeline
	job      db.Job
}

func createCCAPIJob(
	database *realDB,
	teamName string,
	pipelineRef atc.PipelineRef,
	jobName string,
) ccAPIFixture {
	GinkgoHelper()

	team, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(
		pipelineRef,
		atc.Config{Jobs: atc.JobConfigs{{Name: jobName}}},
		db.ConfigVersion(0),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	job, found, err := pipeline.Job(jobName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	return ccAPIFixture{team: team, pipeline: pipeline, job: job}
}

func finishCCAPIBuild(
	database *realDB,
	job db.Job,
	status db.BuildStatus,
	endTime time.Time,
) db.Build {
	GinkgoHelper()

	build, err := job.CreateBuild("cc-api-test")
	Expect(err).NotTo(HaveOccurred())
	started, err := build.Start(atc.Plan{})
	Expect(err).NotTo(HaveOccurred())
	Expect(started).To(BeTrue())
	Expect(build.Finish(status)).To(Succeed())
	_, err = database.Conn.Exec(
		"UPDATE builds SET end_time = $1 WHERE id = $2",
		endTime.UTC(),
		build.ID(),
	)
	Expect(err).NotTo(HaveOccurred())
	found, err := build.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(build.Status()).To(Equal(status))
	Expect(build.EndTime()).To(BeTemporally("==", endTime.UTC()))
	return build
}

func expectedCCProjectXML(
	fixture ccAPIFixture,
	build db.Build,
	activity string,
	lastBuildStatus string,
) string {
	GinkgoHelper()

	return fmt.Sprintf(`
<Projects>
  <Project activity="%s" lastBuildLabel="%s" lastBuildStatus="%s" lastBuildTime="%s" name="%s/%s" webUrl="https://example.com/teams/%s/pipelines/%s/jobs/%s"/>
</Projects>
`,
		activity,
		build.Name(),
		lastBuildStatus,
		build.EndTime().UTC().Format(time.RFC3339),
		fixture.pipeline.Name(),
		fixture.job.Name(),
		fixture.team.Name(),
		fixture.pipeline.Name(),
		fixture.job.Name(),
	)
}

var _ = Describe("cc.xml", func() {
	var (
		database         *realDB
		deps             apiDBDeps
		requestGenerator *rata.RequestGenerator
		response         *http.Response
		server           *httptest.Server
		teamName         string
		grantViewer      bool
	)

	BeforeEach(func() {
		database = useRealDB()
		deps = database.Deps
		teamName = "a-team"
		grantViewer = false
	})

	Describe("GET /api/v1/teams/:team_name/cc.xml", func() {
		JustBeforeEach(func() {
			if grantViewer {
				team, found, err := database.Deps.teamFactory.FindTeam(teamName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				grantProfile(team, memberProfile, accessor.ViewerRole)
			}

			server = newAPIServer(deps)
			DeferCleanup(server.Close)
			requestGenerator = rata.NewRequestGenerator(server.URL, atc.Routes)

			req, err := requestGenerator.CreateRequest(atc.GetCC, rata.Params{
				"team_name": teamName,
			}, nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			if response != nil {
				DeferCleanup(func() {
					Expect(response.Body.Close()).To(Succeed())
				})
			}
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				grantViewer = true
				useProfile(memberProfile)
			})

			Context("when the team is found", func() {
				Context("when a pipeline is found", func() {
					Context("when a job is found", func() {
						var (
							endTime       time.Time
							fixture       ccAPIFixture
							finishedBuild db.Build
						)

						BeforeEach(func() {
							endTime = time.Date(2018, time.November, 4, 21, 26, 38, 0, time.UTC)
							fixture = createCCAPIJob(
								database,
								"a-team",
								atc.PipelineRef{Name: "something-else"},
								"some-job",
							)
							teamName = fixture.team.Name()
						})

						Context("when the last build is successful", func() {
							BeforeEach(func() {
								finishedBuild = finishCCAPIBuild(
									database,
									fixture.job,
									db.BuildStatusSucceeded,
									endTime,
								)
							})

							It("returns 200", func() {
								Expect(response.StatusCode).To(Equal(http.StatusOK))
							})

							It("returns Content-Type 'application/xml'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/xml",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("returns the CC.xml", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML(expectedCCProjectXML(
									fixture,
									finishedBuild,
									"Sleeping",
									"Success",
								)))
							})
						})

						Context("when the last build is aborted", func() {
							BeforeEach(func() {
								finishedBuild = finishCCAPIBuild(
									database,
									fixture.job,
									db.BuildStatusAborted,
									endTime,
								)
							})

							It("returns the CC.xml", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML(expectedCCProjectXML(
									fixture,
									finishedBuild,
									"Sleeping",
									"Exception",
								)))
							})
						})

						Context("when the last build is errored", func() {
							BeforeEach(func() {
								finishedBuild = finishCCAPIBuild(
									database,
									fixture.job,
									db.BuildStatusErrored,
									endTime,
								)
							})

							It("returns the CC.xml", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML(expectedCCProjectXML(
									fixture,
									finishedBuild,
									"Sleeping",
									"Exception",
								)))
							})
						})

						Context("when the last build is failed", func() {
							BeforeEach(func() {
								finishedBuild = finishCCAPIBuild(
									database,
									fixture.job,
									db.BuildStatusFailed,
									endTime,
								)
							})

							It("returns the CC.xml", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML(expectedCCProjectXML(
									fixture,
									finishedBuild,
									"Sleeping",
									"Failure",
								)))
							})
						})

						Context("when a next build exists", func() {
							BeforeEach(func() {
								finishedBuild = finishCCAPIBuild(
									database,
									fixture.job,
									db.BuildStatusSucceeded,
									endTime,
								)
								nextBuild, err := fixture.job.CreateBuild("cc-api-test")
								Expect(err).NotTo(HaveOccurred())
								started, err := nextBuild.Start(atc.Plan{})
								Expect(err).NotTo(HaveOccurred())
								Expect(started).To(BeTrue())
								found, err := nextBuild.Reload()
								Expect(err).NotTo(HaveOccurred())
								Expect(found).To(BeTrue())
							})

							It("returns the CC.xml", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML(expectedCCProjectXML(
									fixture,
									finishedBuild,
									"Building",
									"Success",
								)))
							})
						})

						Context("when no last build exists", func() {
							It("returns the CC.xml without the job", func() {
								body, err := io.ReadAll(response.Body)
								Expect(err).NotTo(HaveOccurred())

								Expect(body).To(MatchXML("<Projects></Projects>"))
							})
						})
					})

					Context("when no job is found", func() {
						BeforeEach(func() {
							team, err := deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
							Expect(err).NotTo(HaveOccurred())
							database.SavePipeline(team, "something-else", atc.Config{})
							teamName = team.Name()
						})

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("returns the CC.xml", func() {
							body, err := io.ReadAll(response.Body)
							Expect(err).NotTo(HaveOccurred())

							Expect(body).To(MatchXML("<Projects></Projects>"))
						})
					})

				})

				Context("when an instanced pipeline is found", func() {
					var (
						endTime       time.Time
						fixture       ccAPIFixture
						finishedBuild db.Build
					)
					instanceVars := atc.InstanceVars{"branch": "feature/foo"}

					BeforeEach(func() {
						endTime = time.Date(2018, time.November, 4, 21, 26, 38, 0, time.UTC)
						fixture = createCCAPIJob(
							database,
							"a-team",
							atc.PipelineRef{Name: "something-else", InstanceVars: instanceVars},
							"some-job",
						)
						teamName = fixture.team.Name()
						finishedBuild = finishCCAPIBuild(
							database,
							fixture.job,
							db.BuildStatusSucceeded,
							endTime,
						)
					})

					It("returns the proper web url in the CC.xml", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						persistedInstanceVars := fixture.pipeline.InstanceVars()
						Expect(persistedInstanceVars).To(Equal(instanceVars))
						pipelineRef := atc.PipelineRef{
							Name:         fixture.pipeline.Name(),
							InstanceVars: persistedInstanceVars,
						}
						Expect(body).To(MatchXML(fmt.Sprintf(`
<Projects>
  <Project activity="Sleeping" lastBuildLabel="%s" lastBuildStatus="Success" lastBuildTime="%s" name="%s/branch:&#34;%s&#34;/%s" webUrl="https://example.com/teams/%s/pipelines/%s/jobs/%s?%s"/>
</Projects>
`,
							finishedBuild.Name(),
							finishedBuild.EndTime().UTC().Format(time.RFC3339),
							fixture.pipeline.Name(),
							persistedInstanceVars["branch"],
							fixture.job.Name(),
							fixture.team.Name(),
							fixture.pipeline.Name(),
							fixture.job.Name(),
							pipelineRef.QueryParams().Encode(),
						)))
					})
				})

				Context("when no pipeline is found", func() {
					BeforeEach(func() {
						team, err := deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						teamName = team.Name()
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns the CC.xml", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchXML("<Projects></Projects>"))
					})
				})

			})

			Context("when the team is not found", func() {
				BeforeEach(func() {
					grantViewer = false
					useProfile(adminProfile)
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			Context("when there are public pipelines", func() {
				var (
					endTime        time.Time
					exposedFixture ccAPIFixture
					exposedBuild   db.Build
					hiddenPipeline db.Pipeline
					hiddenJob      db.Job
				)

				BeforeEach(func() {
					endTime = time.Date(2018, time.November, 4, 21, 26, 38, 0, time.UTC)
					hiddenFixture := createCCAPIJob(
						database,
						"a-public-team",
						atc.PipelineRef{Name: "hidden-pipeline"},
						"hidden-job",
					)
					hiddenPipeline = hiddenFixture.pipeline
					hiddenJob = hiddenFixture.job
					finishCCAPIBuild(
						database,
						hiddenJob,
						db.BuildStatusSucceeded,
						endTime,
					)

					exposedPipeline, _, err := hiddenFixture.team.SavePipeline(
						atc.PipelineRef{Name: "exposed-pipeline"},
						atc.Config{Jobs: atc.JobConfigs{{Name: "a-public-job"}}},
						db.ConfigVersion(0),
						false,
					)
					Expect(err).NotTo(HaveOccurred())
					exposedJob, found, err := exposedPipeline.Job("a-public-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(exposedPipeline.Expose()).To(Succeed())
					found, err = exposedPipeline.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(exposedPipeline.Public()).To(BeTrue())
					exposedFixture = ccAPIFixture{
						team:     hiddenFixture.team,
						pipeline: exposedPipeline,
						job:      exposedJob,
					}
					exposedBuild = finishCCAPIBuild(
						database,
						exposedJob,
						db.BuildStatusSucceeded,
						endTime,
					)
					teamName = hiddenFixture.team.Name()
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("lists public pipelines", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchXML(expectedCCProjectXML(
						exposedFixture,
						exposedBuild,
						"Sleeping",
						"Success",
					)))
					hiddenProjectName := fmt.Sprintf(
						"%s/%s",
						hiddenPipeline.Name(),
						hiddenJob.Name(),
					)
					Expect(string(body)).NotTo(ContainSubstring(hiddenProjectName))
				})
			})

			Context("when there are no public pipelines", func() {
				BeforeEach(func() {
					endTime := time.Date(2018, time.November, 4, 21, 26, 38, 0, time.UTC)
					privateFixture := createCCAPIJob(
						database,
						"a-private-team",
						atc.PipelineRef{Name: "private-pipeline"},
						"private-job",
					)
					finishCCAPIBuild(
						database,
						privateFixture.job,
						db.BuildStatusSucceeded,
						endTime,
					)
					teamName = privateFixture.team.Name()
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns an empty project list", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchXML(`<Projects></Projects>`))
				})
			})
		})

		Context("when the team is not found", func() {
			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})
})
