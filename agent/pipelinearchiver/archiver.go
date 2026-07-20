// Package pipelinearchiver is the agent_pipeline_archiver
// RunnableComponent (ticket #42): once an agent ticket reaches a TERMINAL
// disposition, its per-ticket pipelines (`agent-ticket-<id>` on the main
// team — the dispatch naming convention, agent/dispatch/dispatch.go) are
// dead dashboard cards, so every still-unarchived one is archived via the
// same db.Pipeline.Archive that fly archive-pipeline drives. It archives by
// NAME, catching pipelines the run-linkage pass in atc/runlifecycle misses
// (tickets whose pipeline_run_id was never set or no longer points at the
// latest attempt). POLLING-ONLY, like the dispatcher's run-completion
// reconciler it is patterned on: agent_tickets has no NOTIFY trigger and
// the notifications bus silently drops (recorded fork lesson). Never
// deletes anything.
package pipelinearchiver

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// Archiver scans terminally-disposed tickets and archives their pipelines.
// Non-terminal states — including errored, which is retryable and must
// keep its pipeline live — are never even listed: the scan iterates
// tickets.TerminalStates() (merged, merged_with_fixes, abandoned,
// concluded), the frozen no-outgoing-edges set, so it can never race a
// requeue.
type Archiver struct {
	tickets tickets.Store
	teams   db.TeamFactory
}

func New(tickets tickets.Store, teams db.TeamFactory) *Archiver {
	return &Archiver{tickets: tickets, teams: teams}
}

// Run performs one tick. Listing failures fail the tick (nothing sensible
// to do without the candidate set); a per-pipeline archive failure is
// logged and skipped so one wedged pipeline cannot starve the rest —
// terminal states are forever, so the next tick retries.
func (a *Archiver) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-pipeline-archiver")

	// pipeline name -> ticket id, for every terminal ticket
	terminal := map[string]int{}
	for _, state := range tickets.TerminalStates() {
		tks, err := a.tickets.List(tickets.ListFilter{State: state})
		if err != nil {
			logger.Error("failed-to-list-terminal-tickets", err, lager.Data{"state": string(state)})
			return err
		}
		for _, t := range tks {
			terminal[fmt.Sprintf("agent-ticket-%d", t.ID)] = t.ID
		}
	}
	if len(terminal) == 0 {
		return nil
	}

	// Team scope is pinned to main: dispatch only ever creates per-ticket
	// pipelines there, and an unpinned name match could archive another
	// team's identically-named pipeline.
	team, found, err := a.teams.FindTeam(atc.DefaultTeamName)
	if err != nil {
		logger.Error("failed-to-find-main-team", err)
		return err
	}
	if !found {
		return nil // no main team, no per-ticket pipelines
	}

	// One listing per tick; matching by Name() sweeps the base template
	// AND every {"run": N} instance — instances share the template's name.
	pipelines, err := team.Pipelines()
	if err != nil {
		logger.Error("failed-to-list-pipelines", err)
		return err
	}

	for _, p := range pipelines {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ticketID, ok := terminal[p.Name()]
		if !ok || p.Archived() {
			continue // not a terminal ticket's pipeline, or already tidy
		}
		if err := p.Archive(); err != nil {
			logger.Error("failed-to-archive-pipeline", err, lager.Data{
				"ticket": ticketID, "pipeline-id": p.ID(), "pipeline-name": p.Name(),
			})
			continue
		}
		logger.Info("pipeline-archived", lager.Data{
			"ticket": ticketID, "pipeline-id": p.ID(), "pipeline-name": p.Name(),
			"instance-vars": p.InstanceVars(),
		})
	}
	return nil
}
