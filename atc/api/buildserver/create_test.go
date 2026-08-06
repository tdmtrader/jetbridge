package buildserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Broker fields are the agent broker's to set, never a caller's. A raw plan
// carrying them must be rejected before it reaches the database -- previously
// asserted with CreateStartedBuildCallCount() == 0 on a fake, now asserted by
// the team having no builds at all.
var _ = DescribeTable("CreateBuild rejects broker fields in a raw plan",
	func(plan atc.Plan) {
		team := createTeam("some-team")

		payload, err := json.Marshal(plan)
		Expect(err).NotTo(HaveOccurred())

		server := buildserver.NewServer(lagertest.NewTestLogger("test"), "", nil, nil, nil)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/builds", bytes.NewReader(payload))

		server.CreateBuild(team).ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))

		builds, _, err := team.Builds(db.Page{Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(builds).To(BeEmpty(), "a build was created for a raw broker plan")
	},
	Entry("broker authority", atc.Plan{Agent: &atc.AgentPlan{
		BrokerAuthority: []atc.AgentBrokerProfile{{FunctionID: "agent"}},
	}}),
	Entry("broker MCP marker", atc.Plan{Agent: &atc.AgentPlan{
		Env: map[string]string{"CONCOURSE_AGENT_BROKER_MCP": "1"},
	}}),
	Entry("dynamic across broker field", atc.Plan{Across: &atc.AcrossPlan{
		Vars: []atc.AcrossVar{{Var: "field", Values: []string{"broker_authority"}}},
		SubStepTemplate: `id: agent
agent:
  ((.:field)):
  - function_id: agent`,
	}}),
)
