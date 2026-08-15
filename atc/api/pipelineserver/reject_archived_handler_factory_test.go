package pipelineserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rejected Archived Handler", func() {
	var (
		response *http.Response
		server   *httptest.Server
	)

	// The team and pipeline are looked up by the names in the query string, so
	// each Context sets up (or deliberately omits) the rows those names resolve
	// to and exercises the production lookup.
	JustBeforeEach(func() {
		handlerFactory := pipelineserver.NewRejectArchivedHandlerFactory(teamFactory)
		server = httptest.NewServer(handlerFactory.RejectArchived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Concourse-Archive-Check", "active")
		})))

		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(response.Body.Close()).To(Succeed())
		server.Close()
	})

	Context("when a team is found", func() {
		var team db.Team

		BeforeEach(func() {
			team = createTeam("some-team")
		})

		Context("when a pipeline is found", func() {
			var pipeline db.Pipeline

			BeforeEach(func() {
				pipeline = createPipeline(team, "some-pipeline")
			})

			Context("when a pipeline is archived", func() {
				BeforeEach(func() {
					Expect(pipeline.Archive()).To(Succeed())
				})

				It("returns 409", func() {
					Expect(response.StatusCode).To(Equal(http.StatusConflict))
				})

				It("returns an error in the body", func() {
					body, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(body).To(ContainSubstring("action not allowed for an archived pipeline"))
				})
			})

			Context("when a pipeline is not archived", func() {
				It("returns the active-pipeline response", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Archive-Check")).To(Equal("active"))
				})
			})
		})

		Context("when a pipeline is not found", func() {
			// The team exists but owns a differently-named pipeline.
			BeforeEach(func() {
				createPipeline(team, "some-other-pipeline")
			})

			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Context("when a team is not found", func() {
		BeforeEach(func() {
			createTeam("some-other-team")
		})

		It("returns 404", func() {
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
