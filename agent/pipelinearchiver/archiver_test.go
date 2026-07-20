package pipelinearchiver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/pipelinearchiver"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

// scaffold wires the archiver against dbfakes: a ticket store fake whose
// List answers per-state from byState, and a main-team fake exposing the
// given pipelines.
func scaffold(
	t *testing.T,
	byState map[tickets.State][]tickets.Ticket,
	pipelines []*dbfakes.FakePipeline,
) (*pipelinearchiver.Archiver, *dbfakes.FakeAgentTicketsFactory, *dbfakes.FakeTeam) {
	t.Helper()

	store := new(dbfakes.FakeAgentTicketsFactory)
	store.ListStub = func(f tickets.ListFilter) ([]tickets.Ticket, error) {
		return byState[f.State], nil
	}

	team := new(dbfakes.FakeTeam)
	dbPipelines := make([]db.Pipeline, len(pipelines))
	for i, p := range pipelines {
		dbPipelines[i] = p
	}
	team.PipelinesReturns(dbPipelines, nil)

	teams := new(dbfakes.FakeTeamFactory)
	teams.FindTeamReturns(team, true, nil)

	return pipelinearchiver.New(store, teams), store, team
}

func pipeline(name string, archived bool) *dbfakes.FakePipeline {
	p := new(dbfakes.FakePipeline)
	p.NameReturns(name)
	p.ArchivedReturns(archived)
	return p
}

func ticket(id int, state tickets.State) tickets.Ticket {
	return tickets.Ticket{ID: id, State: state}
}

func TestArchivesAllInstancesOfTerminalTicketPipelines(t *testing.T) {
	// ticket 12 merged; its template AND materialized instances share the
	// name agent-ticket-12 — every unarchived one goes.
	template := pipeline("agent-ticket-12", false)
	instance := pipeline("agent-ticket-12", false) // {"run": N} instance: same name
	bystander := pipeline("some-other-pipeline", false)
	liveTicket := pipeline("agent-ticket-99", false) // ticket 99 is NOT terminal

	archiver, _, _ := scaffold(t,
		map[tickets.State][]tickets.Ticket{
			tickets.StateMerged: {ticket(12, tickets.StateMerged)},
		},
		[]*dbfakes.FakePipeline{template, instance, bystander, liveTicket},
	)

	if err := archiver.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if n := template.ArchiveCallCount(); n != 1 {
		t.Errorf("template: Archive called %d times, want 1", n)
	}
	if n := instance.ArchiveCallCount(); n != 1 {
		t.Errorf("instance: Archive called %d times, want 1", n)
	}
	if n := bystander.ArchiveCallCount(); n != 0 {
		t.Errorf("bystander pipeline archived (%d calls)", n)
	}
	if n := liveTicket.ArchiveCallCount(); n != 0 {
		t.Errorf("non-terminal ticket's pipeline archived (%d calls)", n)
	}
}

func TestCoversEveryTerminalDisposition(t *testing.T) {
	// merged / merged_with_fixes / concluded / abandoned all tidy up.
	byState := map[tickets.State][]tickets.Ticket{
		tickets.StateMerged:          {ticket(1, tickets.StateMerged)},
		tickets.StateMergedWithFixes: {ticket(2, tickets.StateMergedWithFixes)},
		tickets.StateConcluded:       {ticket(3, tickets.StateConcluded)},
		tickets.StateAbandoned:       {ticket(4, tickets.StateAbandoned)},
	}
	var ps []*dbfakes.FakePipeline
	for _, name := range []string{"agent-ticket-1", "agent-ticket-2", "agent-ticket-3", "agent-ticket-4"} {
		ps = append(ps, pipeline(name, false))
	}

	archiver, _, _ := scaffold(t, byState, ps)
	if err := archiver.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, p := range ps {
		if n := p.ArchiveCallCount(); n != 1 {
			t.Errorf("pipeline agent-ticket-%d: Archive called %d times, want 1", i+1, n)
		}
	}
}

func TestOnlyTerminalStatesAreScanned(t *testing.T) {
	// The store must only ever be asked for terminal states — never
	// draft/queued/running/needs_review/sent_back/failed/errored (errored
	// is retryable: its pipeline must stay live).
	archiver, store, team := scaffold(t, nil, nil)
	if err := archiver.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := map[tickets.State]bool{}
	for _, s := range tickets.TerminalStates() {
		want[s] = true
	}
	if store.ListCallCount() != len(want) {
		t.Fatalf("List called %d times, want %d", store.ListCallCount(), len(want))
	}
	for i := 0; i < store.ListCallCount(); i++ {
		f := store.ListArgsForCall(i)
		if !want[f.State] {
			t.Errorf("List called with non-terminal state %q", f.State)
		}
		delete(want, f.State)
	}

	// no terminal tickets at all → the pipeline listing is skipped entirely
	if n := team.PipelinesCallCount(); n != 0 {
		t.Errorf("Pipelines listed %d times with no terminal tickets, want 0", n)
	}
}

func TestIdempotentOnReTick(t *testing.T) {
	// Second tick: the pipeline now reports archived — Archive must not be
	// called again (fly archive-pipeline's method is safe, but re-archiving
	// every tick forever churns the audit trail).
	archived := pipeline("agent-ticket-12", true)
	archiver, _, _ := scaffold(t,
		map[tickets.State][]tickets.Ticket{
			tickets.StateMerged: {ticket(12, tickets.StateMerged)},
		},
		[]*dbfakes.FakePipeline{archived},
	)

	if err := archiver.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := archived.ArchiveCallCount(); n != 0 {
		t.Errorf("already-archived pipeline re-archived (%d calls)", n)
	}
}

func TestArchiveFailureDoesNotAbortThePass(t *testing.T) {
	broken := pipeline("agent-ticket-1", false)
	broken.ArchiveReturns(errors.New("nope"))
	fine := pipeline("agent-ticket-2", false)

	archiver, _, _ := scaffold(t,
		map[tickets.State][]tickets.Ticket{
			tickets.StateMerged:    {ticket(1, tickets.StateMerged)},
			tickets.StateConcluded: {ticket(2, tickets.StateConcluded)},
		},
		[]*dbfakes.FakePipeline{broken, fine},
	)

	if err := archiver.Run(context.Background()); err != nil {
		t.Fatalf("per-pipeline failure must not fail the tick: %v", err)
	}
	if n := fine.ArchiveCallCount(); n != 1 {
		t.Errorf("later pipeline skipped after earlier failure (%d calls)", n)
	}
}

func TestListFailureFailsTheTick(t *testing.T) {
	store := new(dbfakes.FakeAgentTicketsFactory)
	store.ListReturns(nil, errors.New("db down"))
	teams := new(dbfakes.FakeTeamFactory)

	archiver := pipelinearchiver.New(store, teams)
	if err := archiver.Run(context.Background()); err == nil {
		t.Fatal("expected error when listing tickets fails")
	}
	if n := teams.FindTeamCallCount(); n != 0 {
		t.Errorf("team looked up despite list failure (%d calls)", n)
	}
}

func TestMainTeamMissingIsANoOp(t *testing.T) {
	store := new(dbfakes.FakeAgentTicketsFactory)
	store.ListStub = func(f tickets.ListFilter) ([]tickets.Ticket, error) {
		if f.State == tickets.StateMerged {
			return []tickets.Ticket{ticket(12, tickets.StateMerged)}, nil
		}
		return nil, nil
	}
	teams := new(dbfakes.FakeTeamFactory)
	teams.FindTeamReturns(nil, false, nil)

	archiver := pipelinearchiver.New(store, teams)
	if err := archiver.Run(context.Background()); err != nil {
		t.Fatalf("missing main team must be benign: %v", err)
	}
	if got := teams.FindTeamArgsForCall(0); got != atc.DefaultTeamName {
		t.Errorf("looked up team %q, want %q", got, atc.DefaultTeamName)
	}
}
