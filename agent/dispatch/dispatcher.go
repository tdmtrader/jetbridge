package dispatch

import (
	"context"
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"
)

// LoopConfig tunes the Dispatcher beyond DispatchOne's Deps.
type LoopConfig struct {
	// Mode resolves the effective dispatcher mode HOT (read fresh each tick):
	// active auto-dispatches + reconciles, paused reconciles only, off is
	// dormant. Production wires a closure over agent_settings + the boot flag
	// (ResolveEffectiveMode). nil defaults to active, preserving the
	// always-dispatch behavior tests relied on before the runtime toggle.
	Mode func() string
	// RunReader powers the run-completion reconciler (reconcile.go). nil
	// skips that pass entirely.
	RunReader RunReader
	// MaxAttempts is retained as dispatcher configuration. Terminal run
	// reconciliation always requires human review.
	MaxAttempts int
}

// RunReader is the reconciler's read seam over pipeline runs (additive
// db.PipelineRunFactory.GetRunByID, co-signed pipeline-runs).
type RunReader interface {
	GetRunByID(id int) (db.PipelineRun, bool, error)
}

// Dispatcher is the RunnableComponent behind atc.ComponentAgentDispatcher.
// Polling-only (agent_tickets has no NOTIFY trigger; never notify-only per
// the fork's dropped-notification lesson) at the component framework's
// default 10s interval. The Coordinator lock serializes Run across web
// nodes; schema-v3 dispatch also reserves durably before admission, while
// the legacy path retains its guarded queued→running transition. A lost
// coordinator lock therefore degrades to redundant, idempotent work.
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

// Run gates on the effective mode (resolved once per tick, hot):
//
//	active — dispatch every currently-queued ticket, then reconcile.
//	paused — skip dispatch, still reconcile (the safety net stays alive).
//	off    — skip both (fully dormant).
//
// Per-ticket failures never abort the pass (a poison ticket must not starve the
// queue); only listing failures return an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-dispatcher")
	mode := d.effectiveMode()
	logger.Debug("start", lager.Data{"mode": mode})
	defer logger.Debug("done")

	switch mode {
	case ModeActive:
		if err := d.dispatchQueued(ctx, logger); err != nil {
			return err
		}
		return d.reconcileCompletedRuns(ctx, logger)
	case ModePaused:
		return d.reconcileCompletedRuns(ctx, logger)
	default:
		// off (or any unrecognized mode — fail safe to dormant).
		return nil
	}
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
			logger.Info("dispatched", lager.Data{"ticket": t.ID, "run": res.RunID, "pipeline": res.PipelineName})
			// Advisory only (ticket #46): vocabulary that has triggered
			// CLI usage-policy false refusals. Never blocks a dispatch.
			for _, warning := range res.Warnings {
				logger.Info("spec-lint", lager.Data{"ticket": t.ID, "warning": warning})
			}
		case errors.Is(err, ErrBudgetExhausted):
			// §2.7: over-cap stays QUEUED, never failed. Re-admitted next
			// pass — headroom returns at local midnight or on a raised cap.
			logger.Info("dispatch-deferred-over-budget", lager.Data{"ticket": t.ID, "reason": err.Error()})
		case errors.Is(err, ErrInputsPending):
			// The reservation is intentional and durable. An upstream
			// upload/resource-capture/UI selection can fill the missing exact
			// input; the same reservation is retried on the next polling pass.
			logger.Info("dispatch-deferred-inputs-pending", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition):
			// Raced: the manual route or another pass claimed it. Benign.
			logger.Debug("ticket-claimed-elsewhere", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrRenderRefused), errors.Is(err, ErrNoWorkflow), errors.Is(err, ErrWorkflowNotFound),
			errors.Is(err, tickets.ErrDispatchConflict):
			// Malformed for v0: loud, non-fatal, stays queued for a human
			// to fix the workflow or transition the ticket away (see plan
			// Risks R2 for the log-cadence tradeoff).
			logger.Error("dispatch-refused", err, lager.Data{"ticket": t.ID})
		default:
			// Platform fault: DispatchOne is pre-transition retry-safe, so
			// the ticket stays queued and next pass retries. Never mark
			// failed (§2.7: platform faults → error, not failed).
			logger.Error("failed-to-dispatch", err, lager.Data{"ticket": t.ID})
		}
	}
	return nil
}
