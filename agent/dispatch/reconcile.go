package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"
)

// reconcileCompletedRuns walks RUNNING tickets whose pipeline run is complete
// and applies the board's human-attention transition through the single
// writer. For v1/v2 it remains the backup when compatibility harvest never
// executes. For v3 it projects generic execution completion onto the ticket
// adapter; the linked workflow run and snapshots remain canonical. Stale or
// missing transitions are benign races. PARK-V2's awaiting_human runs (not
// landed) keep completed_at NULL, so they can never be candidates here.
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) error {
	if d.cfg.RunReader == nil {
		return nil
	}
	running, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateRunning})
	if err != nil {
		logger.Error("failed-to-list-running-tickets", err)
		return err
	}

	for _, t := range running {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.PipelineRunID == nil {
			// Moved to running outside dispatch (manual transition without
			// a run id). Nothing to reconcile against; humans own it.
			continue
		}
		run, found, err := d.cfg.RunReader.GetRunByID(*t.PipelineRunID)
		if err != nil {
			logger.Error("failed-to-get-run", err, lager.Data{"ticket": t.ID, "run": *t.PipelineRunID})
			continue
		}
		status := db.PipelineRunErrored // vanished run row = errored
		if found {
			if _, complete := run.CompletedAt(); !complete {
				continue // still running; the lifecycler owns completion
			}
			status = run.Status()
		}
		d.reconcileOne(logger, t, status)
	}
	return nil
}

func (d *Dispatcher) reconcileOne(logger lager.Logger, t tickets.Ticket, status db.PipelineRunStatus) {
	log := logger.Session("reconcile", lager.Data{"ticket": t.ID, "run-status": string(status)})

	transition := func(to tickets.State, meta tickets.TransitionMeta) {
		err := d.deps.Tickets.Transition(t.ID, tickets.StateRunning, to, meta)
		switch {
		case err == nil:
			log.Info("reconciled", lager.Data{"to": string(to)})
		case errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrTicketNotFound):
			log.Debug("reconcile-raced", lager.Data{"to": string(to)}) // harvest won; benign
		default:
			log.Error("failed-to-transition", err, lager.Data{"to": string(to)})
		}
	}

	// (a) Run succeeded but the ticket never left running: harvest should
	// have moved it — safety net. Branch meta stays empty (harvest is the
	// Branch field's only legitimate writer; §2.1 TransitionMeta note).
	if status == db.PipelineRunSucceeded {
		transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
		return
	}

	// (b) failed/errored/aborted. Checkpoint branches b.1/b.2 first — only
	// reachable when a question lister is wired (plan 08; nil today).
	if d.cfg.Questions != nil {
		rows, err := d.cfg.Questions.ListByRun(*t.PipelineRunID)
		if err != nil {
			// Cannot classify without checkpoint state: do NOT guess.
			// Retry next pass.
			log.Error("failed-to-list-checkpoint-rows", err)
			return
		}
		if d.reconcileCheckpoints(log, t, status, rows, transition) {
			return
		}
	}

	// (b.3) Checkpoint-free failure: agent step crashed, gate blew up
	// pre-harvest, abort, drain death — human triage.
	transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
}

// reconcileCheckpoints applies F17 b.1/b.2. Returns true when the ticket
// was decided; false when the run is checkpoint-free / all-approved
// (caller falls through to b.3).
func (d *Dispatcher) reconcileCheckpoints(
	log lager.Logger,
	t tickets.Ticket,
	status db.PipelineRunStatus,
	rows []CheckpointRow,
	transition func(tickets.State, tickets.TransitionMeta),
) bool {
	if len(rows) == 0 {
		return false
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AskedAt > rows[j].AskedAt })

	// b.1: latest (max asked_at) row ANSWERED with answer != approve.
	latest := rows[0]
	if latest.Answered && latest.Answer != "approve" {
		name := strings.TrimPrefix(latest.StepName, "checkpoint-")
		if d.onRejectFor(log, t, name) == "send_back" {
			// Attempt cap on the REQUEUE edge (not queued→errored, which
			// is not a legal §1.7 edge): past the cap the platform gives
			// up — errored, never failed (the run did not "run badly").
			if d.cfg.MaxAttempts > 0 && t.AttemptCount+1 > d.cfg.MaxAttempts {
				transition(tickets.StateErrored, tickets.TransitionMeta{
					ErrorDetail: fmt.Sprintf("rejected checkpoint %q would exceed %d dispatch attempts", name, d.cfg.MaxAttempts),
				})
				return true
			}
			transition(tickets.StateQueued, tickets.TransitionMeta{}) // §2.1 bumps attempt_count
			return true
		}
		// on_reject fail / empty / unknown step → human triage.
		transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
		return true
	}

	// b.2: any UNANSWERED row on a completed run — sidecar death, abort
	// while parked. Error the ticket, release the orphans so the
	// open-questions index clears (§3.2: a dead row never stays open).
	var unanswered []CheckpointRow
	for _, r := range rows {
		if !r.Answered {
			unanswered = append(unanswered, r)
		}
	}
	if len(unanswered) > 0 {
		transition(tickets.StateErrored, tickets.TransitionMeta{
			ErrorDetail: fmt.Sprintf("checkpoint %q unresolved: run completed %s while parked", unanswered[0].StepName, status),
		})
		for _, r := range unanswered {
			if err := d.cfg.Questions.Answer(r.ID, "", "dispatcher"); err != nil {
				log.Error("failed-to-release-orphan-question", err, lager.Data{"question": r.ID})
			}
		}
		return true
	}

	return false // every checkpoint answered approve → b.3
}

// onRejectFor resolves the FROZEN workflow config (the version DispatchOne
// pinned onto the ticket) and returns the named checkpoint's on_reject
// ("" when unresolvable → the safe needs_review branch).
func (d *Dispatcher) onRejectFor(log lager.Logger, t tickets.Ticket, checkpointName string) string {
	if t.WorkflowName == "" || t.WorkflowVersion == nil {
		return ""
	}
	def, found, err := d.deps.Workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	if err != nil {
		log.Error("failed-to-resolve-frozen-workflow", err)
		return ""
	}
	if !found {
		return ""
	}
	for _, s := range def.Config.Steps {
		if s.Checkpoint == checkpointName {
			return s.OnReject
		}
	}
	return ""
}
