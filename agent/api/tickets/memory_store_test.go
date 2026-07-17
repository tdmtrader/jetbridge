package tickets_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func newTicket() *tickets.Ticket {
	return &tickets.Ticket{
		Title: "fix flaky spec", Body: "it flakes", Origin: "web",
		Repo: "tdmtrader/concourse", TargetBranch: "main",
		UserName: "tdm", CreatedBy: "tdm",
	}
}

func TestMemoryStoreCreateGetListUpdate(t *testing.T) {
	s := tickets.NewMemoryStore()

	id, err := s.Create(newTicket())
	if err != nil || id != 1 {
		t.Fatalf("Create = %d, %v; want 1, nil", id, err)
	}

	got, found, err := s.Get(id)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v, %v", got, found, err)
	}
	if got.State != tickets.StateDraft || got.Title != "fix flaky spec" || got.CreatedAt == 0 {
		t.Errorf("unexpected ticket: %+v", got)
	}

	newTitle := "fix the flaky spec"
	budget := 5.0
	if err := s.Update(id, tickets.Update{Title: &newTitle, BudgetUSD: &budget}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _, _ = s.Get(id)
	if got.Title != newTitle || got.BudgetUSD == nil || *got.BudgetUSD != 5.0 {
		t.Errorf("update not applied: %+v", got)
	}

	all, err := s.List(tickets.ListFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("List = %d, %v", len(all), err)
	}
	none, _ := s.List(tickets.ListFilter{State: tickets.StateQueued})
	if len(none) != 0 {
		t.Errorf("state filter leaked: %+v", none)
	}
}

func TestMemoryStoreTransition(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, _ := s.Create(newTicket())

	if err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatalf("draft->queued: %v", err)
	}
	err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrStaleTransition) {
		t.Errorf("stale from-state: got %v, want ErrStaleTransition", err)
	}
	err = s.Transition(id, tickets.StateQueued, tickets.StateMerged, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrInvalidTransition) {
		t.Errorf("illegal edge: got %v, want ErrInvalidTransition", err)
	}
	err = s.Transition(999, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	if !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v, want ErrTicketNotFound", err)
	}
}

func TestMemoryStoreSpecsAndPlans(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, _ := s.Create(newTicket())

	v, err := s.SubmitSpec(id, tickets.Spec{Title: "spec", Body: "b", SubmittedBy: "run-1-platform"})
	if err != nil || v != 1 {
		t.Fatalf("SubmitSpec = %d, %v", v, err)
	}
	v, _ = s.SubmitSpec(id, tickets.Spec{Title: "spec2", Body: "b2"})
	if v != 2 {
		t.Fatalf("second SubmitSpec = %d, want 2", v)
	}
	latest, found, _ := s.LatestSpec(id)
	if !found || latest.Title != "spec2" || latest.Version != 2 {
		t.Errorf("LatestSpec = %+v, %v", latest, found)
	}

	pv, err := s.SubmitPlan(id, []tickets.Task{{Title: "one"}, {Title: "two"}})
	if err != nil || pv != 1 {
		t.Fatalf("SubmitPlan = %d, %v", pv, err)
	}
	pv, _ = s.SubmitPlan(id, []tickets.Task{{Title: "redo"}})
	if pv != 2 {
		t.Fatalf("second SubmitPlan = %d, want 2", pv)
	}
	active, _ := s.ActivePlan(id)
	if len(active) != 1 || active[0].Title != "redo" || active[0].Ordering != 1 ||
		active[0].Status != tickets.TaskPending {
		t.Errorf("ActivePlan = %+v", active)
	}

	if err := s.UpdateTaskStatus(id, 2, 1, tickets.TaskDone); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if err := s.AppendTaskNote(id, 2, 1, "was easy"); err != nil {
		t.Fatalf("AppendTaskNote: %v", err)
	}
	active, _ = s.ActivePlan(id)
	if active[0].Status != tickets.TaskDone || active[0].Detail != "> was easy" {
		t.Errorf("task after update = %+v", active[0])
	}
	err = s.UpdateTaskStatus(id, 2, 99, tickets.TaskDone)
	if !errors.Is(err, tickets.ErrTaskNotFound) {
		t.Errorf("missing task: got %v, want ErrTaskNotFound", err)
	}
}
