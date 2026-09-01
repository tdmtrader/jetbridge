package api_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type ccAPIFixture struct {
	team     db.Team
	pipeline db.Pipeline
	job      db.Job
}

// ccAPITeamFactoryResult preserves a successful FindTeam while routing one
// supplied real team whose later database call can fail. A healthy PostgreSQL
// connection cannot selectively fail Team.Pipelines after that lookup.
type ccAPITeamFactoryResult struct {
	db.TeamFactory
	teamName string
	team     db.Team
}

func (factory ccAPITeamFactoryResult) FindTeam(name string) (db.Team, bool, error) {
	if name == factory.teamName {
		return factory.team, true, nil
	}
	return factory.TeamFactory.FindTeam(name)
}

// ccAPITeamPipelinesResult preserves a healthy real team while routing one
// supplied real pipeline whose Dashboard call can fail. Healthy PostgreSQL
// cannot selectively fail Pipeline.Dashboard after Team.Pipelines succeeds.
type ccAPITeamPipelinesResult struct {
	db.Team
	pipelines []db.Pipeline
}

func (team ccAPITeamPipelinesResult) Pipelines() ([]db.Pipeline, error) {
	return team.pipelines, nil
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

func closedCCAPITeam(database *realDB, teamName string) db.Team {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	defer func() {
		Expect(conn.Close()).To(Succeed())
	}()
	teamFactory := db.NewTeamFactory(conn, database.LockFactory)
	team, found, err := teamFactory.FindTeam(teamName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return team
}

func closedCCAPIPipeline(database *realDB, fixture ccAPIFixture) db.Pipeline {
	GinkgoHelper()

	conn := postgresRunner.OpenConn()
	defer func() {
		Expect(conn.Close()).To(Succeed())
	}()
	teamFactory := db.NewTeamFactory(conn, database.LockFactory)
	team, found, err := teamFactory.FindTeam(fixture.team.Name())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{
		Name:         fixture.pipeline.Name(),
		InstanceVars: fixture.pipeline.InstanceVars(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return pipeline
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
	)

	BeforeEach(func() {
		database = useRealDB()
		deps = database.Deps
		teamName = "a-team"
	})

	Describe("GET /api/v1/teams/:team_name/cc.xml", func() {
		JustBeforeEach(func() {
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
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when the team is found", func() {
				Context("when a pipeline is found", func() {
					Context("when finding the jobs fails", func() {
						BeforeEach(func() {
							fixture := createCCAPIJob(
								database,
								"a-team",
								atc.PipelineRef{Name: "something-else"},
								"some-job",
							)
							teamName = fixture.team.Name()
							closedPipeline := closedCCAPIPipeline(database, fixture)
							deps.teamFactory = ccAPITeamFactoryResult{
								TeamFactory: deps.teamFactory,
								teamName:    fixture.team.Name(),
								team: ccAPITeamPipelinesResult{
									Team:      fixture.team,
									pipelines: []db.Pipeline{closedPipeline},
								},
							}
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})
				})

				Context("when getting the pipelines fails", func() {
					BeforeEach(func() {
						team, err := deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
						Expect(err).NotTo(HaveOccurred())
						teamName = team.Name()
						deps.teamFactory = ccAPITeamFactoryResult{
							TeamFactory: deps.teamFactory,
							teamName:    team.Name(),
							team:        closedCCAPITeam(database, team.Name()),
						}
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when finding the team fails", func() {
				BeforeEach(func() {
					doomed := postgresRunner.OpenConn()
					deps.teamFactory = db.NewTeamFactory(doomed, database.LockFactory)
					Expect(doomed.Close()).To(Succeed())
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
