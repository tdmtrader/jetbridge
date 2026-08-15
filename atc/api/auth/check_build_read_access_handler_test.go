package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckBuildReadAccessHandler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		handler  http.Handler

		team        db.Team
		pipeline    db.Pipeline
		build       db.Build
		jobConfig   atc.JobConfig
		requestedID int

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string

		// which of the factory's two handlers this Context exercises
		handlerKind string

		// "" leaves the pipeline as saved; Contexts set these and the fixture is
		// built in JustBeforeEach, so an inner Context can still adjust jobConfig
		// after an outer one has chosen visibility.
		pipelineVisibility string
	)

	BeforeEach(func() {
		team = createTeam("some-team")
		jobConfig = atc.JobConfig{Name: "some-job"}

		// Reset per spec: Ginkgo re-runs BeforeEach but package-scoped vars in
		// the Describe otherwise carry the previous spec's fixture over.
		pipeline = nil
		build = nil
		requestedID = 0
		authorization = ""
		pipelineVisibility = ""
	})

	// The fixture is built in JustBeforeEach so a Context can set jobConfig
	// (public or not) or replace the build entirely before it is saved.
	JustBeforeEach(func() {
		if build == nil {
			pipeline, build = createJobBuildWithConfig(team, "some-pipeline", jobConfig)
		}
		// A one-off build has no pipeline, so there is nothing to expose or hide.
		if pipeline != nil {
			switch pipelineVisibility {
			case "public":
				Expect(pipeline.Expose()).To(Succeed())
			case "private":
				Expect(pipeline.Hide()).To(Succeed())
			}
		}
		if requestedID == 0 {
			requestedID = build.ID()
		}

		handlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(buildFactory)
		var innerHandler http.Handler
		switch handlerKind {
		case "anyJob":
			innerHandler = handlerFactory.AnyJobHandler(http.HandlerFunc(renderBuildScope), auth.UnauthorizedRejector{})
		default:
			innerHandler = handlerFactory.CheckIfPrivateJobHandler(http.HandlerFunc(renderBuildScope), auth.UnauthorizedRejector{})
		}
		// A real accessor resolves the role from the action, and every role
		// fails the blank one, so the action has to be a route that has one.
		handler = accessor.NewHandler(
			logger, atc.GetBuild, innerHandler, realAccessFactory(),
			realAuditor(), map[string]string{},
		)

		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST",
			fmt.Sprintf("%s?:build_id=%d", server.URL, requestedID), nil)
		Expect(err).NotTo(HaveOccurred())

		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		Expect(response.Body.Close()).To(Succeed())
		server.Close()
	})

	ItReturnsTheBuild := func() {
		It("returns 200 ok", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("scopes the response to the requested build", func() {
			Expect(response.Header.Get("X-Concourse-Scoped-Build")).To(Equal(fmt.Sprint(build.ID())))
		})
	}

	WithExistingBuild := func(buildExistsFunc func()) {
		Context("when build exists", func() {
			buildExistsFunc()
		})

		Context("when build is not found", func() {
			BeforeEach(func() {
				requestedID = 1 << 20
			})

			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	}

	Context("AnyJobHandler", func() {
		BeforeEach(func() {
			handlerKind = "anyJob"
		})

		Context("when authenticated and accessing same team's build", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
				grantRole(team, accessor.ViewerRole)
			})

			WithExistingBuild(ItReturnsTheBuild)
		})

		Context("when authenticated but accessing different team's build", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
				// The role is granted on another team entirely, so the build's
				// team resolves to no role at all.
				grantRole(createTeam("some-other-team"), accessor.ViewerRole)
			})

			WithExistingBuild(func() {
				Context("when pipeline is public", func() {
					BeforeEach(func() {
						pipelineVisibility = "public"
					})

					ItReturnsTheBuild()
				})

				Context("when pipeline is private", func() {
					BeforeEach(func() {
						pipelineVisibility = "private"
					})

					It("returns 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})
				Context("when the build is not for a pipeline", func() {
					BeforeEach(func() {
						// A one-off build has no pipeline.
						var err error
						build, err = team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())
					})
					It("return 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				grantRole(team, accessor.ViewerRole)
			})

			WithExistingBuild(func() {
				Context("when pipeline is public", func() {
					BeforeEach(func() {
						pipelineVisibility = "public"
					})

					ItReturnsTheBuild()
				})

				Context("when pipeline is private", func() {
					BeforeEach(func() {
						pipelineVisibility = "private"
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
				Context("when the build is not for a pipeline", func() {
					BeforeEach(func() {
						// A one-off build has no pipeline.
						var err error
						build, err = team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())
					})
					It("return 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
			})
		})
	})

	Context("CheckIfPrivateJobHandler", func() {

		BeforeEach(func() {
			handlerKind = "privateJob"
		})

		Context("when a public historical build's job is no longer active", func() {
			BeforeEach(func() {
				pipeline, build = createJobBuildWithConfig(
					team,
					"some-pipeline",
					atc.JobConfig{Name: "some-job", Public: true},
				)
				Expect(pipeline.Expose()).To(Succeed())

				var err error
				pipeline, _, err = team.SavePipeline(
					atc.PipelineRef{Name: "some-pipeline"},
					atc.Config{},
					pipeline.ConfigVersion(),
					false,
				)
				Expect(err).NotTo(HaveOccurred())

				_, found, err := pipeline.Job("some-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})

			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})

		ItChecksIfJobIsPrivate := func(status int) {
			Context("when pipeline is public", func() {
				BeforeEach(func() {
					pipelineVisibility = "public"
				})

				Context("when the build is not for a job", func() {
					BeforeEach(func() {
						// A one-off build belongs to no job.
						var err error
						build, err = team.CreateOneOffBuild()
						Expect(err).NotTo(HaveOccurred())
					})

					It("returns "+fmt.Sprint(status), func() {
						Expect(response.StatusCode).To(Equal(status))
					})
				})

				Context("and job is public", func() {
					BeforeEach(func() {
						jobConfig = atc.JobConfig{Name: "some-job", Public: true}
					})

					ItReturnsTheBuild()
				})

				Context("and job is private", func() {
					BeforeEach(func() {
						jobConfig = atc.JobConfig{Name: "some-job", Public: false}
					})

					It("returns "+fmt.Sprint(status), func() {
						Expect(response.StatusCode).To(Equal(status))
					})
				})
			})

			Context("when pipeline is private", func() {
				BeforeEach(func() {
					pipelineVisibility = "private"
				})

				It("returns "+fmt.Sprint(status), func() {
					Expect(response.StatusCode).To(Equal(status))
				})
			})
		}

		Context("when authenticated and accessing same team's build", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
				grantRole(team, accessor.ViewerRole)
			})

			WithExistingBuild(ItReturnsTheBuild)
		})

		Context("when authenticated but accessing different team's build", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
				// The role is granted on another team entirely, so the build's
				// team resolves to no role at all.
				grantRole(createTeam("some-other-team"), accessor.ViewerRole)
			})

			WithExistingBuild(func() {
				ItChecksIfJobIsPrivate(http.StatusForbidden)
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				grantRole(team, accessor.ViewerRole)
			})

			WithExistingBuild(func() {
				ItChecksIfJobIsPrivate(http.StatusUnauthorized)
			})
		})
	})
})

func renderBuildScope(w http.ResponseWriter, r *http.Request) {
	build := r.Context().Value(auth.BuildContextKey).(db.BuildForAPI)
	w.Header().Set("X-Concourse-Scoped-Build", fmt.Sprint(build.ID()))
}
