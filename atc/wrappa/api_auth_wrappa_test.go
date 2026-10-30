package wrappa_test

import (
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

var _ = Describe("APIAuthWrappa", func() {
	var (
		checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
		checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
		checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
		checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
	)

	BeforeEach(func() {
		checkPipelineAccessHandlerFactory = auth.NewCheckPipelineAccessHandlerFactory(nil)
		checkBuildReadAccessHandlerFactory = auth.NewCheckBuildReadAccessHandlerFactory(nil)
		checkBuildWriteAccessHandlerFactory = auth.NewCheckBuildWriteAccessHandlerFactory(nil)
		checkWorkerTeamAccessHandlerFactory = auth.NewCheckWorkerTeamAccessHandlerFactory(nil)
	})

	Describe("Wrap", func() {
		It("uses public template-pipeline access for list and detail", func() {
			// This fails if durable history starts requiring member authorization or resolves an instance instead of the base.
			pipeline := &authRoutePipeline{public: true}
			team := &authRouteTeam{pipeline: pipeline}
			teamFactory := &authRouteTeamFactory{team: team}
			accessFactory := &authRouteAccessFactory{team: team}
			wrapped := wrappa.NewAPIAuthWrappa(
				auth.NewCheckPipelineAccessHandlerFactory(teamFactory),
				checkBuildReadAccessHandlerFactory,
				checkBuildWriteAccessHandlerFactory,
				checkWorkerTeamAccessHandlerFactory,
			).Wrap(rata.Handlers{
				atc.ListPipelineRuns: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
				atc.GetPipelineRun:   http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			})

			for _, action := range []string{atc.ListPipelineRuns, atc.GetPipelineRun} {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				query := req.URL.Query()
				query.Set(":team_name", "team")
				query.Set(":pipeline_name", "template")
				req.URL.RawQuery = query.Encode()
				accessor.NewHandler(lager.NewLogger("test"), action, wrapped[action], accessFactory, nil).ServeHTTP(httptest.NewRecorder(), req)
			}

			Expect(teamFactory.findNames).To(Equal([]string{"team", "team"}))
			Expect(team.refs).To(Equal([]atc.PipelineRef{{Name: "template"}, {Name: "template"}}))
		})

		It("uses member authorization for creation without public-pipeline lookup", func() {
			// This fails if create is accidentally wrapped as a publicly readable pipeline route.
			// test:user is a member of "team" and of nothing else, so the
			// handler is only served if it asked for member authorization on
			// exactly that team.
			accessFactory := &authRouteAccessFactory{team: &authRouteTeam{}, user: "user"}
			served := false
			wrapped := wrappa.NewAPIAuthWrappa(
				checkPipelineAccessHandlerFactory,
				checkBuildReadAccessHandlerFactory,
				checkBuildWriteAccessHandlerFactory,
				checkWorkerTeamAccessHandlerFactory,
			).Wrap(rata.Handlers{atc.CreatePipelineRunV2: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true })})
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			query := req.URL.Query()
			query.Set(":team_name", "team")
			req.URL.RawQuery = query.Encode()
			accessor.NewHandler(lager.NewLogger("test"), atc.CreatePipelineRunV2, wrapped[atc.CreatePipelineRunV2], accessFactory, nil).ServeHTTP(httptest.NewRecorder(), req)

			Expect(served).To(BeTrue())
			Expect(accessFactory.roles).To(Equal([]string{accessor.MemberRole}))
		})

		It("classifies all run routes", func() {
			// This fails if public template history becomes member-only, or creation inherits public access.
			inputHandlers := rata.Handlers{
				atc.CreatePipelineRunV2: &stupidHandler{},
				atc.ListPipelineRuns:    &stupidHandler{},
				atc.GetPipelineRun:      &stupidHandler{},
			}
			matched := 0
			for name := range inputHandlers {
				switch name {
				case atc.CreatePipelineRunV2, atc.ListPipelineRuns, atc.GetPipelineRun:
					matched++
				}
			}
			Expect(matched).To(Equal(3))
			Expect(func() {
				wrappa.NewAPIAuthWrappa(
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
				).Wrap(inputHandlers)
			}).NotTo(Panic())
		})

		It("handles each route", func() {
			inputHandlers := rata.Handlers{}

			for _, route := range atc.Routes {
				inputHandlers[route.Name] = &stupidHandler{}
			}
			Expect(func() {
				wrappa.NewAPIAuthWrappa(
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
				).Wrap(inputHandlers)
			}).NotTo(Panic())
		})
	})
})

type authRouteTeamFactory struct {
	db.TeamFactory
	team      db.Team
	findNames []string
}

func (f *authRouteTeamFactory) FindTeam(name string) (db.Team, bool, error) {
	f.findNames = append(f.findNames, name)
	return f.team, true, nil
}

type authRouteTeam struct {
	db.Team
	pipeline db.Pipeline
	refs     []atc.PipelineRef
}

func (t *authRouteTeam) Pipeline(ref atc.PipelineRef) (db.Pipeline, bool, error) {
	t.refs = append(t.refs, ref)
	return t.pipeline, true, nil
}

func (t *authRouteTeam) Name() string { return "team" }
func (t *authRouteTeam) Admin() bool  { return false }
func (t *authRouteTeam) Auth() atc.TeamAuth {
	return atc.TeamAuth{accessor.MemberRole: {"users": {"test:user"}}}
}

// authRouteAccessFactory builds the real accessor over the route's team for
// the role each route asked, as the connector "test" vouching for user, or
// for nobody when user is empty.
type authRouteAccessFactory struct {
	team  db.Team
	user  string
	roles []string
}

func (f *authRouteAccessFactory) Create(_ *http.Request, role string) (accessor.Access, error) {
	f.roles = append(f.roles, role)
	verification := accessor.Verification{}
	if f.user != "" {
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: map[string]any{
			"federated_claims": map[string]any{"connector_id": "test", "user_id": f.user},
		}}
	}
	return accessor.NewAccessor(verification, role, "sub", nil, []db.Team{f.team}, nil), nil
}

type authRoutePipeline struct {
	db.Pipeline
	public bool
}

func (p *authRoutePipeline) Public() bool { return p.public }
