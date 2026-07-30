package auditor_test

import (
	"net/http"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

func TestAgentNodeRoutesAreSystemAuditActions(t *testing.T) {
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
		atc.ListAgentNodeConsumers,
		atc.UpgradeAgentNodeConsumers,
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := lagertest.NewTestLogger("agent-node-auditor")
	aud := auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger)
	for _, route := range routes {
		aud.Audit(route, "alice", req)
	}
	logs := logger.Logs()
	if len(logs) != len(routes) {
		t.Fatalf("system audit emitted %d logs, want %d", len(logs), len(routes))
	}
	for index, route := range routes {
		if logs[index].Data["action"] != route {
			t.Errorf("log %d action = %v, want %q", index, logs[index].Data["action"], route)
		}
	}

	disabledLogger := lagertest.NewTestLogger("agent-node-auditor-disabled")
	disabled := auditor.NewAuditor(true, true, true, true, true, false, true, true, true, disabledLogger)
	for _, route := range routes {
		disabled.Audit(route, "alice", req)
	}
	if len(disabledLogger.Logs()) != 0 {
		t.Fatal("agent node actions were classified outside the system audit bucket")
	}
}
