package pipelineserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Handler", func() {
	var (
		response        *http.Response
		server          *httptest.Server
		handler         http.Handler
		contextPipeline db.Pipeline
		requestForm     url.Values
	)

	BeforeEach(func() {
		contextPipeline = nil
		requestForm = nil
	})

	JustBeforeEach(func() {
		handlerFactory := pipelineserver.NewScopedHandlerFactory(teamFactory)
		handler = handlerFactory.HandlerFor(renderPipeline)
		if contextPipeline != nil {
			handler = &pipelineContextHandler{next: handler, pipeline: contextPipeline}
		}
		server = httptest.NewServer(handler)

		var body *strings.Reader
		if requestForm == nil {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(requestForm.Encode())
		}
		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", body)
		Expect(err).NotTo(HaveOccurred())
		if requestForm != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(response.Body.Close()).To(Succeed())
		server.Close()
	})

	Context("when pipeline is in request context", func() {
		BeforeEach(func() {
			contextPipeline = createPipeline(createTeam("context-team"), "context-pipeline")
		})

		It("renders the pipeline already present in context", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("X-Concourse-Scoped-Pipeline")).To(Equal(fmt.Sprint(contextPipeline.ID())))
			Expect(response.Header.Get("X-Concourse-Scoped-Team")).To(Equal("context-team"))
		})
	})

	Context("when pipeline is not in request context", func() {
		Context("when the team does not exist", func() {
			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})

		Context("when the team exists", func() {
			var team db.Team

			BeforeEach(func() {
				team = createTeam("some-team")
			})

			Context("when the request carries a different team name in a form body", func() {
				var urlTeamPipeline db.Pipeline

				BeforeEach(func() {
					// A team the caller is trying to reach by putting its name in
					// the body. Both teams own a pipeline of the same name, so the
					// only way to tell which one the handler scoped to is which
					// pipeline identity the response renders -- the externally
					// meaningful scoping property this scenario must prove.
					otherTeam := createTeam("some-other-team")
					createPipeline(otherTeam, "some-pipeline")
					urlTeamPipeline = createPipeline(team, "some-pipeline")
					requestForm = url.Values{":team_name": {"some-other-team"}}
				})

				It("scopes to the team named in the URL", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Scoped-Pipeline")).To(Equal(fmt.Sprint(urlTeamPipeline.ID())))
					Expect(response.Header.Get("X-Concourse-Scoped-Team")).To(Equal("some-team"))
				})
			})

			Context("when the pipeline exists", func() {
				var pipeline db.Pipeline

				BeforeEach(func() {
					pipeline = createPipeline(team, "some-pipeline")
				})

				It("renders the resolved pipeline", func() {
					Expect(response.Header.Get("X-Concourse-Scoped-Pipeline")).To(Equal(fmt.Sprint(pipeline.ID())))
					Expect(response.Header.Get("X-Concourse-Scoped-Pipeline-Name")).To(Equal("some-pipeline"))
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
			})
		})
	})
})

func renderPipeline(dbPipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Concourse-Scoped-Pipeline", fmt.Sprint(dbPipeline.ID()))
		w.Header().Set("X-Concourse-Scoped-Pipeline-Name", dbPipeline.Name())
		w.Header().Set("X-Concourse-Scoped-Team", dbPipeline.TeamName())
	})
}

type pipelineContextHandler struct {
	next     http.Handler
	pipeline db.Pipeline
}

func (h *pipelineContextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), auth.PipelineContextKey, h.pipeline)
	h.next.ServeHTTP(w, r.WithContext(ctx))
}
