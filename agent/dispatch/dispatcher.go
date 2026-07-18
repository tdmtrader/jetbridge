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
	// RunReader powers the run-completion reconciler (reconcile.go). nil
	// skips that pass entirely.
	RunReader RunReader
	// Questions is the plan-08 checkpoint seam. nil = no checkpoint rows
	// can exist (checkpoints are render-refused; agent_run_questions is
	// not landed) — the reconciler treats every completed run as
	// checkpoint-free. Plan 08 wires the real store later.
	Questions QuestionLister
	// MaxAttempts caps the reconciler's automatic running→queued
	// re-dispatches (§2.1 bumps attempt_count on that edge). <=0 =
	// uncapped. NEVER enforced against queued tickets: queued→errored is
	// not a legal edge (§1.7) and a human re-queue is explicit intent.
	MaxAttempts int
}

// RunReader is the reconciler's read seam over pipeline runs (additive
// db.PipelineRunFactory.GetRunByID, co-signed pipeline-runs).
type RunReader interface {
	GetRunByID(id int) (db.PipelineRun, bool, error)
}

// QuestionLister is the plan-08 checkpoint seam, deliberately narrow and
// LOCAL to this package: agent_run_questions is not landed and checkpoints
// are render-refused, so production wires nil and every completed run is
// checkpoint-free. Plan 08 supplies an adapter over its questions store
// without this package changing shape.
type QuestionLister interface {
	// ListByRun returns the run's kind='checkpoint' rows (any order).
	ListByRun(pipelineRunID int) ([]CheckpointRow, error)
	// Answer releases a row (orphan cleanup: answer "", answeredBy "dispatcher").
	Answer(id int, answer, answeredBy string) error
}

// CheckpointRow is the narrow projection of agent_run_questions the F17
// tree consumes.
type CheckpointRow struct {
	ID       int
	StepName string // "checkpoint-<name>"
	AskedAt  int64
	Answered bool
	Answer   string // meaningful only when Answered
}

// Dispatcher is the RunnableComponent behind atc.ComponentAgentDispatcher.
// Polling-only (agent_tickets has no NOTIFY trigger; never notify-only per
// the fork's dropped-notification lesson) at the component framework's
// default 10s interval. The Coordinator lock serializes Run across web
// nodes; DispatchOne's guarded queued→running Transition is the intra-pass
// claim, so even a lost lock degrades to redundant-but-safe work.
type Dispatcher struct {
	deps Deps
	cfg  LoopConfig
}

func NewDispatcher(deps Deps, cfg LoopConfig) *Dispatcher {
	return &Dispatcher{deps: deps, cfg: cfg}
}

// Run dispatches every currently-queued ticket, then reconciles completed
// runs. Per-ticket failures never abort the pass (a poison ticket must not
// starve the queue); only listing failures return an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-dispatcher")
	logger.Debug("start")
	defer logger.Debug("done")

	if err := d.dispatchQueued(ctx, logger); err != nil {
		return err
	}
	return d.reconcileCompletedRuns(ctx, logger)
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
		case errors.Is(err, ErrBudgetExhausted):
			// §2.7: over-cap stays QUEUED, never failed. Re-admitted next
			// pass — headroom returns at local midnight or on a raised cap.
			logger.Info("dispatch-deferred-over-budget", lager.Data{"ticket": t.ID, "reason": err.Error()})
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition):
			// Raced: the manual route or another pass claimed it. Benign.
			logger.Debug("ticket-claimed-elsewhere", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrRenderRefused), errors.Is(err, ErrNoWorkflow), errors.Is(err, ErrWorkflowNotFound):
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

// reconcileCompletedRuns is implemented in reconcile.go (Task 6 of the
// dispatch-remainder plan). Stub keeps this slice shippable on its own.
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) error {
	return nil
}
