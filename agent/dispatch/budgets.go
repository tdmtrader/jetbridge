package dispatch

import (
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/workflow"
)

// TicketGetter is the subset of tickets.Store the budget resolver reads.
type TicketGetter interface {
	Get(id int) (*tickets.Ticket, bool, error)
}

// TicketBudgets implements budget.TicketBudgets (§2.7) with the real
// "tickets.budget_usd ?? workflow default ticket_usd" rule the wave-1
// NoTicketBudgets stub stood in for (§2.8.2). Version resolution matches
// DispatchOne: a pinned ticket reads its FROZEN definition, an unpinned
// one reads live (only possible pre-dispatch; DispatchOne pins at claim).
type TicketBudgets struct {
	tickets   TicketGetter
	workflows WorkflowResolver
}

var _ budget.TicketBudgets = (*TicketBudgets)(nil)

func NewTicketBudgets(tg TicketGetter, wf WorkflowResolver) *TicketBudgets {
	return &TicketBudgets{tickets: tg, workflows: wf}
}

// BudgetUSD returns the effective ticket budget. found=false means
// UNCAPPED (unknown ticket, workflow-less ticket, unresolvable or
// default-less workflow). Store errors PROPAGATE so the Checker's callers
// keep their fail-closed semantics (agent_step.go:299-313) instead of
// silently treating a broken budget read as "no cap".
func (b *TicketBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	t, ok, err := b.tickets.Get(ticketID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	if t.BudgetUSD != nil && *t.BudgetUSD > 0 {
		return *t.BudgetUSD, true, nil
	}
	if t.WorkflowName == "" {
		return 0, false, nil
	}

	var def *workflow.Definition
	var found bool
	if t.WorkflowVersion != nil {
		def, found, err = b.workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	} else {
		def, found, err = b.workflows.Live(t.WorkflowName)
	}
	if err != nil {
		return 0, false, err
	}
	if !found {
		// Dangling workflow ref: dispatch errors this ticket loudly
		// elsewhere; budget-wise it is uncapped, not broken.
		return 0, false, nil
	}
	if def.Config.Budget.TicketUSD > 0 {
		return def.Config.Budget.TicketUSD, true, nil
	}
	return 0, false, nil
}
