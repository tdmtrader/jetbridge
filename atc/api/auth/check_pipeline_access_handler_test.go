package auth_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckPipelineAccessHandler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		delegate *pipelineDelegateHandler
		factory  db.TeamFactory
		team     db.Team
		pipeline db.Pipeline
		handler  http.Handler

		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
	)

	BeforeEach(func() {
		// The handler resolves ?:team_name and ?:pipeline_name against rows, so
		// each Context sets up (or omits) what those names should find.
		factory = teamFactory
		team = createTeam("some-team")
		pipeline = nil

		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)
		delegate = &pipelineDelegateHandler{}
	})

	// JustBeforeEach, so a Context can swap `factory` for a doomed one before
	// the handler is built from it.
	JustBeforeEach(func() {
		innerHandler := auth.NewCheckPipelineAccessHandlerFactory(factory).
			HandlerFor(delegate, auth.UnauthorizedRejector{})

		handler = accessor.NewHandler(
			logger,
			"some-action",
			innerHandler,
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		)

		fakeAccessor.CreateReturns(fakeaccess, nil)
		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		server.Close()
	})

	Context("When team is not returned", func() {
		Context("when it returns an error", func() {
			BeforeEach(func() {
				factory = doomedTeamFactory()
			})
			It("returns an internal server error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
		Context("when team is not found", func() {
			BeforeEach(func() {
				Expect(team.Delete()).To(Succeed())
			})
			It("returns not found error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})

	})

	Context("when pipeline exists", func() {
		BeforeEach(func() {
			pipeline = createPipeline(team, "some-pipeline")
		})

		Context("when pipeline is public", func() {
			BeforeEach(func() {
				// Visibility is a column, not a config field: Expose() is what
				// makes a pipeline public.
				Expect(pipeline.Expose()).To(Succeed())
			})

			It("calls pipelineScopedHandler with pipelineDB in context", func() {
				Expect(delegate.IsCalled).To(BeTrue())
				Expect(delegate.ContextPipelineDB.ID()).To(Equal(pipeline.ID()))
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})
		})

		Context("when pipeline is private", func() {
			BeforeEach(func() {
				Expect(pipeline.Hide()).To(Succeed())
			})

			Context("and authorized", func() {
				BeforeEach(func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(true)
				})

				It("calls pipelineScopedHandler with pipelineDB in context", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(delegate.ContextPipelineDB.ID()).To(Equal(pipeline.ID()))
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("and unauthorized", func() {
				BeforeEach(func() {
					fakeaccess.IsAuthorizedReturns(false)
				})

				Context("and is authenticated", func() {
					BeforeEach(func() {
						fakeaccess.IsAuthenticatedReturns(true)
					})

					It("returns 403 Forbidden", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})

				Context("and not authenticated", func() {
					BeforeEach(func() {
						fakeaccess.IsAuthenticatedReturns(false)
					})

					It("returns 401 Unauthorized", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
			})
		})
	})

	Context("when pipeline does not exist", func() {
		BeforeEach(func() {
			// The team owns a differently-named pipeline, so the lookup misses.
			createPipeline(team, "some-other-pipeline")
		})

		It("returns 404", func() {
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})

		It("does not call the scoped handler", func() {
			Expect(delegate.IsCalled).To(BeFalse())
		})
	})

	Context("when getting pipeline fails", func() {
		BeforeEach(func() {
			factory = doomedTeamFactory()
		})

		It("returns 500", func() {
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("does not call the scoped handler", func() {
			Expect(delegate.IsCalled).To(BeFalse())
		})
	})
})

type pipelineDelegateHandler struct {
	IsCalled          bool
	ContextPipelineDB db.Pipeline
}

func (handler *pipelineDelegateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.IsCalled = true
	handler.ContextPipelineDB = r.Context().Value(auth.PipelineContextKey).(db.Pipeline)
}
