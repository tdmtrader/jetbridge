package auth_test

import (
	"errors"
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
		delegate *pipelineDelegateHandler
		factory  db.TeamFactory
		handler  http.Handler

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	BeforeEach(func() {
		// The handler resolves ?:team_name and ?:pipeline_name against rows, so
		// each Context sets up (or omits) what those names should find.
		factory = teamFactory
		createTeam("some-team")
		authorization = ""

		delegate = &pipelineDelegateHandler{}
	})

	// JustBeforeEach, so a Context can swap `factory` for a doomed one before
	// the handler is built from it.
	JustBeforeEach(func() {
		innerHandler := auth.NewCheckPipelineAccessHandlerFactory(factory).
			HandlerFor(delegate, auth.UnauthorizedRejector{})

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
	})

	Context("when getting pipeline fails", func() {
		BeforeEach(func() {
			// A real database cannot selectively fail only Team.Pipeline while
			// leaving TeamFactory.FindTeam healthy. Keep this seam deliberately
			// narrow so the request reaches the late lookup under test.
			factory = pipelineLookupFailsTeamFactory{TeamFactory: teamFactory}
		})

		It("returns 500", func() {
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})

		It("does not call the scoped handler", func() {
			Expect(delegate.IsCalled).To(BeFalse())
		})
	})
})

// pipelineLookupFailsTeamFactory resolves the team for real and then fails only
// the pipeline lookup made through it, which is the one ordering the handler
// distinguishes but a live connection cannot produce on demand.
type pipelineLookupFailsTeamFactory struct {
	db.TeamFactory
}

func (factory pipelineLookupFailsTeamFactory) FindTeam(name string) (db.Team, bool, error) {
	team, found, err := factory.TeamFactory.FindTeam(name)
	if err != nil || !found {
		return team, found, err
	}
	return pipelineLookupFailsTeam{Team: team}, true, nil
}

type pipelineLookupFailsTeam struct {
	db.Team
}

func (pipelineLookupFailsTeam) Pipeline(atc.PipelineRef) (db.Pipeline, bool, error) {
	return nil, false, errors.New("pipeline lookup failed")
}

type pipelineDelegateHandler struct {
	IsCalled          bool
	ContextPipelineDB db.Pipeline
}

func (handler *pipelineDelegateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.IsCalled = true
	handler.ContextPipelineDB = r.Context().Value(auth.PipelineContextKey).(db.Pipeline)
}
