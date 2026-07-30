package agentchildexecutions_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/db"
)

func TestInspectionIsReadOnlyTeamBoundAndDoesNotExposeInputDigest(t *testing.T) {
	store := &fakeStore{execution: db.AgentChildExecution{
		ID:                "c34a6e95-2e3a-45b0-b3f0-30c4e09acb7d",
		ExecutionIdentity: broker.ExecutionIdentity{TeamID: 1, InputDigest: "sha256:" + strings.Repeat("a", 64), Tool: broker.ToolConsultAgent, ProfileID: "frozen", ProfileDigest: "sha256:" + strings.Repeat("b", 64)},
		State:             broker.ExecutionSucceeded,
	}}
	handler := agentchildexecutions.NewInspectionHandler(store, 1)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/teams/main/agent-child-executions/"+store.execution.ID+"?%3Aexecution_id="+store.execution.ID, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), store.execution.InputDigest) {
		t.Fatalf("inspection status=%d body=%s", response.Code, response.Body.String())
	}
	foreign := httptest.NewRecorder()
	agentchildexecutions.NewInspectionHandler(store, 2).ServeHTTP(foreign, request)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign team status = %d", foreign.Code)
	}
	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodPost, request.URL.String(), nil))
	if method.Code != http.StatusNotFound {
		t.Fatalf("write method status = %d", method.Code)
	}
}
