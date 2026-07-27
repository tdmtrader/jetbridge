package dispatch

import (
	"context"
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
)

// LoopConfig tunes the Dispatcher beyond DispatchOne's Deps.
type LoopConfig struct {
	// Mode resolves the effective dispatcher mode HOT (read fresh each tick):
	// active dispatches queued tickets, anything else is dormant. Production
	// wires a closure over agent_settings (EffectiveModeFromRead). nil defaults
	// to active, preserving the always-dispatch behavior tests relied on before
	// the runtime toggle.
	Mode func() string
}

// Dispatcher is the RunnableComponent behind atc.ComponentAgentDispatcher, and
// it does exactly one thing: dispatch currently-queued tickets when the runtime
// mode says to. Ticket terminalization is NOT here — a finished workflow run
// projects its owning ticket to needs_review from the always-on workflow-run
// reconciler (agent/workflowrun), so pausing dispatch can never strand a
// running ticket.
//
// Polling-only (agent_tickets has no NOTIFY trigger; never notify-only per the
// fork's dropped-notification lesson) at the component framework's default 10s
// interval. The Coordinator lock serializes Run across web nodes; dispatch
// reserves durably before admission and guards its final queued→running
// transition, so a lost coordinator lock degrades to redundant, idempotent
// work.
type Dispatcher struct {
	deps Deps
	cfg  LoopConfig
}

func NewDispatcher(deps Deps, cfg LoopConfig) *Dispatcher {
	return &Dispatcher{deps: deps, cfg: cfg}
}

// effectiveMode resolves the mode this tick honors. nil provider (older
// callers / tests) defaults to active — the historical always-dispatch loop.
func (d *Dispatcher) effectiveMode() string {
	if d.cfg.Mode == nil {
		return ModeActive
	}
	return d.cfg.Mode()
}

// Run dispatches every currently-queued ticket when the effective mode
// (resolved once per tick, hot) is active, and does nothing otherwise.
//
// Per-ticket failures never abort the pass (a poison ticket must not starve the
// queue); only listing failures return an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-dispatcher")
	mode := d.effectiveMode()
	logger.Debug("start", lager.Data{"mode": mode})
	defer logger.Debug("done")

	if mode != ModeActive {
		// paused, off, or any unrecognized mode — fail safe to dormant.
		return nil
	}
	return d.dispatchQueued(ctx, logger)
}

func (d *Dispatcher) dispatchQueued(ctx context.Context, logger lager.Logger) error {
	queued, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateQueued})
	if err != nil {
		logger.Error("failed-to-list-queued-tickets", err)
		return err
	}

	for _, t := range queued {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := DispatchOne(ctx, d.deps, t.ID, "agent-dispatcher")
		switch {
		case err == nil:
			logger.Info("dispatched", lager.Data{"ticket": t.ID, "workflow-run": res.WorkflowRunID.String()})
		case errors.Is(err, ErrBudgetExhausted):
			// Over-cap stays QUEUED, never failed. Re-admitted next pass —
			// headroom returns at local midnight or on a raised cap.
			logger.Info("dispatch-deferred-over-budget", lager.Data{"ticket": t.ID, "reason": err.Error()})
		case errors.Is(err, ErrInputsPending):
			// The reservation is intentional and durable. An upstream
			// upload/resource-capture/UI selection can fill the missing exact
			// input; the same reservation is retried on the next polling pass.
			logger.Info("dispatch-deferred-inputs-pending", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition):
			// Raced: the manual route or another pass claimed it. Benign.
			logger.Debug("ticket-claimed-elsewhere", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrNotTicketDispatchable), errors.Is(err, ErrNoWorkflow),
			errors.Is(err, ErrWorkflowNotFound), errors.Is(err, tickets.ErrDispatchConflict):
			// Permanently refused: loud, non-fatal, and queued for a human to
			// fix the workflow or transition the ticket away.
			logger.Error("dispatch-refused", err, lager.Data{"ticket": t.ID})
		default:
			// Platform fault: DispatchOne is pre-transition retry-safe, so
			// the ticket stays queued and next pass retries. Never mark
			// failed (platform faults → error, not failed).
			logger.Error("failed-to-dispatch", err, lager.Data{"ticket": t.ID})
		}
	}
	return nil
}
