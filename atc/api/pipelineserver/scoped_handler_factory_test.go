package pipelineserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Handler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		delegate *delegateHandler

		factory db.TeamFactory
		handler http.Handler
	)

	BeforeEach(func() {
		delegate = &delegateHandler{}
		factory = teamFactory
	})

	JustBeforeEach(func() {
		handlerFactory := pipelineserver.NewScopedHandlerFactory(factory)
		handler = handlerFactory.HandlerFor(delegate.GetHandler)
		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		server.Close()
	})

	Context("when pipeline is in request context", func() {
		var contextPipeline db.Pipeline

		BeforeEach(func() {
			contextPipeline = createPipeline(createTeam("context-team"), "context-pipeline")
		})

		JustBeforeEach(func() {
			// Rebuild with the wrapper, then reissue: a pipeline already in the
			// context short-circuits the lookup entirely.
			server.Close()
			server = httptest.NewServer(&wrapHandler{handler, contextPipeline})

			request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
			Expect(err).NotTo(HaveOccurred())
			response, err = new(http.Client).Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		It("calls scoped handler with pipeline from context", func() {
			Expect(delegate.IsCalled).To(BeTrue())
			Expect(delegate.Pipeline).To(BeIdenticalTo(contextPipeline))
		})
	})

	Context("when pipeline is not in request context", func() {
		Context("when the team does not exist", func() {
			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})

			It("does not call the scoped handler", func() {
				Expect(delegate.IsCalled).To(BeFalse())
			})
		})

		Context("when finding the team fails", func() {
			BeforeEach(func() {
				doomed := postgresRunner.OpenConn()
				doomedFactory := db.NewTeamFactory(doomed, lockFactory)
				_, err := doomedFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				Expect(doomed.Close()).To(Succeed())

				factory = doomedFactory
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})

			It("does not call the scoped handler", func() {
				Expect(delegate.IsCalled).To(BeFalse())
			})
		})

		Context("when finding the pipeline fails", func() {
			BeforeEach(func() {
				factory = teamFactoryFailingPipelineLookup(errors.New("pipeline lookup failed"))
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})

			It("does not call the scoped handler", func() {
				Expect(delegate.IsCalled).To(BeFalse())
			})
		})

		Context("when the team exists", func() {
			var team db.Team

			BeforeEach(func() {
				team = createTeam("some-team")
			})

			Context("when the request carries a different team name in a form body", func() {
				JustBeforeEach(func() {
					// A team the caller is trying to reach by putting its name in
					// the body. Both teams own a pipeline of the same name, so the
					// only way to tell which one the handler scoped to is which
					// pipeline the delegate receives -- which is the property that
					// matters, and one the old FindTeamArgsForCall assertion could
					// only approximate.
					otherTeam := createTeam("some-other-team")
					createPipeline(otherTeam, "some-pipeline")
					urlTeamPipeline := createPipeline(team, "some-pipeline")

					body := url.Values{":team_name": {"some-other-team"}}
					request, err := http.NewRequest(
						"POST",
						server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline",
						strings.NewReader(body.Encode()),
					)
					Expect(err).NotTo(HaveOccurred())
					request.Header.Add("Content-type", "application/x-www-form-urlencoded")

					response, err = new(http.Client).Do(request)
					Expect(err).NotTo(HaveOccurred())

					Expect(delegate.IsCalled).To(BeTrue())
					Expect(delegate.Pipeline.ID()).To(Equal(urlTeamPipeline.ID()),
						"the team name in the URL must win over the one in the body")
				})

				It("scopes to the team named in the URL", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when the pipeline exists", func() {
				var pipeline db.Pipeline

				BeforeEach(func() {
					pipeline = createPipeline(team, "some-pipeline")
				})

				It("hands the scoped handler that pipeline", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(delegate.Pipeline.ID()).To(Equal(pipeline.ID()))
					Expect(delegate.Pipeline.Name()).To(Equal("some-pipeline"))
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when the pipeline does not exist", func() {
				BeforeEach(func() {
					createPipeline(team, "some-other-pipeline")
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})

				It("does not call the scoped handler", func() {
					Expect(delegate.IsCalled).To(BeFalse())
				})
			})
		})
	})
})

// Keep this fake at the narrow late-query seam: FindTeam succeeds, then only
// Team.Pipeline fails. Closing a real connection would fail FindTeam first and
// leave the handler's distinct pipeline-error branch untested.
func teamFactoryFailingPipelineLookup(err error) db.TeamFactory {
	return pipelineLookupFailureFactory{err: err}
}

type pipelineLookupFailureFactory struct {
	db.TeamFactory
	err error
}

func (factory pipelineLookupFailureFactory) FindTeam(string) (db.Team, bool, error) {
	return pipelineLookupFailureTeam{err: factory.err}, true, nil
}

type pipelineLookupFailureTeam struct {
	db.Team
	err error
}

func (team pipelineLookupFailureTeam) Pipeline(atc.PipelineRef) (db.Pipeline, bool, error) {
	return nil, false, team.err
}

type delegateHandler struct {
	IsCalled bool
	Pipeline db.Pipeline
}

func (handler *delegateHandler) GetHandler(dbPipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.IsCalled = true
		handler.Pipeline = dbPipeline
	})
}

type wrapHandler struct {
	delegate        http.Handler
	contextPipeline db.Pipeline
}

func (h *wrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), auth.PipelineContextKey, h.contextPipeline)
	h.delegate.ServeHTTP(w, r.WithContext(ctx))
}
