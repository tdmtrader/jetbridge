package wrappa_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

func TestAgentWorkflowRunRoutesUseHumanMainTeamAuthorization(t *testing.T) {
	routes := []string{
		atc.CreateAgentWorkflowRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
	}

	teamFactory := new(dbfakes.FakeTeamFactory)
	workerFactory := new(dbfakes.FakeWorkerFactory)
	buildFactory := new(dbfakes.FakeBuildFactory)
	principalStore := principals.NewMemoryStore()
	_, principalToken, err := principalStore.Create(principals.CreateSpec{
		Name: "ticket-writer", Scopes: []string{principals.ScopeTicketsWrite},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			delegateHit := false
			delegate := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegateHit = true
				w.WriteHeader(http.StatusOK)
			})
			wrapped := wrappa.NewAPIAuthWrappa(
				auth.NewCheckPipelineAccessHandlerFactory(teamFactory),
				auth.NewCheckBuildReadAccessHandlerFactory(buildFactory),
				auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory),
				auth.NewCheckWorkerTeamAccessHandlerFactory(workerFactory),
				auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principalStore)),
			).Wrap(rata.Handlers{route: delegate})[route]

			serve := func(authenticated, authorized bool, authorization string) int {
				t.Helper()
				delegateHit = false
				accessFactory := new(accessorfakes.FakeAccessFactory)
				access := new(accessorfakes.FakeAccess)
				access.IsAuthenticatedReturns(authenticated)
				access.IsAuthorizedReturns(authorized)
				accessFactory.CreateReturns(access, nil)
				authenticatedHandler := accessor.NewHandler(
					lagertest.NewTestLogger("workflow-run-auth"),
					route,
					wrapped,
					accessFactory,
					new(auditorfakes.FakeAuditor),
					map[string]string{},
				)

				req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				recorder := httptest.NewRecorder()
				authenticatedHandler.ServeHTTP(recorder, req)
				if authorized {
					if access.IsAuthorizedCallCount() != 1 || access.IsAuthorizedArgsForCall(0) != atc.DefaultTeamName {
						t.Fatalf("route did not authorize against %q", atc.DefaultTeamName)
					}
				}
				return recorder.Code
			}

			if status := serve(false, false, ""); status != http.StatusUnauthorized || delegateHit {
				t.Fatalf("anonymous status = %d, delegateHit = %t; want 401, false", status, delegateHit)
			}
			if status := serve(true, false, ""); status != http.StatusForbidden || delegateHit {
				t.Fatalf("unauthorized human status = %d, delegateHit = %t; want 403, false", status, delegateHit)
			}
			if status := serve(false, false, "Bearer "+principalToken); status != http.StatusUnauthorized || delegateHit {
				t.Fatalf("bare principal status = %d, delegateHit = %t; want 401, false", status, delegateHit)
			}
			if status := serve(true, true, ""); status != http.StatusOK || !delegateHit {
				t.Fatalf("authorized human status = %d, delegateHit = %t; want 200, true", status, delegateHit)
			}
		})
	}
}
