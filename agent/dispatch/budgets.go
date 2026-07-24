package dispatch

import (
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
)

// TicketGetter is the subset of tickets.Store the budget resolver reads.
type TicketGetter interface {
	Get(id int) (*tickets.Ticket, bool, error)
}

// TicketBudgets implements budget.TicketBudgets using an explicit positive
// tickets.budget_usd. Tickets without one are uncapped.
type TicketBudgets struct {
	tickets TicketGetter
}

var _ budget.TicketBudgets = (*TicketBudgets)(nil)

func NewTicketBudgets(tg TicketGetter) *TicketBudgets {
	return &TicketBudgets{tickets: tg}
}

// BudgetUSD returns the effective ticket budget. found=false means
// UNCAPPED (unknown ticket or absent/non-positive explicit budget). Store
// errors PROPAGATE so the Checker's callers keep their fail-closed semantics
// (agent_step.go:299-313) instead of silently treating a broken budget read
// as "no cap".
func (b *TicketBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	ticket, found, err := b.tickets.Get(ticketID)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	if ticket.BudgetUSD != nil && *ticket.BudgetUSD > 0 {
		return *ticket.BudgetUSD, true, nil
	}
	return 0, false, nil
}
