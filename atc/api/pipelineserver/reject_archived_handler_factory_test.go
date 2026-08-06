package pipelineserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rejected Archived Handler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		delegate *delegateHandler

		factory db.TeamFactory
	)

	BeforeEach(func() {
		delegate = &delegateHandler{}
		factory = teamFactory
	})

	// The team and pipeline are looked up by the names in the query string, so
	// each Context sets up (or deliberately omits) the rows those names resolve
	// to rather than stubbing the lookup.
	JustBeforeEach(func() {
		handlerFactory := pipelineserver.NewRejectArchivedHandlerFactory(factory)
		server = httptest.NewServer(handlerFactory.RejectArchived(delegate.GetHandler(nil)))

		request, err := http.NewRequest("POST", server.URL+"?:team_name=some-team&:pipeline_name=some-pipeline", nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
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
				It("returns the delegate handler", func() {
					Expect(delegate.IsCalled).To(BeTrue())
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

	Context("when the database is unavailable", func() {
		// Previously two Contexts -- one failing the team lookup, one failing the
		// pipeline lookup -- both asserting 500. A real database fails both
		// through the same closed connection, and which internal call errored
		// first is not something the handler's contract distinguishes: any
		// database failure is a 500.
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
	})
})
