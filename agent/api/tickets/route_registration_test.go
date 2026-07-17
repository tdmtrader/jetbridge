package tickets_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestTicketRoutesRegistered verifies the eight ticket-core routes are
// in the main ATC route table with the §4.2 methods and paths.
func TestTicketRoutesRegistered(t *testing.T) {
	required := []struct {
		name   string
		method string
		path   string
	}{
		{atc.ListAgentTickets, "GET", "/api/v1/agent/tickets"},
		{atc.CreateAgentTicket, "POST", "/api/v1/agent/tickets"},
		{atc.GetAgentTicket, "GET", "/api/v1/agent/tickets/:ticket_id"},
		{atc.UpdateAgentTicket, "PUT", "/api/v1/agent/tickets/:ticket_id"},
		{atc.TransitionAgentTicket, "PUT", "/api/v1/agent/tickets/:ticket_id/state"},
		{atc.SubmitAgentTicketSpec, "POST", "/api/v1/agent/tickets/:ticket_id/spec"},
		{atc.SubmitAgentTicketPlan, "POST", "/api/v1/agent/tickets/:ticket_id/plan"},
		{atc.UpdateAgentTicketTask, "PUT", "/api/v1/agent/tickets/:ticket_id/tasks/:ordering"},
	}

	for _, rr := range required {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method {
					t.Errorf("route %q: method = %s, want %s", rr.name, route.Method, rr.method)
				}
				if route.Path != rr.path {
					t.Errorf("route %q: path = %s, want %s", rr.name, route.Path, rr.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q (%s %s) not registered in atc.Routes", rr.name, rr.method, rr.path)
		}
	}
}
