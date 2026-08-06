package pipelineserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Broker fields are the agent broker's to set, never a caller's. A raw plan
// carrying them must be rejected before it reaches the database -- this was
// previously asserted with CreateStartedBuildCallCount() == 0 on a fake, and is
// now asserted by there being no build row.
var _ = DescribeTable("CreateBuild rejects broker fields in a raw plan",
	func(plan atc.Plan) {
		pipeline := createPipeline(createTeam("some-team"), "some-pipeline")

		payload, err := json.Marshal(plan)
		Expect(err).NotTo(HaveOccurred())

		server := pipelineserver.NewServer(lagertest.NewTestLogger("test"), teamFactory, nil, "")
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost, "/api/v1/pipelines/pipeline/builds", bytes.NewReader(payload),
		)

		server.CreateBuild(pipeline).ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))

		builds, _, err := pipeline.Builds(db.Page{Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(builds).To(BeEmpty(), "a build was created for a raw broker plan")
	},
	Entry("broker authority", atc.Plan{Agent: &atc.AgentPlan{
		BrokerAuthority: []atc.AgentBrokerProfile{{FunctionID: "agent"}},
	}}),
	Entry("broker MCP marker", atc.Plan{Agent: &atc.AgentPlan{
		Env: map[string]string{"CONCOURSE_AGENT_BROKER_MCP": "1"},
	}}),
)
