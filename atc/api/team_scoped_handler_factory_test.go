package api_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TeamScopedHandlerFactory", func() {
	var (
		response *http.Response
		server   *httptest.Server
		realdb   *realDB
		someTeam db.Team
		handler  http.Handler
	)

	BeforeEach(func() {
		realdb = useRealDB()

		// The handler looks the team up by the name in the request, so the row
		// is what decides whether it is found.
		var err error
		someTeam, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).NotTo(HaveOccurred())

		logger := lagertest.NewTestLogger("test")
		handlerFactory := api.NewTeamScopedHandlerFactory(logger, realdb.Deps.teamFactory)
		handler = handlerFactory.HandlerFor(func(team db.Team) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, "%d:%s", team.ID(), team.Name())
			})
		})
	})

	JustBeforeEach(func() {
		server = httptest.NewServer(handler)

		fullUrl := fmt.Sprintf("%s?:team_name=some-team", server.URL)

		serverUrl, err := url.Parse(fullUrl)
		Expect(err).NotTo(HaveOccurred())

		request, err := http.NewRequest("POST", serverUrl.String(), nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		server.Close()
	})

	Context("when the team is not found", func() {
		BeforeEach(func() {
			// The request asks for "some-team"; drop the row so the lookup misses.
			Expect(someTeam.Delete()).To(Succeed())
		})

		It("returns 404", func() {
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})

	It("hands the scoped handler the team named in the request", func() {
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal(fmt.Sprintf("%d:some-team", someTeam.ID())))
	})
})
