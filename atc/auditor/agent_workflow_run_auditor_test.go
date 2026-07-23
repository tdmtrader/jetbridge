package auditor_test

import (
	"net/http"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

func TestAgentWorkflowRunRoutesAreSystemAuditActions(t *testing.T) {
	routes := []string{
		atc.CreateAgentWorkflowRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRunOperationalStatusCounts,
		atc.GetAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
		atc.ListAgentWorkflowRunReviews,
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := lagertest.NewTestLogger("workflow-run-auditor")
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

	disabledLogger := lagertest.NewTestLogger("workflow-run-auditor-disabled")
	disabled := auditor.NewAuditor(true, true, true, true, true, false, true, true, true, disabledLogger)
	for _, route := range routes {
		disabled.Audit(route, "alice", req)
	}
	if len(disabledLogger.Logs()) != 0 {
		t.Fatal("workflow-run actions were classified outside the system audit bucket")
	}
}
