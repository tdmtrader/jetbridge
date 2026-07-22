package tickets_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot/contracts"
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

func TestMemoryStoreIncrementsRevisionForEverySuccessfulMutation(t *testing.T) {
	s := tickets.NewMemoryStore()
	id, err := s.Create(newTicket())
	if err != nil {
		t.Fatal(err)
	}

	wantRevision := int64(1)
	assertRevision := func(operation string) {
		t.Helper()
		got, found, err := s.Get(id)
		if err != nil || !found || got.Revision != wantRevision {
			t.Fatalf("%s revision = (%+v, %t, %v), want %d", operation, got, found, err, wantRevision)
		}
	}
	mutated := func(operation string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		wantRevision++
		assertRevision(operation)
	}

	assertRevision("create")
	title := "updated"
	mutated("update", s.Update(id, tickets.Update{Title: &title}))
	mutated("transition", s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}))
	_, err = s.SubmitSpec(id, tickets.Spec{Title: "spec", Body: "body", SubmittedBy: "agent"})
	mutated("submit spec", err)
	planVersion, err := s.SubmitPlan(id, []tickets.Task{{Title: "task"}})
	mutated("submit plan", err)
	mutated("update task status", s.UpdateTaskStatus(id, planVersion, 1, tickets.TaskInProgress))
	mutated("append task note", s.AppendTaskNote(id, planVersion, 1, "progress"))
	_, err = s.UpdateActiveTask(id, 1, tickets.TaskDone, "finished")
	mutated("update active task", err)
	commentID, err := s.AppendComment(id, tickets.Comment{Body: "Ship it?", CreatedBy: "agent"})
	mutated("append comment", err)
	mutated("answer comment", s.AnswerComment(id, commentID, "yes", "alice"))

	beforeFailure := wantRevision
	if err := s.AnswerComment(id, commentID, "again", "bob"); !errors.Is(err, tickets.ErrCommentAnswered) {
		t.Fatalf("second answer = %v, want ErrCommentAnswered", err)
	}
	wantRevision = beforeFailure
	assertRevision("failed second answer")
}

func TestMemoryStoreCapturesLatestNestedStateAndFirstWriterAnswer(t *testing.T) {
	s := tickets.NewMemoryStore()
	version := 3
	id, err := s.Create(&tickets.Ticket{
		Title: "upgrade", Body: "upgrade the dependency", Origin: "jira", ExternalRef: "ENG-42", Repo: "repo",
		WorkflowName: "version-upgrade", WorkflowVersion: &version, CreatedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSpec(id, tickets.Spec{
		Title: "old", Body: "old", SubmittedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSpec(id, tickets.Spec{
		Title: "current", Body: "current spec", AcceptanceCriteria: []string{"tests pass"}, SubmittedBy: "bob",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitPlan(id, []tickets.Task{{Title: "old plan"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitPlan(id, []tickets.Task{{Title: "bump version"}, {Title: "run tests"}}); err != nil {
		t.Fatal(err)
	}
	commentID, err := s.AppendComment(id, tickets.Comment{Body: "May I update the lockfile?", CreatedBy: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerComment(id, commentID, "yes", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}

	captured, found, err := s.CaptureRevision(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("CaptureRevision = (%+v, %t, %v)", captured, found, err)
	}
	var document contracts.WorkItemDocument
	if err := json.Unmarshal(captured.Document, &document); err != nil {
		t.Fatal(err)
	}
	if document.Adapter != "jira" || document.ExternalID != "ENG-42" || document.State != "queued" ||
		document.Workflow == nil || document.Workflow.Name != "version-upgrade" || document.Spec == nil || document.Spec.Revision != "2" ||
		document.Plan == nil || document.Plan.Revision != "2" || len(document.Comments) != 1 {
		t.Fatalf("captured document = %+v", document)
	}
	var comment struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
	}
	if err := json.Unmarshal([]byte(document.Comments[0].Content), &comment); err != nil {
		t.Fatal(err)
	}
	if comment.Answer != "yes" || comment.AnsweredBy != "alice" {
		t.Fatalf("captured answer = %+v", comment)
	}
}
