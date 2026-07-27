package buildserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestGetBuildPlanServesTheStoredPublicPlan(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","do":[{"id":"8/task","task":{"name":"build"}}]}`)
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.SchemaReturns("exec.v2")
	build.PublicPlanReturns(&raw)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"plan":{"id":"0","do":[{"id":"8/task","task":{"name":"build"}}]}`) {
		t.Fatalf("plan not served verbatim: %s", body)
	}
	if !strings.Contains(body, `"schema":"exec.v2"`) {
		t.Fatalf("missing schema: %s", body)
	}
}

func TestGetBuildPlanReturnsNotFoundWithoutAPlan(t *testing.T) {
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(false)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestGetBuildPlanPreservesNilPlan(t *testing.T) {
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.SchemaReturns("exec.v1")
	build.PublicPlanReturns(nil)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"plan":null`) {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func requestBuildPlan(t *testing.T, build *dbfakes.FakeBuildForAPI) *httptest.ResponseRecorder {
	t.Helper()

	server := buildserver.NewServer(lagertest.NewTestLogger("test"), "", nil, nil, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/builds/1/plan", nil)
	server.GetBuildPlan(build).ServeHTTP(response, request)

	return response
}
