package dispatch_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

type fakeWorkflows struct {
	byName map[string]*workflow.Definition
}

func (f *fakeWorkflows) Live(name string) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	return d, ok, nil
}

func (f *fakeWorkflows) Get(name string, version int) (*workflow.Definition, bool, error) {
	d, ok := f.byName[name]
	if !ok || d.Version != version {
		return nil, false, nil
	}
	return d, true, nil
}

type fakeSaver struct {
	savedName string
	savedCfg  atc.Config
	id        int
	err       error
}

func (f *fakeSaver) SaveTemplate(name string, cfg atc.Config) (int, error) {
	f.savedName, f.savedCfg = name, cfg
	if f.err != nil {
		return 0, f.err
	}
	return f.id, nil
}

func smokeDefinition() *workflow.Definition {
	return &workflow.Definition{
		Name: "smoke", Version: 3, ContentHash: "abc123", Live: true,
		Config: workflow.Config{
			Name:         "smoke",
			SpecDelivery: "files",
			Defaults:     workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 5},
			Prompts:      map[string]string{"do": "Do it."},
			Steps: []workflow.Step{
				{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
			},
		},
	}
}

func dispatchDeps(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, *fakeSaver, *dbfakes.FakePipelineRunFactory) {
	t.Helper()
	store := tickets.NewMemoryStore()
	saver := &fakeSaver{id: 77}
	runs := new(dbfakes.FakePipelineRunFactory)
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(555)
	runs.CreateRunReturns(run, nil)

	deps := dispatch.Deps{
		Tickets:        store,
		Workflows:      &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": smokeDefinition()}},
		Templates:      saver,
		Runs:           runs,
		ATCExternalURL: "http://concourse.home",
		RepoBaseURL:    "https://github.com",
	}
	return deps, store, saver, runs
}

func queuedTicket(t *testing.T, store *tickets.MemoryStore, workflowName string) int {
	t.Helper()
	id, err := store.Create(&tickets.Ticket{
		Title: "fix X", Body: "details", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "main",
		WorkflowName: workflowName, UserName: "tdm", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatchOneHappyPath(t *testing.T) {
	deps, store, saver, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")

	res, err := dispatch.DispatchOne(deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	if res.RunID != 555 || res.PipelineName != fmt.Sprintf("agent-ticket-%d", id) {
		t.Errorf("result = %+v", res)
	}

	if saver.savedName != fmt.Sprintf("agent-ticket-%d", id) || !saver.savedCfg.Template {
		t.Errorf("template save wrong: name=%q template=%v", saver.savedName, saver.savedCfg.Template)
	}

	templateID, params, createdBy := runs.CreateRunArgsForCall(0)
	if templateID != 77 || params != nil || createdBy != "admin" {
		t.Errorf("CreateRun args = %d %v %q", templateID, params, createdBy)
	}

	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.PipelineRunID == nil || *got.PipelineRunID != 555 {
		t.Errorf("ticket after dispatch = %+v", got)
	}
	if got.WorkflowVersion == nil || *got.WorkflowVersion != 3 {
		t.Errorf("live workflow version must be frozen onto the ticket at dispatch, got %+v", got.WorkflowVersion)
	}
}

func TestDispatchOnePinnedVersion(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	pin := 3
	store.Update(id, tickets.Update{WorkflowVersion: &pin})

	if _, err := dispatch.DispatchOne(deps, id, "admin"); err != nil {
		t.Fatalf("pinned dispatch: %v", err)
	}

	deps2, store2, _, _ := dispatchDeps(t)
	id2 := queuedTicket(t, store2, "smoke")
	missing := 9
	store2.Update(id2, tickets.Update{WorkflowVersion: &missing})
	if _, err := dispatch.DispatchOne(deps2, id2, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("missing pinned version: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRefusals(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)

	if _, err := dispatch.DispatchOne(deps, 999, "admin"); !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Errorf("missing ticket: got %v", err)
	}

	draft, _ := store.Create(&tickets.Ticket{Title: "d", Repo: "r", WorkflowName: "smoke"})
	if _, err := dispatch.DispatchOne(deps, draft, "admin"); !errors.Is(err, dispatch.ErrNotQueued) {
		t.Errorf("draft ticket: got %v, want ErrNotQueued", err)
	}

	noWF := queuedTicket(t, store, "")
	if _, err := dispatch.DispatchOne(deps, noWF, "admin"); !errors.Is(err, dispatch.ErrNoWorkflow) {
		t.Errorf("no workflow name: got %v, want ErrNoWorkflow", err)
	}

	unknown := queuedTicket(t, store, "nope")
	if _, err := dispatch.DispatchOne(deps, unknown, "admin"); !errors.Is(err, dispatch.ErrWorkflowNotFound) {
		t.Errorf("unknown workflow: got %v, want ErrWorkflowNotFound", err)
	}
}

func TestDispatchOneRunCreationFailureLeavesTicketQueued(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	runs.CreateRunReturns(nil, errors.New("boom"))

	if _, err := dispatch.DispatchOne(deps, id, "admin"); err == nil {
		t.Fatal("run-creation failure must surface")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("ticket must stay queued for a retry, state = %s", got.State)
	}
}

var _ dispatch.RunCreator = db.PipelineRunFactory(nil)
