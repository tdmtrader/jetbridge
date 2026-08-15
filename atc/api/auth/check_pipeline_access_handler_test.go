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

var _ = Describe("CheckPipelineAccessHandler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		team     db.Team
		pipeline db.Pipeline
		handler  http.Handler

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	BeforeEach(func() {
		// The handler resolves ?:team_name and ?:pipeline_name against rows, so
		// each Context sets up (or omits) what those names should find.
		team = createTeam("some-team")
		pipeline = nil
		authorization = ""
	})

	JustBeforeEach(func() {
		innerHandler := auth.NewCheckPipelineAccessHandlerFactory(teamFactory).
			HandlerFor(http.HandlerFunc(renderPipelineScope), auth.UnauthorizedRejector{})

		// A real accessor resolves the role from the action, and every role
		// fails the blank one, so the action has to be a route that has one.
		handler = accessor.NewHandler(
			logger,
			atc.GetPipeline,
			innerHandler,
			realAccessFactory(),
			realAuditor(),
			map[string]string{},
		)

		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
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

	Context("When team is not returned", func() {
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

			It("scopes the response to the public pipeline", func() {
				Expect(response.Header.Get("X-Concourse-Scoped-Pipeline")).To(Equal(fmt.Sprint(pipeline.ID())))
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
					authorization = validAccessToken()
					grantRole(team, accessor.ViewerRole)
				})

				It("scopes the response to the authorized pipeline", func() {
					Expect(response.Header.Get("X-Concourse-Scoped-Pipeline")).To(Equal(fmt.Sprint(pipeline.ID())))
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("and unauthorized", func() {
				BeforeEach(func() {
					// The role is granted on another team entirely, so the
					// request's team resolves to no role at all.
					grantRole(createTeam("some-other-team"), accessor.ViewerRole)
				})

				Context("and is authenticated", func() {
					BeforeEach(func() {
						authorization = validAccessToken()
					})

					It("returns 403 Forbidden", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})

				Context("and not authenticated", func() {
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
	})
})

func renderPipelineScope(w http.ResponseWriter, r *http.Request) {
	pipeline := r.Context().Value(auth.PipelineContextKey).(db.Pipeline)
	w.Header().Set("X-Concourse-Scoped-Pipeline", fmt.Sprint(pipeline.ID()))
}
