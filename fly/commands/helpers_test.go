package commands_test

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/fly/commands"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/go-concourse/concourse"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Helper Functions", func() {
	var (
		atcServer *ghttp.Server
		client    concourse.Client
		team      concourse.Team
	)

	BeforeEach(func() {
		atcServer = ghttp.NewServer()
		client = concourse.NewClient(atcServer.URL(), &http.Client{}, false)
		team = client.Team("some-team")
	})

	AfterEach(func() {
		atcServer.Close()
	})

	Describe("#GetBuild", func() {
		expectedBuildID := "123"
		expectedBuildName := "5"
		expectedJobName := "myjob"
		expectedPipelineRef := atc.PipelineRef{Name: "mypipeline"}
		expectedBuild := atc.Build{
			ID:      123,
			Name:    expectedBuildName,
			Status:  "Great Success",
			JobName: expectedJobName,
			APIURL:  fmt.Sprintf("api/v1/builds/%s", expectedBuildID),
		}

		Context("when passed a build id", func() {
			expectedURL := "/api/v1/builds/123"

			Context("when no error is encountered while fetching build", func() {
				Context("when build exists", func() {
					BeforeEach(func() {
						atcServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL),
								ghttp.RespondWithJSONEncoded(http.StatusOK, expectedBuild),
							),
						)
					})

					It("returns the build", func() {
						build, err := GetBuild(client, nil, "", expectedBuildID, atc.PipelineRef{})
						Expect(err).NotTo(HaveOccurred())
						Expect(build).To(Equal(expectedBuild))
						Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
					})
				})

				Context("when a build does not exist", func() {
					BeforeEach(func() {
						atcServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL),
								ghttp.RespondWithJSONEncoded(http.StatusNotFound, nil),
							),
						)
					})

					It("returns an error", func() {
						_, err := GetBuild(client, nil, "", expectedBuildID, atc.PipelineRef{})
						Expect(err).To(HaveOccurred())
						Expect(err).To(MatchError("build not found"))
					})
				})
			})

			Context("when an error is encountered while fetching build", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", expectedURL),
							ghttp.RespondWith(http.StatusInternalServerError, "some-error"),
						),
					)
				})

				It("return an error", func() {
					_, err := GetBuild(client, nil, "", expectedBuildID, atc.PipelineRef{})
					Expect(err).To(MatchError(HavePrefix("failed to get build ")))
					Expect(err).To(MatchError(ContainSubstring("some-error")))
				})
			})
		})

		Context("when passed a pipeline and job name", func() {
			expectedURL := "/api/v1/teams/some-team/pipelines/mypipeline/jobs/myjob"

			Context("when no error was encountered while looking up for team job", func() {
				Context("when job exists", func() {
					Context("when the next build exists", func() {
						BeforeEach(func() {
							atcServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("GET", expectedURL),
									ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Job{
										Name:      expectedJobName,
										NextBuild: &expectedBuild,
									}),
								),
							)
						})

						It("returns the next build for that job", func() {
							build, err := GetBuild(client, team, expectedJobName, "", expectedPipelineRef)
							Expect(err).NotTo(HaveOccurred())
							Expect(build).To(Equal(expectedBuild))
							Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
						})
					})

					Context("when the only the finished build exists", func() {
						BeforeEach(func() {
							atcServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("GET", expectedURL),
									ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Job{
										Name:          expectedJobName,
										FinishedBuild: &expectedBuild,
									}),
								),
							)
						})

						It("returns the finished build for that job", func() {
							build, err := GetBuild(client, team, expectedJobName, "", expectedPipelineRef)
							Expect(err).NotTo(HaveOccurred())
							Expect(build).To(Equal(expectedBuild))
							Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
						})
					})

					Context("when no builds exist", func() {
						BeforeEach(func() {
							atcServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("GET", expectedURL),
									ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Job{
										Name: expectedJobName,
									}),
								),
							)
						})

						It("returns an error", func() {
							_, err := GetBuild(client, team, expectedJobName, "", expectedPipelineRef)
							Expect(err).To(HaveOccurred())
						})
					})
				})

				Context("when job does not exists", func() {
					BeforeEach(func() {
						atcServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL),
								ghttp.RespondWithJSONEncoded(http.StatusNotFound, nil),
							),
						)
					})

					It("returns an error", func() {
						_, err := GetBuild(client, team, expectedJobName, "", expectedPipelineRef)
						Expect(err).To(MatchError("job not found"))
					})
				})
			})

			Context("when an error was encountered while looking up for team job", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", expectedURL),
							ghttp.RespondWith(http.StatusInternalServerError, "some-error"),
						),
					)
				})

				It("should return an error", func() {
					_, err := GetBuild(client, team, expectedJobName, "", expectedPipelineRef)
					Expect(err).To(HaveOccurred())
					Expect(err).To(MatchError(HavePrefix("failed to get job ")))
					Expect(err).To(MatchError(ContainSubstring("some-error")))
				})
			})

		})

		Context("when passed pipeline, job, and build names", func() {
			expectedURL := "/api/v1/teams/some-team/pipelines/mypipeline/jobs/myjob/builds/5"

			Context("when the build exists", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", expectedURL),
							ghttp.RespondWithJSONEncoded(http.StatusOK, expectedBuild),
						),
					)
				})

				It("returns the build", func() {
					build, err := GetBuild(client, team, expectedJobName, expectedBuildName, expectedPipelineRef)
					Expect(err).NotTo(HaveOccurred())
					Expect(build).To(Equal(expectedBuild))
					Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
				})
			})

			Context("when the build does not exist", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", expectedURL),
							ghttp.RespondWithJSONEncoded(http.StatusNotFound, nil),
						),
					)
				})

				It("returns an error", func() {
					_, err := GetBuild(client, team, expectedJobName, expectedBuildName, expectedPipelineRef)
					Expect(err).To(MatchError("build not found"))
				})
			})
		})

		Context("when nothing is passed", func() {
			expectedURL := "/api/v1/builds"

			Context("when client.Builds does not return an error", func() {
				var allBuilds [300]atc.Build

				expectedOneOffBuild := atc.Build{
					ID:      150,
					Name:    expectedBuildName,
					Status:  "success",
					JobName: "",
					APIURL:  fmt.Sprintf("api/v1/builds/%s", expectedBuildID),
				}

				Context("when a build was found", func() {
					BeforeEach(func() {
						for i := 300 - 1; i >= 0; i-- {
							allBuilds[i] = atc.Build{
								ID:      i,
								Name:    strconv.Itoa(i),
								JobName: "some-job",
								APIURL:  fmt.Sprintf("api/v1/builds/%d", i),
							}
						}

						allBuilds[150] = expectedOneOffBuild

						atcServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL, "limit=100"),
								ghttp.RespondWithJSONEncoded(http.StatusOK, allBuilds[0:100], http.Header{
									"Link": []string{
										fmt.Sprintf(`<%s/api/v1/builds?to=99&limit=100>; rel="next"`, atcServer.URL()),
									},
								}),
							),
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL, "to=99&limit=100"),
								ghttp.RespondWithJSONEncoded(http.StatusOK, allBuilds[99:199]),
							),
						)
					})

					It("returns latest one off build", func() {
						build, err := GetBuild(client, nil, "", "", atc.PipelineRef{})
						Expect(err).NotTo(HaveOccurred())
						Expect(build).To(Equal(expectedOneOffBuild))
						Expect(atcServer.ReceivedRequests()).To(HaveLen(2))
					})
				})

				Context("when no builds were found ", func() {
					BeforeEach(func() {
						atcServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", expectedURL, "limit=100"),
								ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.Build{}),
							),
						)
					})

					It("returns an error", func() {
						_, err := GetBuild(client, nil, "", "", atc.PipelineRef{})
						Expect(err).To(HaveOccurred())
						Expect(err).To(MatchError("no builds match job"))
					})
				})
			})

			Context("when client.Builds returns an error", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", expectedURL, "limit=100"),
							ghttp.RespondWith(http.StatusInternalServerError, "some-error"),
						),
					)
				})

				It("should return an error", func() {
					_, err := GetBuild(client, nil, "", "", atc.PipelineRef{})
					Expect(err).To(HaveOccurred())
					Expect(err).To(MatchError(HavePrefix("failed to get builds ")))
					Expect(err).To(MatchError(ContainSubstring("some-error")))
				})
			})
		})
	})

	Describe("#GetLatestResourceVersions", func() {
		var resourceVersions []atc.ResourceVersion

		expectedURL := "/api/v1/teams/some-team/pipelines/mypipeline/resources/myresource/versions"

		resource := flaghelpers.ResourceFlag{
			PipelineRef: atc.PipelineRef{
				Name:         "mypipeline",
				InstanceVars: atc.InstanceVars{"branch": "master"},
			},
			ResourceName: "myresource",
		}

		BeforeEach(func() {
			resourceVersions = []atc.ResourceVersion{
				{
					ID:      1,
					Version: atc.Version{"version": "v1"},
				},
				{
					ID:      2,
					Version: atc.Version{"version": "v1"},
				},
			}
		})

		When("resource versions exist", func() {
			It("returns latest resource version", func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL, "filter=version%3Av1&vars.branch=%22master%22"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, resourceVersions),
					),
				)

				latestResourceVersion, err := GetLatestResourceVersion(team, resource, atc.Version{"version": "v1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(latestResourceVersion.Version).To(Equal(atc.Version{"version": "v1"}))
				Expect(latestResourceVersion.ID).To(Equal(1))
			})
		})

		When("call to resource versions returns an error", func() {
			It("returns an error", func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL, "filter=version%3Av1&vars.branch=%22master%22"),
						ghttp.RespondWith(http.StatusInternalServerError, "fake error"),
					),
				)

				_, err := GetLatestResourceVersion(team, resource, atc.Version{"version": "v1"})
				Expect(err).To(MatchError(ContainSubstring("fake error")))
			})
		})

		When("call to resource versions returns an empty array", func() {
			It("returns an error", func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL, "filter=version%3Av2&vars.branch=%22master%22"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.ResourceVersion{}),
					),
				)

				_, err := GetLatestResourceVersion(team, resource, atc.Version{"version": "v2"})
				Expect(err).To(MatchError("could not find version matching {\"version\":\"v2\"}"))
			})
		})
	})
})
