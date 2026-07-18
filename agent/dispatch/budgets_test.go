package dispatch_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
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

func budgetWorkflows() *fakeWorkflows {
	return &fakeWorkflows{byName: map[string]*workflow.Definition{
		"standard-dev": {Name: "standard-dev", Version: 4, Live: true,
			Config: workflow.Config{Name: "standard-dev", Budget: workflow.Budget{TicketUSD: 15}}},
	}}
}

func TestTicketBudgetsTicketOverrideWinsOverWorkflowDefault(t *testing.T) {
	budget20 := 20.0
	v4 := 4
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		7: {ID: 7, WorkflowName: "standard-dev", BudgetUSD: &budget20},
		8: {ID: 8, WorkflowName: "standard-dev"},                       // live resolution
		9: {ID: 9, WorkflowName: "standard-dev", WorkflowVersion: &v4}, // pinned resolution
	}}
	tb := dispatch.NewTicketBudgets(getter, budgetWorkflows())

	got, found, err := tb.BudgetUSD(7)
	if err != nil || !found || got != 20.0 {
		t.Errorf("ticket override: got=%v found=%v err=%v, want 20/true/nil", got, found, err)
	}
	got, found, err = tb.BudgetUSD(8)
	if err != nil || !found || got != 15.0 {
		t.Errorf("live workflow default: got=%v found=%v err=%v, want 15/true/nil", got, found, err)
	}
	got, found, err = tb.BudgetUSD(9)
	if err != nil || !found || got != 15.0 {
		t.Errorf("pinned workflow default: got=%v found=%v err=%v, want 15/true/nil", got, found, err)
	}
}

func TestTicketBudgetsUncappedCases(t *testing.T) {
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		5: {ID: 5}, // no workflow, no budget
	}}
	tb := dispatch.NewTicketBudgets(getter, budgetWorkflows())

	if _, found, err := tb.BudgetUSD(99); err != nil || found {
		t.Errorf("unknown ticket must be uncapped: found=%v err=%v", found, err)
	}
	if _, found, err := tb.BudgetUSD(5); err != nil || found {
		t.Errorf("workflow-less ticket must be uncapped: found=%v err=%v", found, err)
	}
}

func TestTicketBudgetsPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("db down")
	tb := dispatch.NewTicketBudgets(budgetTicketGetter{err: boom}, budgetWorkflows())
	if _, _, err := tb.BudgetUSD(7); !errors.Is(err, boom) {
		t.Fatalf("store errors must propagate (fail-open would bypass step fail-closed), got %v", err)
	}
}
