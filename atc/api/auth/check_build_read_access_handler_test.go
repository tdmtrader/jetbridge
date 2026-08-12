package auth_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckBuildReadAccessHandler", func() {
	var (
		response     *http.Response
		server       *httptest.Server
		delegate     *buildDelegateHandler
		factory      db.BuildFactory
		handler      http.Handler
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess

		team        db.Team
		pipeline    db.Pipeline
		build       db.Build
		jobConfig   atc.JobConfig
		requestedID int

		// which of the factory's two handlers this Context exercises
		handlerKind string

		// "" leaves the pipeline as saved; Contexts set these and the fixture is
		// built in JustBeforeEach, so an inner Context can still adjust jobConfig
		// after an outer one has chosen visibility.
		pipelineVisibility string
	)

	BeforeEach(func() {
		factory = buildFactory
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)

		delegate = &buildDelegateHandler{}

		team = createTeam("some-team")
		jobConfig = atc.JobConfig{Name: "some-job"}

		// Reset per spec: Ginkgo re-runs BeforeEach but package-scoped vars in
		// the Describe otherwise carry the previous spec's fixture over.
		pipeline = nil
		build = nil
		requestedID = 0
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

		// Built here, not in each Context's BeforeEach, so an inner Context can
		// swap `factory` for a doomed one first.
		handlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(factory)
		var innerHandler http.Handler
		switch handlerKind {
		case "anyJob":
			innerHandler = handlerFactory.AnyJobHandler(delegate, auth.UnauthorizedRejector{})
		default:
			innerHandler = handlerFactory.CheckIfPrivateJobHandler(delegate, auth.UnauthorizedRejector{})
		}
		handler = accessor.NewHandler(
			logger, "some-action", innerHandler, fakeAccessor,
			new(auditorfakes.FakeAuditor), map[string]string{},
		)

		fakeAccessor.CreateReturns(fakeaccess, nil)
		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST",
			fmt.Sprintf("%s?:build_id=%d", server.URL, requestedID), nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		server.Close()
	})

	ItReturnsTheBuild := func() {
		It("returns 200 ok", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("calls delegate with the build context", func() {
			Expect(delegate.IsCalled).To(BeTrue())
			Expect(delegate.ContextBuild.ID()).To(Equal(build.ID()))
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

		Context("when getting build fails", func() {
			BeforeEach(func() {
				factory = doomedBuildFactory()
			})

			It("returns 503", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	}

	Context("AnyJobHandler", func() {
		BeforeEach(func() {
			handlerKind = "anyJob"
		})

		Context("when authenticated and accessing same team's build", func() {
			BeforeEach(func() {
				fakeaccess.IsAuthenticatedReturns(true)
				fakeaccess.IsAuthorizedReturns(true)
			})

			WithExistingBuild(ItReturnsTheBuild)
		})

		Context("when authenticated but accessing different team's build", func() {
			BeforeEach(func() {
				fakeaccess.IsAuthenticatedReturns(true)
				fakeaccess.IsAuthorizedReturns(false)
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
				Context("when fetching pipeline throws error", func() {
					BeforeEach(func() {
						factory = buildFactoryWithPipelineLookup(false, errors.New("pipeline lookup failed"))
					})
					It("return 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
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
				Context("when the pipeline disappears during the request", func() {
					BeforeEach(func() {
						factory = buildFactoryWithPipelineLookup(false, nil)
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeaccess.IsAuthenticatedReturns(false)
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
				Context("when fetching pipeline throws error", func() {
					BeforeEach(func() {
						factory = buildFactoryWithPipelineLookup(false, errors.New("pipeline lookup failed"))
					})
					It("return 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
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
				Context("when the pipeline disappears during the request", func() {
					BeforeEach(func() {
						factory = buildFactoryWithPipelineLookup(false, nil)
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
					})
				})
			})
		})
	})

	Context("CheckIfPrivateJobHandler", func() {

		BeforeEach(func() {
			handlerKind = "privateJob"
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

				Context("getting the job fails", func() {
					BeforeEach(func() {
						factory = buildFactoryWithJobLookup(false, errors.New("job lookup failed"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("when the job disappears during the request", func() {
					BeforeEach(func() {
						factory = buildFactoryWithJobLookup(false, nil)
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
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
				fakeaccess.IsAuthenticatedReturns(true)
				fakeaccess.IsAuthorizedReturns(true)
			})

			WithExistingBuild(ItReturnsTheBuild)
		})

		Context("when authenticated but accessing different team's build", func() {
			BeforeEach(func() {
				fakeaccess.IsAuthenticatedReturns(true)
				fakeaccess.IsAuthorizedReturns(false)
			})

			WithExistingBuild(func() {
				ItChecksIfJobIsPrivate(http.StatusForbidden)
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeaccess.IsAuthenticatedReturns(false)
			})

			WithExistingBuild(func() {
				ItChecksIfJobIsPrivate(http.StatusUnauthorized)
			})
		})
	})
})

// These factories retain only the late-lookup seams that real PostgreSQL
// cannot trigger deterministically: BuildForAPI succeeds, then a concurrent
// cascade can remove the selected parent before its lookup. They also isolate
// errors to that second query; a closed connection would fail BuildForAPI
// first and leave both branches untested.
func buildFactoryWithPipelineLookup(found bool, err error) db.BuildFactory {
	return lateLookupBuildFactory{
		BuildFactory: buildFactory,
		wrap: func(build db.BuildForAPI) db.BuildForAPI {
			return pipelineLookupResultBuild{BuildForAPI: build, found: found, err: err}
		},
	}
}

func buildFactoryWithJobLookup(found bool, err error) db.BuildFactory {
	return lateLookupBuildFactory{
		BuildFactory: buildFactory,
		wrap: func(build db.BuildForAPI) db.BuildForAPI {
			return jobLookupResultBuild{BuildForAPI: build, found: found, err: err}
		},
	}
}

type lateLookupBuildFactory struct {
	db.BuildFactory

	wrap func(db.BuildForAPI) db.BuildForAPI
}

func (factory lateLookupBuildFactory) BuildForAPI(buildID int) (db.BuildForAPI, bool, error) {
	build, found, err := factory.BuildFactory.BuildForAPI(buildID)
	if err != nil || !found {
		return build, found, err
	}
	return factory.wrap(build), true, nil
}

type pipelineLookupResultBuild struct {
	db.BuildForAPI

	found bool
	err   error
}

func (build pipelineLookupResultBuild) Pipeline() (db.Pipeline, bool, error) {
	return nil, build.found, build.err
}

type jobLookupResultBuild struct {
	db.BuildForAPI

	found bool
	err   error
}

func (build jobLookupResultBuild) Pipeline() (db.Pipeline, bool, error) {
	pipeline, found, err := build.BuildForAPI.Pipeline()
	if err != nil || !found {
		return pipeline, found, err
	}
	return jobLookupResultPipeline{Pipeline: pipeline, found: build.found, err: build.err}, true, nil
}

type jobLookupResultPipeline struct {
	db.Pipeline

	found bool
	err   error
}

func (pipeline jobLookupResultPipeline) Job(string) (db.Job, bool, error) {
	return nil, pipeline.found, pipeline.err
}

type buildDelegateHandler struct {
	IsCalled     bool
	ContextBuild db.BuildForAPI
}

func (handler *buildDelegateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.IsCalled = true
	handler.ContextBuild = r.Context().Value(auth.BuildContextKey).(db.BuildForAPI)
}
