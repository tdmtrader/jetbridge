package dispatch_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
)

type budgetTicketGetter struct {
	rows map[int]tickets.Ticket
	err  error
}

func (g budgetTicketGetter) Get(id int) (*tickets.Ticket, bool, error) {
	if g.err != nil {
		return nil, false, g.err
	}
	t, ok := g.rows[id]
	if !ok {
		return nil, false, nil
	}
	return &t, true, nil
}

func TestTicketBudgetsReturnsPositiveExplicitBudget(t *testing.T) {
	budget20 := 20.0
	v4 := 4
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		7: {ID: 7, WorkflowName: "standard-dev", BudgetUSD: &budget20},
		8: {ID: 8, WorkflowName: "standard-dev", WorkflowVersion: &v4, BudgetUSD: &budget20},
	}}
	tb := dispatch.NewTicketBudgets(getter)

	got, found, err := tb.BudgetUSD(7)
	if err != nil || !found || got != 20.0 {
		t.Errorf("live metadata: got=%v found=%v err=%v, want 20/true/nil", got, found, err)
	}
	got, found, err = tb.BudgetUSD(8)
	if err != nil || !found || got != 20.0 {
		t.Errorf("pinned metadata: got=%v found=%v err=%v, want 20/true/nil", got, found, err)
	}
}

func TestTicketBudgetsDoesNotReadWorkflowDefaults(t *testing.T) {
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		42: {ID: 42, WorkflowName: "small-fix"},
	}}

	amount, found, err := dispatch.NewTicketBudgets(getter).BudgetUSD(42)
	if err != nil || found || amount != 0 {
		t.Fatalf("amount=%v found=%v err=%v, want 0/false/nil", amount, found, err)
	}
}

func TestTicketBudgetsTreatsNonPositiveAndAbsentBudgetsAsUncapped(t *testing.T) {
	zero := 0.0
	negative := -1.0
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		1: {ID: 1},
		2: {ID: 2, BudgetUSD: &zero},
		3: {ID: 3, BudgetUSD: &negative},
	}}
	tb := dispatch.NewTicketBudgets(getter)

	for _, ticketID := range []int{1, 2, 3, 99} {
		amount, found, err := tb.BudgetUSD(ticketID)
		if err != nil || found || amount != 0 {
			t.Errorf("ticket %d: amount=%v found=%v err=%v, want 0/false/nil", ticketID, amount, found, err)
		}
	}
}

func TestTicketBudgetsPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("db down")
	tb := dispatch.NewTicketBudgets(budgetTicketGetter{err: boom})
	if _, _, err := tb.BudgetUSD(7); !errors.Is(err, boom) {
		t.Fatalf("store errors must propagate (fail-open would bypass step fail-closed), got %v", err)
	}
}
