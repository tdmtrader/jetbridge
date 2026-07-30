package buildserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestCreateBuildRejectsBrokerFieldsInRawPlan(t *testing.T) {
	tests := []struct {
		name string
		plan atc.Plan
	}{
		{
			name: "broker authority",
			plan: atc.Plan{Agent: &atc.AgentPlan{
				BrokerAuthority: []atc.AgentBrokerProfile{{FunctionID: "agent"}},
			}},
		},
		{
			name: "broker MCP marker",
			plan: atc.Plan{Agent: &atc.AgentPlan{
				Env: map[string]string{"CONCOURSE_AGENT_BROKER_MCP": "1"},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.plan)
			if err != nil {
				t.Fatal(err)
			}
			team := new(dbfakes.FakeTeam)
			server := buildserver.NewServer(lagertest.NewTestLogger("test"), "", nil, nil, nil)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/builds", bytes.NewReader(payload))

			server.CreateBuild(team).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if team.CreateStartedBuildCallCount() != 0 {
				t.Fatal("CreateStartedBuild was called for a raw broker plan")
			}
		})
	}
}
