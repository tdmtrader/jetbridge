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
	"github.com/concourse/concourse/atc/legacyplan"
)

func TestGetBuildPlanRewritesCompletedHistoricalHarvest(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","do":[{"id":"8/harvest","harvest":{"name":"push-branch","repo":"private/repo"}}]}`)
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.IsRunningReturns(false)
	build.SchemaReturns("exec.v1")
	build.PublicPlanReturns(&raw)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"retired_step":{"kind":"harvest","name":"push-branch"}`) {
		t.Fatalf("missing retired step: %s", body)
	}
	if strings.Contains(body, `"harvest":`) {
		t.Fatalf("raw harvest leaked: %s", body)
	}
}

func TestGetBuildPlanRejectsRunningHistoricalHarvest(t *testing.T) {
	raw := json.RawMessage(`{"id":"8/harvest","harvest":{"name":"push-branch"}}`)
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.IsRunningReturns(true)
	build.PublicPlanReturns(&raw)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, legacyplan.ErrActiveHarvestPlan.Error()) {
		t.Fatalf("missing retired-plan error: %s", body)
	}
	if strings.Contains(body, `"harvest":`) {
		t.Fatalf("raw harvest leaked: %s", body)
	}
}

func TestGetBuildPlanRejectsMalformedHistoricalJSON(t *testing.T) {
	raw := json.RawMessage(`{"id":`)
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.IsRunningReturns(false)
	build.PublicPlanReturns(&raw)

	response := requestBuildPlan(t, build)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestGetBuildPlanPreservesNilPlan(t *testing.T) {
	build := new(dbfakes.FakeBuildForAPI)
	build.HasPlanReturns(true)
	build.IsRunningReturns(false)
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
