package auditor_test

import (
	"net/http"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

func TestAgentNodeRoutesAreSystemAuditActions(t *testing.T) {
	// Derived from the route table rather than hand-listed. A hand-listed
	// version of this test silently stopped covering CancelAgentNodeRun the
	// moment that route was added — which is precisely the claim the test
	// exists to make, so it has to enumerate itself.
	var routes []string
	for _, route := range atc.Routes {
		if strings.HasPrefix(route.Path, "/api/v1/agent/nodes") {
			routes = append(routes, route.Name)
		}
	}
	if len(routes) == 0 {
		t.Fatal("no agent node routes found in the route table")
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
