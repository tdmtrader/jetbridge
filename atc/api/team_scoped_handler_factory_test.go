package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TeamScopedHandlerFactory", func() {
	var (
		response    *http.Response
		server      *httptest.Server
		delegate    *delegateHandler
		realdb      *realDB
		teamFactory db.TeamFactory
		someTeam    db.Team
		handler     http.Handler
	)

	BeforeEach(func() {
		realdb = useRealDB()
		teamFactory = realdb.Deps.teamFactory

		// The handler looks the team up by the name in the request, so the row
		// is what decides whether it is found.
		var err error
		someTeam, err = teamFactory.CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).NotTo(HaveOccurred())

		delegate = &delegateHandler{}

		logger := lagertest.NewTestLogger("test")

		handlerFactory := api.NewTeamScopedHandlerFactory(logger, teamFactory)
		innerHandler := handlerFactory.HandlerFor(delegate.GetHandler)

		handler = accessor.NewHandler(
			logger,
			"some-action",
			innerHandler,
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		)
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

	Context("when finding the team fails", func() {
		BeforeEach(func() {
			// A closed connection is a real failure the database produces on
			// demand; opened separately so the suite's own conn is untouched.
			doomed := postgresRunner.OpenConn()
			doomedFactory := db.NewTeamFactory(doomed, realdb.LockFactory)
			Expect(doomed.Close()).To(Succeed())

			handler = accessor.NewHandler(
				logger, "some-action",
				api.NewTeamScopedHandlerFactory(logger, doomedFactory).HandlerFor(delegate.GetHandler),
				fakeAccessor, new(auditorfakes.FakeAuditor), map[string]string{},
			)
		})

		It("returns 500", func() {
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})

	It("hands the scoped handler the team named in the request", func() {
		Expect(delegate.IsCalled).To(BeTrue())
		Expect(delegate.Team.ID()).To(Equal(someTeam.ID()))
		Expect(delegate.Team.Name()).To(Equal("some-team"))
	})
})

type delegateHandler struct {
	IsCalled bool
	Team     db.Team
}

func (handler *delegateHandler) GetHandler(team db.Team) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.IsCalled = true
		handler.Team = team
	})
}
