package auditor_test

import (
	"net/http"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

func TestAgentWorkflowWaitRoutesAreSystemAuditActions(t *testing.T) {
	routes := []string{atc.ListAgentWorkflowRunWaits, atc.ResolveAgentWorkflowRunWait}
	request, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := lagertest.NewTestLogger("agent-workflow-wait-auditor")
	aud := auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger)
	for _, route := range routes {
		aud.Audit(route, "alice", request)
	}
	if len(logger.Logs()) != len(routes) {
		t.Fatalf("system audit emitted %d logs, want %d", len(logger.Logs()), len(routes))
	}
}
