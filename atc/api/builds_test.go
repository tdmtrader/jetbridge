package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/event"
	. "github.com/concourse/concourse/atc/testhelpers"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vito/go-sse/sse"
)

func buildsAPIRequireTeamBuilds(team db.Team) []db.BuildForAPI {
	GinkgoHelper()

	builds, _, err := team.Builds(db.Page{Limit: 100})
	Expect(err).NotTo(HaveOccurred())
	return builds
}

func buildsAPIRemoveJob(team db.Team, pipeline db.Pipeline, jobName string) db.Pipeline {
	GinkgoHelper()

	config, err := pipeline.Config()
	Expect(err).NotTo(HaveOccurred())
	remainingJobs := make(atc.JobConfigs, 0, len(config.Jobs))
	for _, job := range config.Jobs {
		if job.Name != jobName {
			remainingJobs = append(remainingJobs, job)
		}
	}
	config.Jobs = remainingJobs

	updated, created, err := team.SavePipeline(
		atc.PipelineRef{Name: pipeline.Name(), InstanceVars: pipeline.InstanceVars()},
		config,
		pipeline.ConfigVersion(),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeFalse())
	_, found, err := updated.Job(jobName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeFalse())
	return updated
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

var _ = Describe("Builds API", func() {

	Describe("POST /api/v1/builds", func() {
		var (
			database   *realDB
			deps       apiDBDeps
			team       db.Team
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
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				Expect(buildsAPIRequireTeamBuilds(team)).To(HaveLen(buildCount))
			})
		})

		Context("when authenticated", func() {
			Context("when not authorized", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					Expect(buildsAPIRequireTeamBuilds(team)).To(HaveLen(buildCount))
				})
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					grantProfile(team, memberProfile, accessor.MemberRole)
					useProfile(memberProfile)
				})

				Context("when creating a started build succeeds", func() {
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
						var actual atc.Build
						Expect(json.NewDecoder(response.Body).Decode(&actual)).To(Succeed())

						persisted, found, err := database.Deps.buildFactory.Build(actual.ID)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(persisted.ID()).To(Equal(actual.ID))
						Expect(persisted.TeamID()).To(Equal(team.ID()))
						Expect(persisted.TeamName()).To(Equal(team.Name()))
						Expect(persisted.Status()).To(Equal(db.BuildStatusStarted))
						Expect(persisted.IsRunning()).To(BeTrue())
						Expect(persisted.StartTime()).NotTo(BeZero())
						Expect(persisted.Schema()).To(Equal("exec.v2"))
						Expect(persisted.PrivatePlan()).To(Equal(plan))
						Expect(persisted.PublicPlan()).To(Equal(plan.Public()))
						Expect(buildsAPIRequireTeamBuilds(team)).To(HaveLen(buildCount + 1))
					})

					It("returns the created build", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						var actual atc.Build
						Expect(json.Unmarshal(body, &actual)).To(Succeed())

						persisted, found, err := deps.buildFactory.BuildForAPI(actual.ID)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						expected, err := json.Marshal(atc.Build{
							ID:        persisted.ID(),
							Name:      persisted.Name(),
							TeamName:  persisted.TeamName(),
							Status:    atc.StatusStarted,
							APIURL:    fmt.Sprintf("/api/v1/builds/%d", persisted.ID()),
							StartTime: persisted.StartTime().Unix(),
						})
						Expect(err).NotTo(HaveOccurred())
						Expect(body).To(MatchJSON(expected))
					})

				})
			})
		})
	})

	Describe("GET /api/v1/builds", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory

			publicBuilds          []db.BuildForAPI
			sameTeamPrivateBuild  db.BuildForAPI
			crossTeamPrivateBuild db.BuildForAPI
			someTeam              db.Team
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

			var err error
			someTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
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

			queryParams = ""
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
				useProfile(anonymousProfile)
			})

			Context("when all the params are passed", func() {
				BeforeEach(func() {
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=8",
						publicBuilds[1].ID(),
						publicBuilds[2].ID(),
					)
				})

				It("returns the requested build range", func() {
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

					It("returns the requested timestamp range", func() {
						buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[2], publicBuilds[1]})
						Expect(response.Header.Values("Link")).To(BeEmpty())
					})
				})
			})

			Context("when getting the builds succeeds", func() {
				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

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

		})

		Context("when authenticated", func() {
			Context("when user has the admin privilege", func() {
				BeforeEach(func() {
					useProfile(adminProfile)
				})

				It("returns private builds across all teams", func() {
					builds := buildsAPIExpectBuildsResponse(response, adminBuilds)
					Expect(builds[0].ID).To(Equal(crossTeamPrivateBuild.ID()))
				})

			})

			Context("when all the params are passed", func() {
				BeforeEach(func() {
					grantProfile(someTeam, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
					queryParams = fmt.Sprintf(
						"?from=%d&to=%d&limit=8",
						publicBuilds[1].ID(),
						publicBuilds[2].ID(),
					)
				})

				It("returns the requested build range", func() {
					buildsAPIExpectBuildsResponse(response, []db.BuildForAPI{publicBuilds[1], publicBuilds[2]})
				})
			})

			Context("when getting the builds succeeds", func() {
				BeforeEach(func() {
					grantProfile(someTeam, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
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

				It("returns all builds", func() {
					buildsAPIExpectBuildsResponse(response, authenticatedBuilds)
				})

				It("returns builds for teams from the token", func() {
					builds := buildsAPIExpectBuildsResponse(response, authenticatedBuilds)
					Expect(builds[0].ID).To(Equal(sameTeamPrivateBuild.ID()))
				})
			})

			Context("when next/previous pages are available", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
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

		})
	})

	Describe("GET /api/v1/builds/:build_id", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory

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
						useProfile(anonymousProfile)
					})

					Context("and build is one off", func() {
						BeforeEach(func() {
							oneOffBuild, err := team.CreateOneOffBuild()
							Expect(err).NotTo(HaveOccurred())
							persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuild.ID())
							Expect(persistedBuild.PipelineID()).To(BeZero())
							requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
						})

						It("returns 401", func() {
							Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
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

						It("returns 401", func() {
							Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
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
						Expect(pipeline.Hide()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeFalse())
					})

					Context("when user is not authorized", func() {
						BeforeEach(func() {
							useProfile(memberProfile)
						})

						It("returns 403", func() {
							Expect(response.StatusCode).To(Equal(http.StatusForbidden))
						})
					})

					Context("when user is authorized", func() {
						BeforeEach(func() {
							grantProfile(team, memberProfile, accessor.ViewerRole)
							useProfile(memberProfile)
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

						It("returns the build with the given build_id", func() {
							actual := buildsAPIExpectBuildResponse(response, persistedBuild)
							Expect(actual.ID).To(Equal(persistedBuild.ID()))
							Expect(actual.PipelineID).To(Equal(pipeline.ID()))
							Expect(actual.StartTime).To(Equal(persistedBuild.StartTime().Unix()))
							Expect(actual.EndTime).To(Equal(persistedBuild.EndTime().Unix()))
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
					useProfile(anonymousProfile)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						oneOffBuild, err := team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())
						persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, oneOffBuild.ID())
						Expect(persistedBuild.PipelineID()).To(BeZero())
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("and the pipeline is private", func() {
					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
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
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when authenticated and authorized", func() {
				BeforeEach(func() {
					grantProfile(team, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
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

				Context("with an invalid build", func() {
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

			team               db.Team
			pipeline           db.Pipeline
			privateJob         db.Job
			publicJob          db.Job
			persistedBuild     db.BuildForAPI
			publicBuild        db.BuildForAPI
			missingBuildID     int
			requestBuildID     string
			targetEventPayload string
			decoyEventPayload  string
			cancelEventRequest context.CancelFunc
			eventRequest       *http.Request

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
			targetEventPayload = "target-build-log-payload"
			decoyEventPayload = "decoy-build-log-payload"
			Expect(publicBuildRow.SaveEvent(event.Log{Payload: decoyEventPayload})).To(Succeed())
			Expect(privateBuildRow.SaveEvent(event.Log{Payload: targetEventPayload})).To(Succeed())
			persistedBuild = buildsAPIRequireBuildForAPI(realBuildFactory, privateBuildRow.ID())
			publicBuild = buildsAPIRequireBuildForAPI(realBuildFactory, publicBuildRow.ID())
			Expect(persistedBuild.ID()).NotTo(Equal(publicBuild.ID()))
			Expect(persistedBuild.PipelineID()).To(Equal(pipeline.ID()))
			Expect(publicBuild.PipelineID()).To(Equal(pipeline.ID()))

			missingBuildID = publicBuild.ID() + 1_000_000
			_, found, err = realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

			requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
		})

		JustBeforeEach(func() {
			database.Deps = deps
			server = database.Serve()

			var err error
			requestContext, cancel := context.WithCancel(context.Background())
			cancelEventRequest = cancel
			eventRequest, err = http.NewRequestWithContext(
				requestContext,
				http.MethodGet,
				server.URL+"/api/v1/builds/"+requestBuildID+"/events",
				nil,
			)
			Expect(err).NotTo(HaveOccurred())
			eventRequest.Header.Set("Last-Event-ID", "1")

			response, err = client.Do(eventRequest)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				cancelEventRequest()
				_ = response.Body.Close()
			})
		})

		Context("when the build can be found", func() {
			Context("when authenticated, but not authorized", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when authorized", func() {
				BeforeEach(func() {
					grantProfile(team, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(200))
				})

				It("streams the target build event at its per-build ordinal", func() {
					Expect(eventRequest.URL.Path).To(Equal(fmt.Sprintf("/api/v1/builds/%d/events", persistedBuild.ID())))
					Expect(response.Header.Get("Content-Type")).To(Equal("text/event-stream; charset=utf-8"))

					stream := sse.NewReadCloser(response.Body)
					outer, err := stream.Next()
					Expect(err).NotTo(HaveOccurred())
					Expect(outer.Name).To(Equal("event"))
					Expect(outer.ID).To(Equal("2"))

					var envelope event.Envelope
					Expect(json.Unmarshal(outer.Data, &envelope)).To(Succeed())
					Expect(envelope.Event).To(Equal(event.EventTypeLog))
					Expect(envelope.EventID).To(Equal("2"))
					Expect(envelope.Data).NotTo(BeNil())

					var logEvent event.Log
					Expect(json.Unmarshal(*envelope.Data, &logEvent)).To(Succeed())
					Expect(logEvent.Payload).To(Equal(targetEventPayload))
					Expect(logEvent.Payload).NotTo(Equal(decoyEventPayload))

					cancelEventRequest()
					Expect(response.Body.Close()).To(Succeed())
				})
			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					useProfile(anonymousProfile)
				})

				Context("and the pipeline is private", func() {
					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
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
							It("returns 401", func() {
								Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
							})
						})

						Context("and the job is public", func() {
							BeforeEach(func() {
								persistedBuild = publicBuild
								requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
							})

							It("returns 200", func() {
								Expect(response.StatusCode).To(Equal(200))
							})

						})
					})

					Context("when the job cannot be found", func() {
						BeforeEach(func() {
							pipeline = buildsAPIRemoveJob(team, pipeline, privateJob.Name())
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

			})
		})
	})

	Describe("shared-scope in-memory check build access", func() {
		It("lets an associated team read a finished check and its events while denying an unrelated team", func() {
			database := useRealDB()
			deps := database.Deps

			ownerTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "check-owner-team"})
			Expect(err).NotTo(HaveOccurred())
			peerTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "check-peer-team"})
			Expect(err).NotTo(HaveOccurred())
			unrelatedTeam, err := deps.teamFactory.CreateTeam(atc.Team{Name: "check-unrelated-team"})
			Expect(err).NotTo(HaveOccurred())

			resourceConfig := atc.ResourceConfig{
				Name:   "shared-resource",
				Type:   dbtest.BaseResourceType,
				Source: atc.Source{"repository": "shared-scope"},
			}
			ownerPipeline := database.SavePipeline(ownerTeam, "check-owner-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{resourceConfig},
			})
			peerPipeline := database.SavePipeline(peerTeam, "check-peer-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{resourceConfig},
			})
			Expect(ownerPipeline.Hide()).To(Succeed())
			Expect(peerPipeline.Hide()).To(Succeed())

			ownerResource, found, err := ownerPipeline.Resource(resourceConfig.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			peerResource, found, err := peerPipeline.Resource(resourceConfig.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			config, err := deps.resourceConfigFactory.FindOrCreateResourceConfig(
				ownerResource.Type(), ownerResource.Source(), nil,
			)
			Expect(err).NotTo(HaveOccurred())
			sharedScope, err := config.FindOrCreateScope(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ownerResource.SetResourceConfigScope(sharedScope)).To(Succeed())
			Expect(peerResource.SetResourceConfigScope(sharedScope)).To(Succeed())
			ownerReloaded, err := ownerResource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(ownerReloaded).To(BeTrue())
			peerReloaded, err := peerResource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(peerReloaded).To(BeTrue())
			Expect(ownerResource.ResourceConfigScopeID()).To(Equal(sharedScope.ID()))
			Expect(peerResource.ResourceConfigScopeID()).To(Equal(sharedScope.ID()))

			plan := atc.Plan{
				ID: "shared-scope-check",
				Check: &atc.CheckPlan{
					Name:     ownerResource.Name(),
					Resource: ownerResource.Name(),
					Type:     ownerResource.Type(),
					Source:   ownerResource.Source(),
				},
			}
			checkBuild, err := ownerResource.CreateInMemoryBuild(
				context.Background(), plan, util.NewSequenceGenerator(1),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(checkBuild.OnCheckBuildStart()).To(Succeed())
			updated, err := sharedScope.UpdateLastCheckStartTime(checkBuild.ID(), checkBuild.PublicPlan())
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(checkBuild.SaveEvent(event.Log{Payload: "shared-check-log"})).To(Succeed())
			Expect(checkBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			updated, err = sharedScope.UpdateLastCheckEndTime(true)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())

			persistedBuild, found, err := deps.buildFactory.BuildForAPI(checkBuild.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedBuild.TeamName()).To(Or(Equal(ownerTeam.Name()), Equal(peerTeam.Name())))

			associatedTeam := ownerTeam
			if persistedBuild.TeamName() == ownerTeam.Name() {
				associatedTeam = peerTeam
			}
			associatedProfile := persistRequestProfile(
				"shared-check-associated-token",
				"shared-check-associated-subject",
				"shared-check-associated-user",
				"Shared Check Associated User",
				"shared-check-associated",
			)
			grantProfile(associatedTeam, associatedProfile, accessor.ViewerRole)
			unrelatedProfile := persistRequestProfile(
				"shared-check-unrelated-token",
				"shared-check-unrelated-subject",
				"shared-check-unrelated-user",
				"Shared Check Unrelated User",
				"shared-check-unrelated",
			)
			grantProfile(unrelatedTeam, unrelatedProfile, accessor.ViewerRole)

			buildURL := fmt.Sprintf("%s/api/v1/builds/%d", server.URL, checkBuild.ID())
			useProfile(associatedProfile)
			buildResponse, err := client.Get(buildURL)
			Expect(err).NotTo(HaveOccurred())
			Expect(buildResponse.StatusCode).To(Equal(http.StatusOK))
			var presented atc.Build
			Expect(json.NewDecoder(buildResponse.Body).Decode(&presented)).To(Succeed())
			Expect(buildResponse.Body.Close()).To(Succeed())
			Expect(presented.ID).To(Equal(checkBuild.ID()))
			Expect(presented.Name).To(Equal(db.CheckBuildName))
			Expect(presented.ResourceName).To(Equal(resourceConfig.Name))

			eventContext, cancelEvents := context.WithTimeout(context.Background(), 10*time.Second)
			DeferCleanup(cancelEvents)
			eventRequest, err := http.NewRequestWithContext(
				eventContext, http.MethodGet, buildURL+"/events", nil,
			)
			Expect(err).NotTo(HaveOccurred())
			eventResponse, err := client.Do(eventRequest)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(eventResponse.Body.Close)
			Expect(eventResponse.StatusCode).To(Equal(http.StatusOK))
			stream := sse.NewReadCloser(eventResponse.Body)

			nextEnvelope := func(expectedID string, expectedType atc.EventType) event.Envelope {
				GinkgoHelper()
				outer, err := stream.Next()
				Expect(err).NotTo(HaveOccurred())
				Expect(outer.Name).To(Equal("event"))
				Expect(outer.ID).To(Equal(expectedID))
				var envelope event.Envelope
				Expect(json.Unmarshal(outer.Data, &envelope)).To(Succeed())
				Expect(envelope.Event).To(Equal(expectedType))
				Expect(envelope.EventID).To(Equal(expectedID))
				return envelope
			}

			started := nextEnvelope("0", event.EventTypeStatus)
			var startedStatus event.Status
			Expect(json.Unmarshal(*started.Data, &startedStatus)).To(Succeed())
			Expect(startedStatus.Status).To(Equal(atc.StatusStarted))
			logged := nextEnvelope("1", event.EventTypeLog)
			var logEvent event.Log
			Expect(json.Unmarshal(*logged.Data, &logEvent)).To(Succeed())
			Expect(logEvent.Payload).To(Equal("shared-check-log"))
			finished := nextEnvelope("2", event.EventTypeStatus)
			var finishedStatus event.Status
			Expect(json.Unmarshal(*finished.Data, &finishedStatus)).To(Succeed())
			Expect(finishedStatus.Status).To(Equal(atc.StatusSucceeded))
			end, err := stream.Next()
			Expect(err).NotTo(HaveOccurred())
			Expect(end.Name).To(Equal("end"))
			Expect(end.ID).To(Equal("3"))
			cancelEvents()
			Expect(eventResponse.Body.Close()).To(Succeed())

			useProfile(unrelatedProfile)
			deniedBuildResponse, err := client.Get(buildURL)
			Expect(err).NotTo(HaveOccurred())
			Expect(deniedBuildResponse.StatusCode).To(Equal(http.StatusForbidden))
			Expect(deniedBuildResponse.Body.Close()).To(Succeed())
			deniedEventResponse, err := client.Get(buildURL + "/events")
			Expect(err).NotTo(HaveOccurred())
			Expect(deniedEventResponse.StatusCode).To(Equal(http.StatusForbidden))
			Expect(deniedEventResponse.Body.Close()).To(Succeed())
		})
	})

	Describe("build comment and artifact routes", func() {
		It("validates and persists the selected build comment while presenting its artifact state", func() {
			database := useRealDB()
			team, err := database.Deps.teamFactory.CreateTeam(atc.Team{Name: "comment-team"})
			Expect(err).NotTo(HaveOccurred())
			pipeline := database.SavePipeline(team, "comment-pipeline", atc.Config{
				Jobs: atc.JobConfigs{{Name: "comment-job"}},
			})
			job, found, err := pipeline.Job("comment-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			build, err := job.CreateBuild("comment-user")
			Expect(err).NotTo(HaveOccurred())

			decoy, err := job.CreateBuild("comment-decoy-user")
			Expect(err).NotTo(HaveOccurred())
			Expect(decoy.ID()).NotTo(Equal(build.ID()))

			worker, err := database.Deps.workerFactory.SaveWorker(atc.Worker{Name: "comment-artifact-worker"}, 0)
			Expect(err).NotTo(HaveOccurred())
			createArtifact := func(owner db.Build, name, handle string) db.WorkerArtifact {
				GinkgoHelper()
				creating, err := database.Deps.volumeRepository.CreateVolumeWithHandle(
					handle,
					team.ID(),
					worker.Name(),
					db.VolumeTypeArtifact,
				)
				Expect(err).NotTo(HaveOccurred())
				created, err := creating.Created()
				Expect(err).NotTo(HaveOccurred())
				artifact, err := created.InitializeArtifact(name, owner.ID())
				Expect(err).NotTo(HaveOccurred())
				return artifact
			}
			targetArtifact := createArtifact(build, "target-output", "comment-target-artifact")
			decoyArtifact := createArtifact(decoy, "decoy-output", "comment-decoy-artifact")
			Expect(decoyArtifact.ID()).NotTo(Equal(targetArtifact.ID()))

			grantProfile(team, memberProfile, accessor.OperatorRole)
			useProfile(memberProfile)
			commentServer := database.Serve()
			path := fmt.Sprintf("%s/api/v1/builds/%d/comment", commentServer.URL, build.ID())

			putComment := func(body string) *http.Response {
				GinkgoHelper()
				request, err := http.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
				Expect(err).NotTo(HaveOccurred())
				response, err := client.Do(request)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(response.Body.Close)
				return response
			}

			malformed := putComment("{")
			Expect(malformed.StatusCode).To(Equal(http.StatusBadRequest))
			reloaded, found, err := database.Deps.buildFactory.BuildForAPI(build.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(reloaded.Comment()).To(BeEmpty())

			updated := putComment(`{"comment":"investigating flaky input"}`)
			Expect(updated.StatusCode).To(Equal(http.StatusOK))
			Expect(updated.Header.Get("Content-Type")).To(Equal("application/json"))
			reloaded, found, err = database.Deps.buildFactory.BuildForAPI(build.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(reloaded.Comment()).To(Equal("investigating flaky input"))

			decoyReloaded, found, err := database.Deps.buildFactory.BuildForAPI(decoy.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(decoyReloaded.Comment()).To(BeEmpty())

			artifacts, err := client.Get(fmt.Sprintf("%s/api/v1/builds/%d/artifacts", commentServer.URL, build.ID()))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(artifacts.Body.Close)
			Expect(artifacts.StatusCode).To(Equal(http.StatusOK))
			Expect(artifacts.Header.Get("Content-Type")).To(Equal("application/json"))
			var presentedArtifacts []atc.WorkerArtifact
			Expect(json.NewDecoder(artifacts.Body).Decode(&presentedArtifacts)).To(Succeed())
			Expect(presentedArtifacts).To(HaveLen(1))
			Expect(presentedArtifacts[0].ID).To(Equal(targetArtifact.ID()))
			Expect(presentedArtifacts[0].Name).To(Equal(targetArtifact.Name()))
			Expect(presentedArtifacts[0].BuildID).To(Equal(build.ID()))
			Expect(presentedArtifacts[0].CreatedAt).To(Equal(targetArtifact.CreatedAt().Unix()))
		})
	})

	Describe("PUT /api/v1/builds/:build_id/abort", func() {
		var (
			database         *realDB
			deps             apiDBDeps
			realBuildFactory db.BuildFactory

			team               db.Team
			persistedBuild     db.BuildForAPI
			decoyBuild         db.BuildForAPI
			initialStatus      db.BuildStatus
			decoyInitialStatus db.BuildStatus
			missingBuildID     int
			requestBuildID     string

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
			initialStatus = persistedBuild.Status()

			decoyOneOffBuild, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			started, err = decoyOneOffBuild.Start(atc.Plan{
				ID: "abort-decoy-task",
				Task: &atc.TaskPlan{Config: &atc.TaskConfig{
					Run: atc.TaskRunConfig{Path: "abort-decoy-task"},
				}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())
			decoyBuild = buildsAPIRequireBuildForAPI(realBuildFactory, decoyOneOffBuild.ID())
			Expect(decoyBuild.ID()).NotTo(Equal(persistedBuild.ID()))
			Expect(decoyBuild.PipelineID()).To(BeZero())
			Expect(buildsAPIRequireBuild(realBuildFactory, decoyBuild.ID()).IsAborted()).To(BeFalse())
			decoyInitialStatus = decoyBuild.Status()

			missingBuildID = persistedBuild.ID() + 1_000_000
			_, found, err := realBuildFactory.BuildForAPI(missingBuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())

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
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
				Expect(reloaded.IsAborted()).To(BeFalse())
				decoy := buildsAPIRequireBuild(realBuildFactory, decoyBuild.ID())
				Expect(decoy.IsAborted()).To(BeFalse())
				Expect(decoy.Status()).To(Equal(decoyInitialStatus))
			})
		})

		Context("when authenticated", func() {
			Context("when the build can not be found", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
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
						useProfile(memberProfile)
					})

					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
						reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
						Expect(reloaded.IsAborted()).To(BeFalse())
						decoy := buildsAPIRequireBuild(realBuildFactory, decoyBuild.ID())
						Expect(decoy.IsAborted()).To(BeFalse())
						Expect(decoy.Status()).To(Equal(decoyInitialStatus))
					})
				})

				Context("when authorized", func() {
					BeforeEach(func() {
						grantProfile(team, memberProfile, accessor.OperatorRole)
						useProfile(memberProfile)
					})

					Context("when aborting succeeds", func() {
						It("returns 204", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNoContent))
							reloaded := buildsAPIRequireBuild(realBuildFactory, persistedBuild.ID())
							Expect(reloaded.IsAborted()).To(BeTrue())
							Expect(reloaded.Status()).To(Equal(initialStatus))
							decoy := buildsAPIRequireBuild(realBuildFactory, decoyBuild.ID())
							Expect(decoy.IsAborted()).To(BeFalse())
							Expect(decoy.Status()).To(Equal(decoyInitialStatus))
						})
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
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					useProfile(anonymousProfile)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						persistedBuild = oneOffBuild
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("and the pipeline is private", func() {
					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
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
							It("returns 401", func() {
								Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
							})
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

					Context("when the job cannot be found", func() {
						BeforeEach(func() {
							pipeline = buildsAPIRemoveJob(team, pipeline, preparationJob.Name())
						})

						It("returns Not Found", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})
				})
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					grantProfile(team, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
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
						pipeline = buildsAPIRemoveJob(team, pipeline, preparationJob.Name())
					})

					It("returns Not Found", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})

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
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})

			Context("when not authenticated", func() {
				BeforeEach(func() {
					useProfile(anonymousProfile)
				})

				Context("and build is one off", func() {
					BeforeEach(func() {
						persistedBuild = oneOffBuild
						requestBuildID = fmt.Sprintf("%d", persistedBuild.ID())
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("and the pipeline is private", func() {
					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("and the pipeline is public", func() {
					BeforeEach(func() {
						Expect(pipeline.Expose()).To(Succeed())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.Public()).To(BeTrue())
					})

					Context("when the job does not exist", func() {
						BeforeEach(func() {
							pipeline = buildsAPIRemoveJob(team, pipeline, privateJob.Name())
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
							It("returns 401", func() {
								Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
							})
						})
					})
				})
			})

			Context("when authenticated", func() {
				BeforeEach(func() {
					grantProfile(team, memberProfile, accessor.ViewerRole)
					useProfile(memberProfile)
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

	})
})
