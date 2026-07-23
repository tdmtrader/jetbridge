package atccmd

import (
	"context"
	"testing"

	legacyoutcomes "github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/outcomewatcher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

type compositionOutcomeAuthorizer struct{}

func (compositionOutcomeAuthorizer) AuthorizeRun(context.Context, int, string, snapshot.WorkflowRunID) (bool, error) {
	return true, nil
}

func (compositionOutcomeAuthorizer) AuthorizeOutput(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (bool, error) {
	return true, nil
}

func (compositionOutcomeAuthorizer) AuthorizeModification(
	context.Context,
	int,
	snapshot.WorkflowRunID,
	snapshot.SnapshotID,
	snapshot.SnapshotID,
) (bool, error) {
	return true, nil
}

type compositionOutputResolver struct{}

func (compositionOutputResolver) ResolveLegacyTicketOutput(
	context.Context,
	int,
	string,
	int,
	bool,
) (outcomewatcher.GenericOutputLink, bool, error) {
	return outcomewatcher.GenericOutputLink{TeamID: 7, WorkflowRunID: 21, OutputSnapshotID: 22}, true, nil
}

func TestWorkflowOutcomeProductionCompositionBuildsAPIAndLegacyProjector(t *testing.T) {
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("main")
	store := workflowoutcomes.NewMemoryStore(nil)
	handler, err := buildWorkflowOutcomeAPI(team, store, compositionOutcomeAuthorizer{})
	if err != nil || handler == nil {
		t.Fatalf("API handler = %v, %v", handler, err)
	}
	projector, err := buildLegacyGenericOutcomeProjector(team, compositionOutputResolver{}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.Project(context.Background(), outcomewatcher.TerminalFact{
		TicketID: 9, Kind: outcomewatcher.TerminalMerged, Actor: "watcher",
	}); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.Get(context.Background(), 7, 21, 22)
	if err != nil || !found || stored.Disposition != workflowoutcomes.DispositionMerged {
		t.Fatalf("composed projection = %+v, found=%t, err=%v", stored, found, err)
	}
}

func TestWorkflowOutcomeProductionCompositionProjectsWithoutGitMirrors(t *testing.T) {
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("main")
	ticketStore := tickets.NewMemoryStore()
	ticketID, err := ticketStore.Create(&tickets.Ticket{Title: "terminal without mirrors"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ticketStore.Transition(ticketID, tickets.StateDraft, tickets.StateAbandoned, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	legacyStore := legacyoutcomes.NewMemoryStore()
	if err := legacyStore.Ensure(&legacyoutcomes.Outcome{TicketID: ticketID}); err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Close(ticketID, legacyoutcomes.ClosedUnmerged); err != nil {
		t.Fatal(err)
	}
	genericStore := workflowoutcomes.NewMemoryStore(nil)
	watcher, err := buildAgentOutcomeWatcher(
		team,
		ticketStore,
		legacyStore,
		compositionOutputResolver{},
		genericStore,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, found, err := genericStore.Get(context.Background(), 7, 21, 22)
	if err != nil || !found || stored.Disposition != workflowoutcomes.DispositionAbandoned {
		t.Fatalf("projection without Git mirrors = %+v, found=%t, err=%v", stored, found, err)
	}
}
