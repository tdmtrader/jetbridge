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

func TestAgentNodeAndWorkflowRunRoutesUseHumanMainTeamAuthorization(t *testing.T) {
	routes := []string{
		atc.ListAgentNodes,
		atc.ListAgentNodeVersions,
		atc.GetAgentNodeVersion,
		atc.CreateAgentNodeVersion,
		atc.ReleaseAgentNodeVersion,
		atc.DeprecateAgentNodeVersion,
		atc.CreateAgentNodeRun,
		atc.ListAgentNodeRuns,
		atc.GetAgentNodeRun,
		atc.CancelAgentNodeRun,
		atc.ListAgentNodeConsumers,
		atc.UpgradeAgentNodeConsumers,
		atc.CreateAgentWorkflowRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRunOperationalStatusCounts,
		atc.GetAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
		atc.GetAgentWorkflowRunGraph,
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
			if status := serve(false, false, "Bearer cap1.7.s3cret"); status != http.StatusUnauthorized || delegateHit {
				t.Fatalf("retired bearer status = %d, delegateHit = %t; want 401, false", status, delegateHit)
			}
			if status := serve(true, true, ""); status != http.StatusOK || !delegateHit {
				t.Fatalf("authorized human status = %d, delegateHit = %t; want 200, true", status, delegateHit)
			}
		})
	}
}
