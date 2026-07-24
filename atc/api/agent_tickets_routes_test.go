package api_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestRetiredTicketRoutesAbsent guards the v3-only cleanup (outcome-metrics
// Task 4): the ticket-specific disposition/outcome/diff routes and the
// ticket-scoped metrics route must no longer be registered. Their function
// moved to the generic workflow-run outcome + metrics endpoints and the
// snapshot-projection endpoints, which the durable workflow run — not the
// ticket work-item adapter — now owns.
//
// The retired names are string literals on purpose: the route constants are
// deleted, so referencing atc.SetAgentTicketDisposition (etc.) would not
// compile.
func TestRetiredTicketRoutesAbsent(t *testing.T) {
	registered := make(map[string]bool, len(atc.Routes))
	for _, route := range atc.Routes {
		registered[route.Name] = true
	}

	for _, retired := range []string{
		"SetAgentTicketDisposition",
		"GetAgentTicketOutcome",
		"GetAgentTicketDiff",
		"ListAgentRunMetrics",
	} {
		if registered[retired] {
			t.Errorf("route %q must not be registered after ticket API consolidation", retired)
		}
	}
}
