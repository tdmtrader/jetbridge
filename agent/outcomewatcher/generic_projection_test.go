package outcomewatcher_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/outcomewatcher"
	"github.com/concourse/concourse/agent/snapshot"
)

type genericOutputResolverStub struct {
	link       outcomewatcher.GenericOutputLink
	found      bool
	err        error
	preferRepo bool
}

type explicitPortResolverStub struct {
	genericOutputResolverStub
	selectedPort string
}

func (stub *explicitPortResolverStub) ResolveLegacyTicketOutputAtPort(
	_ context.Context,
	_ int,
	_ string,
	_ int,
	portName string,
) (outcomewatcher.GenericOutputLink, bool, error) {
	stub.selectedPort = portName
	return stub.link, stub.found, stub.err
}

type collectingGenericProjector struct{ facts []outcomewatcher.TerminalFact }

func (projector *collectingGenericProjector) Project(_ context.Context, fact outcomewatcher.TerminalFact) error {
	projector.facts = append(projector.facts, fact)
	return nil
}

type poisoningGenericProjector struct {
	poisonTicketID int
	err            error
	facts          []outcomewatcher.TerminalFact
}

func (projector *poisoningGenericProjector) Project(_ context.Context, fact outcomewatcher.TerminalFact) error {
	projector.facts = append(projector.facts, fact)
	if fact.TicketID == projector.poisonTicketID {
		return projector.err
	}
	return nil
}

func TestWatcherOptionallyReconcilesEveryTerminalLegacyFact(t *testing.T) {
	tests := []struct {
		name        string
		state       outcomes.MergeState
		disposition outcomes.DispositionInput
		ticketState tickets.State
		want        outcomewatcher.TerminalFact
	}{
		{name: "merged", state: outcomes.Merged, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalMerged, Actor: "agent-outcome-watcher"}},
		{name: "merged with fixes", state: outcomes.MergedWithFixes, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalMergedWithFixes, Actor: "agent-outcome-watcher", HumanIntervention: true}},
		{name: "human abandoned", disposition: outcomes.DispositionInput{Disposition: outcomes.DispositionAbandoned, By: "alice"}, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalAbandoned, Actor: "alice", HumanIntervention: true}},
		{name: "raw abandoned", state: outcomes.ClosedUnmerged, ticketState: tickets.StateAbandoned, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalAbandoned, Actor: "agent-outcome-watcher"}},
		{name: "concluded", disposition: outcomes.DispositionInput{Disposition: outcomes.DispositionConcluded, By: "alice"}, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalConcluded, Actor: "alice", HumanIntervention: true}},
		{name: "sent back", disposition: outcomes.DispositionInput{Disposition: outcomes.DispositionSentBack, By: "alice"}, want: outcomewatcher.TerminalFact{Kind: outcomewatcher.TerminalSentBack, Actor: "alice", HumanIntervention: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticketStore := tickets.NewMemoryStore()
			ticketID, err := ticketStore.Create(&tickets.Ticket{Title: "terminal"})
			if err != nil {
				t.Fatal(err)
			}
			if test.ticketState == tickets.StateAbandoned {
				if err := ticketStore.Transition(ticketID, tickets.StateDraft, tickets.StateAbandoned, tickets.TransitionMeta{}); err != nil {
					t.Fatal(err)
				}
			}
			outcomeStore := outcomes.NewMemoryStore()
			if err := outcomeStore.Ensure(&outcomes.Outcome{TicketID: ticketID}); err != nil {
				t.Fatal(err)
			}
			switch test.state {
			case outcomes.Merged, outcomes.MergedWithFixes:
				if err := outcomeStore.RecordMerge(ticketID, outcomes.MergeResult{State: test.state, MergedSha: "deadbeef"}); err != nil {
					t.Fatal(err)
				}
			case outcomes.ClosedUnmerged:
				if err := outcomeStore.Close(ticketID, outcomes.ClosedUnmerged); err != nil {
					t.Fatal(err)
				}
			}
			if test.disposition.Disposition != "" {
				if err := outcomeStore.SetDisposition(ticketID, test.disposition); err != nil {
					t.Fatal(err)
				}
			}
			collector := &collectingGenericProjector{}
			watcher := outcomewatcher.New(ticketStore, outcomeStore, nil, outcomewatcher.WithGenericProjector(collector))
			if err := watcher.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(collector.facts) != 1 {
				t.Fatalf("projected facts = %+v", collector.facts)
			}
			want := test.want
			want.TicketID = ticketID
			if collector.facts[0] != want {
				t.Fatalf("fact = %+v, want %+v", collector.facts[0], want)
			}
		})
	}
}

func TestWatcherContinuesProjectingTerminalFactsAfterOnePoisonTicket(t *testing.T) {
	ticketStore := tickets.NewMemoryStore()
	outcomeStore := outcomes.NewMemoryStore()
	ticketIDs := make([]int, 0, 2)
	for range 2 {
		ticketID, err := ticketStore.Create(&tickets.Ticket{Title: "terminal"})
		if err != nil {
			t.Fatal(err)
		}
		if err := ticketStore.Transition(ticketID, tickets.StateDraft, tickets.StateAbandoned, tickets.TransitionMeta{}); err != nil {
			t.Fatal(err)
		}
		if err := outcomeStore.Ensure(&outcomes.Outcome{TicketID: ticketID}); err != nil {
			t.Fatal(err)
		}
		if err := outcomeStore.Close(ticketID, outcomes.ClosedUnmerged); err != nil {
			t.Fatal(err)
		}
		ticketIDs = append(ticketIDs, ticketID)
	}

	sentinel := errors.New("ambiguous disposition output")
	projector := &poisoningGenericProjector{poisonTicketID: ticketIDs[0], err: sentinel}
	watcher := outcomewatcher.New(ticketStore, outcomeStore, nil, outcomewatcher.WithGenericProjector(projector))
	if err := watcher.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want %v", err, sentinel)
	}
	if len(projector.facts) != 2 {
		t.Fatalf("projected facts = %+v, want both terminal tickets", projector.facts)
	}
	if projector.facts[1].TicketID != ticketIDs[1] {
		t.Fatalf("second projected ticket = %d, want %d", projector.facts[1].TicketID, ticketIDs[1])
	}
}

func TestProjectionOnlyWatcherDoesNotTouchLiveGitForActiveTickets(t *testing.T) {
	ticketStore := tickets.NewMemoryStore()
	ticketID, err := ticketStore.Create(&tickets.Ticket{Title: "awaiting review", Repo: "private/repo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range [][2]tickets.State{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateQueued, tickets.StateRunning},
		{tickets.StateRunning, tickets.StateNeedsReview},
	} {
		if err := ticketStore.Transition(ticketID, transition[0], transition[1], tickets.TransitionMeta{Branch: "agent/active"}); err != nil {
			t.Fatal(err)
		}
	}
	watcher := outcomewatcher.New(
		ticketStore,
		outcomes.NewMemoryStore(),
		nil,
		outcomewatcher.WithGenericProjector(&collectingGenericProjector{}),
	)
	if err := watcher.Run(context.Background()); err != nil {
		t.Fatalf("projection-only tick with active ticket: %v", err)
	}
}

func (stub *genericOutputResolverStub) ResolveLegacyTicketOutput(
	context.Context,
	int,
	string,
	int,
	bool,
) (outcomewatcher.GenericOutputLink, bool, error) {
	return stub.link, stub.found, stub.err
}

func TestDurableGenericProjectorMapsLegacyTerminalFacts(t *testing.T) {
	tests := []struct {
		name             string
		kind             outcomewatcher.TerminalFactKind
		disposition      workflowoutcomes.Disposition
		publication      workflowoutcomes.PublicationState
		humanModified    bool
		humanIntervened  bool
		interventionWant int
	}{
		{name: "merged", kind: outcomewatcher.TerminalMerged, disposition: workflowoutcomes.DispositionMerged, publication: workflowoutcomes.PublicationNotRequested},
		{name: "merged with fixes", kind: outcomewatcher.TerminalMergedWithFixes, disposition: workflowoutcomes.DispositionMerged, publication: workflowoutcomes.PublicationNotRequested, humanModified: true, humanIntervened: true, interventionWant: 1},
		{name: "abandoned", kind: outcomewatcher.TerminalAbandoned, disposition: workflowoutcomes.DispositionAbandoned, publication: workflowoutcomes.PublicationNotRequested, humanIntervened: true, interventionWant: 1},
		{name: "concluded", kind: outcomewatcher.TerminalConcluded, disposition: workflowoutcomes.DispositionAccepted, publication: workflowoutcomes.PublicationNotRequested, humanIntervened: true, interventionWant: 1},
		{name: "sent back", kind: outcomewatcher.TerminalSentBack, disposition: workflowoutcomes.DispositionRejected, publication: workflowoutcomes.PublicationNotRequested, humanIntervened: true, interventionWant: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := workflowoutcomes.NewMemoryStore(nil)
			resolver := &genericOutputResolverStub{found: true, link: outcomewatcher.GenericOutputLink{
				TeamID: 7, WorkflowRunID: snapshot.WorkflowRunID(21), OutputSnapshotID: snapshot.SnapshotID(22),
			}}
			projector, err := outcomewatcher.NewDurableGenericProjector(7, "main", resolver, store)
			if err != nil {
				t.Fatal(err)
			}
			fact := outcomewatcher.TerminalFact{
				TicketID: 9, Kind: test.kind, Actor: "alice", HumanIntervention: test.humanIntervened,
			}
			if err := projector.Project(context.Background(), fact); err != nil {
				t.Fatal(err)
			}
			outcome, found, err := store.Get(context.Background(), 7, 21, 22)
			if err != nil || !found {
				t.Fatalf("outcome found=%t err=%v", found, err)
			}
			if outcome.Disposition != test.disposition || outcome.PublicationState != test.publication ||
				outcome.HumanModified != test.humanModified || outcome.InterventionCount != test.interventionWant || outcome.Actor != "alice" {
				t.Fatalf("outcome = %+v", outcome)
			}
			if err := projector.Project(context.Background(), fact); err != nil {
				t.Fatal(err)
			}
			replayed, _, _ := store.Get(context.Background(), 7, 21, 22)
			if replayed.Revision != 1 {
				t.Fatalf("idempotent replay revision = %d, want 1", replayed.Revision)
			}
		})
	}
}

func TestDurableGenericProjectorDoesNotGuessDispositionOutputsFromTerminalFactKinds(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(nil)
	resolver := &genericOutputResolverStub{found: false}
	projector, err := outcomewatcher.NewDurableGenericProjector(7, "main", resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind outcomewatcher.TerminalFactKind
	}{
		{outcomewatcher.TerminalMerged},
		{outcomewatcher.TerminalMergedWithFixes},
		{outcomewatcher.TerminalAbandoned},
		{outcomewatcher.TerminalConcluded},
		{outcomewatcher.TerminalSentBack},
	} {
		resolver.preferRepo = false
		resolver.found = false
		resolver.err = nil
		resolver.link = outcomewatcher.GenericOutputLink{}
		resolverCall := false
		resolverWithAssertion := &assertingGenericResolver{
			call: func(prefer bool) { resolverCall = true; resolver.preferRepo = prefer },
		}
		projector, err = outcomewatcher.NewDurableGenericProjector(7, "main", resolverWithAssertion, store)
		if err != nil {
			t.Fatal(err)
		}
		if err := projector.Project(context.Background(), outcomewatcher.TerminalFact{TicketID: 1, Kind: test.kind, Actor: "watcher"}); err != nil {
			t.Fatal(err)
		}
		if !resolverCall || resolver.preferRepo {
			t.Fatalf("kind %q guessed repository-change preference = %t", test.kind, resolver.preferRepo)
		}
	}
}

func TestDurableGenericProjectorUsesAnExplicitDispositionOutputPortSelector(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(nil)
	resolver := &explicitPortResolverStub{genericOutputResolverStub: genericOutputResolverStub{
		found: true,
		link: outcomewatcher.GenericOutputLink{
			TeamID: 7, WorkflowRunID: 21, OutputSnapshotID: 22,
		},
	}}
	var selectedFact outcomewatcher.TerminalFact
	projector, err := outcomewatcher.NewDurableGenericProjector(
		7,
		"main",
		resolver,
		store,
		outcomewatcher.WithDispositionOutputSelector(outcomewatcher.DispositionOutputSelectorFunc(
			func(_ context.Context, fact outcomewatcher.TerminalFact) (string, bool, error) {
				selectedFact = fact
				return "change", true, nil
			},
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	fact := outcomewatcher.TerminalFact{TicketID: 9, Kind: outcomewatcher.TerminalMerged, Actor: "watcher"}
	if err := projector.Project(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	if selectedFact != fact {
		t.Fatalf("selector fact = %+v, want %+v", selectedFact, fact)
	}
	if resolver.selectedPort != "change" {
		t.Fatalf("resolved output port = %q, want change", resolver.selectedPort)
	}
	if _, found, err := store.Get(context.Background(), 7, 21, 22); err != nil || !found {
		t.Fatalf("outcome found=%t err=%v", found, err)
	}
}

type assertingGenericResolver struct{ call func(bool) }

func (resolver *assertingGenericResolver) ResolveLegacyTicketOutput(
	_ context.Context,
	_ int,
	_ string,
	_ int,
	prefer bool,
) (outcomewatcher.GenericOutputLink, bool, error) {
	resolver.call(prefer)
	return outcomewatcher.GenericOutputLink{}, false, nil
}

func TestDurableGenericProjectorFailsClosedOnResolverOrTeamMismatch(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(nil)
	sentinel := errors.New("ambiguous outputs")
	resolver := &genericOutputResolverStub{err: sentinel}
	projector, err := outcomewatcher.NewDurableGenericProjector(7, "main", resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	fact := outcomewatcher.TerminalFact{TicketID: 1, Kind: outcomewatcher.TerminalMerged, Actor: "watcher"}
	if err := projector.Project(context.Background(), fact); !errors.Is(err, sentinel) {
		t.Fatalf("resolver error = %v, want %v", err, sentinel)
	}
	resolver.err = nil
	resolver.found = true
	resolver.link = outcomewatcher.GenericOutputLink{TeamID: 8, WorkflowRunID: 21, OutputSnapshotID: 22}
	if err := projector.Project(context.Background(), fact); !errors.Is(err, outcomewatcher.ErrGenericOutputTeamMismatch) {
		t.Fatalf("team mismatch error = %v", err)
	}
}

func TestDurableGenericProjectorDoesNotOverwriteAnOutcomeOwnedByTheGenericAPI(t *testing.T) {
	store := workflowoutcomes.NewMemoryStore(nil)
	resolver := &genericOutputResolverStub{found: true, link: outcomewatcher.GenericOutputLink{
		TeamID: 7, WorkflowRunID: 21, OutputSnapshotID: 22,
	}}
	_, _, err := store.Record(context.Background(), 7, workflowoutcomes.RecordRequest{
		WorkflowRunID: 21, OutputSnapshotID: 22,
		Disposition: workflowoutcomes.DispositionAccepted, PublicationState: workflowoutcomes.PublicationNotRequested,
		Labels: []string{"human-reviewed"}, Actor: "alice", InterventionCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	projector, err := outcomewatcher.NewDurableGenericProjector(7, "main", resolver, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.Project(context.Background(), outcomewatcher.TerminalFact{
		TicketID: 1, Kind: outcomewatcher.TerminalMerged, Actor: "watcher",
	}); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := store.Get(context.Background(), 7, 21, 22)
	if stored.Disposition != workflowoutcomes.DispositionAccepted || stored.Actor != "alice" || stored.Revision != 1 {
		t.Fatalf("generic API outcome was overwritten: %+v", stored)
	}
}
