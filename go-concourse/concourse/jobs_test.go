package concourse_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("ATC Handler Jobs", func() {
	Describe("JobBuilds", func() {
		var (
			expectedURL   = "/api/v1/teams/some-team/pipelines/mypipeline/jobs/myjob/builds"
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: "mypipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", expectedURL, expectedQuery),
				ghttp.RespondWith(http.StatusInternalServerError, ""),
			))
		})

		Context("when the server returns an error", func() {
			It("returns false and an error", func() {
				_, _, found, err := team.JobBuilds(pipelineRef, "myjob", concourse.Page{})
				Expect(err).To(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("PauseJob", func() {
		var (
			pipelineName  = "banana"
			jobName       = "disjob"
			expectedURL   = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/jobs/%s/pause", pipelineName, jobName)
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
				ghttp.RespondWith(http.StatusInternalServerError, nil),
			))
		})

		Context("when the pause job call fails", func() {
			It("calls the pause job and returns an error", func() {
				Expect(func() {
					paused, err := team.PauseJob(pipelineRef, jobName)
					Expect(err).To(HaveOccurred())
					Expect(paused).To(BeFalse())
				}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
			})
		})
	})

	Describe("UnpauseJob", func() {
		var (
			pipelineName  = "banana"
			jobName       = "disjob"
			expectedURL   = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/jobs/%s/unpause", pipelineName, jobName)
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
				ghttp.RespondWith(http.StatusInternalServerError, nil),
			))
		})

		Context("when the pause job call fails", func() {
			It("calls the pause job and returns an error", func() {
				Expect(func() {
					paused, err := team.UnpauseJob(pipelineRef, jobName)
					Expect(err).To(HaveOccurred())
					Expect(paused).To(BeFalse())
				}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
			})
		})
	})

	Describe("ScheduleJob", func() {
		var (
			pipelineName  = "banana"
			jobName       = "disjob"
			expectedURL   = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/jobs/%s/schedule", pipelineName, jobName)
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
				ghttp.RespondWith(http.StatusInternalServerError, nil),
			))
		})

		Context("when the schedule job call fails", func() {
			It("calls the schedule job and returns an error", func() {
				Expect(func() {
					requested, err := team.ScheduleJob(pipelineRef, jobName)
					Expect(err).To(HaveOccurred())
					Expect(requested).To(BeFalse())
				}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
			})
		})
	})

	Describe("Clear Job Task Cache", func() {
		var (
			expectedURL   string
			requestMethod string
			expectedQuery []string
			pipelineRef   = atc.PipelineRef{Name: "mypipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		BeforeEach(func() {
			requestMethod = "DELETE"
			expectedQuery = []string{"vars.branch=%22master%22"}
		})

		Context("when job step exists", func() {
			JustBeforeEach(func() {
				expectedURL = "/api/v1/teams/some-team/pipelines/mypipeline/jobs/myjob/tasks/mystep/cache"
				atcServer.AppendHandlers(ghttp.CombineHandlers(
					ghttp.VerifyRequest(requestMethod, expectedURL, strings.Join(expectedQuery, "&")),
					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.ClearTaskCacheResponse{CachesRemoved: 1}),
				))
			})

			Context("when no cache path is given", func() {
				It("succeeds", func() {
					Expect(func() {
						numDeleted, err := team.ClearTaskCache(pipelineRef, "myjob", "mystep", "")
						Expect(err).NotTo(HaveOccurred())
						Expect(numDeleted).To(Equal(int64(1)))
					}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
				})
			})

			Context("when a cache path is given", func() {
				BeforeEach(func() { expectedQuery = append(expectedQuery, "cache_path=mycachepath") })
				Context("when the cache path exists", func() {
					It("succeeds", func() {
						Expect(func() {
							numDeleted, err := team.ClearTaskCache(pipelineRef, "myjob", "mystep", "mycachepath")
							Expect(err).NotTo(HaveOccurred())
							Expect(numDeleted).To(Equal(int64(1)))
						}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
					})
				})
			})
		})

		Context("when job step does not exist", func() {
			BeforeEach(func() {
				expectedURL = "/api/v1/teams/some-team/pipelines/mypipeline/jobs/myjob/tasks/my-nonexistent-step/cache"
				expectedQuery = append(expectedQuery, "cache_path=mycachepath")
				atcServer.AppendHandlers(ghttp.CombineHandlers(
					ghttp.VerifyRequest(requestMethod, expectedURL, strings.Join(expectedQuery, "&")),
					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.ClearTaskCacheResponse{CachesRemoved: 0}),
				))
			})

			It("returns that 0 caches were deleted", func() {
				Expect(func() {
					numDeleted, err := team.ClearTaskCache(pipelineRef, "myjob", "my-nonexistent-step", "mycachepath")
					Expect(err).NotTo(HaveOccurred())
					Expect(numDeleted).To(BeZero())
				}).To(Change(func() int { return len(atcServer.ReceivedRequests()) }).By(1))
			})
		})
	})
})
