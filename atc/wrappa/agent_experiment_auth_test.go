package wrappa_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

func TestAgentExperimentRoutesUseHumanMainTeamAuthorization(t *testing.T) {
	routes := []string{
		atc.CreateAgentExperiment, atc.ListAgentExperiments, atc.GetAgentExperiment,
		atc.UpdateAgentExperiment, atc.ValidateAgentExperiment, atc.StartAgentExperiment,
		atc.CancelAgentExperiment, atc.ListAgentExperimentCells, atc.GetAgentExperimentCell,
		atc.GetAgentExperimentScorecard,
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			delegateHit := false
			delegate := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegateHit = true
				w.WriteHeader(http.StatusOK)
			})
			// These routes authorize against the team named in the request, so the
			// auth chain never reaches a db factory. nil is deliberate: if that ever
			// changes, the test panics instead of quietly reading a zero value.
			wrapped := wrappa.NewAPIAuthWrappa(
				auth.NewCheckPipelineAccessHandlerFactory(nil),
				auth.NewCheckBuildReadAccessHandlerFactory(nil),
				auth.NewCheckBuildWriteAccessHandlerFactory(nil),
				auth.NewCheckWorkerTeamAccessHandlerFactory(nil),
			).Wrap(rata.Handlers{route: delegate})[route]

			accessFactory := new(accessorfakes.FakeAccessFactory)
			access := new(accessorfakes.FakeAccess)
			access.IsAuthenticatedReturns(true)
			access.IsAuthorizedReturns(true)
			accessFactory.CreateReturns(access, nil)
			handler := accessor.NewHandler(
				lagertest.NewTestLogger("experiment-auth"), route, wrapped,
				accessFactory, new(auditorfakes.FakeAuditor), map[string]string{},
			)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test", nil))
			if recorder.Code != http.StatusOK || !delegateHit {
				t.Fatalf("authorized status/delegate = %d/%t, want 200/true", recorder.Code, delegateHit)
			}
			if access.IsAuthorizedCallCount() != 1 || access.IsAuthorizedArgsForCall(0) != atc.DefaultTeamName {
				t.Fatalf("route did not authorize against %q", atc.DefaultTeamName)
			}
		})
	}
}
