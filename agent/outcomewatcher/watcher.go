// Package outcomewatcher polls target repos natively (no webhooks) to record
// merge outcomes and the human-touch delta (spec §9, shared-contracts
// §1.11.1). It is the agent_outcome_watcher RunnableComponent: POLLING-ONLY,
// never notify-only — the notifications bus silently drops (fork lesson).
package outcomewatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/gitcheck"
)

// Watcher is METRICS-INDEPENDENT by design (delivery-outcomes remainder,
// amendment C3-1): the authoritative pushed_sha/base_sha source is harvest's
// push-time outcome seeding (exec.HarvestStep.seedOutcome), NOT
// agent_run_metrics — whether or not harvest writes metrics rows. Generic
// durable outcome projection is an optional, database-only adapter.
type Watcher struct {
	tickets          tickets.Store
	outcomes         outcomes.Store
	cache            *MirrorCache
	genericProjector GenericProjector
}

type Option func(*Watcher)

func WithGenericProjector(projector GenericProjector) Option {
	return func(watcher *Watcher) {
		watcher.genericProjector = projector
	}
}

func New(t tickets.Store, o outcomes.Store, cache *MirrorCache, options ...Option) *Watcher {
	watcher := &Watcher{tickets: t, outcomes: o, cache: cache}
	for _, option := range options {
		if option != nil {
			option(watcher)
		}
	}
	return watcher
}

// Run performs one tick: backstop-seed rows, detect merges on open rows,
// then sweep open rows whose tickets a bypassing writer already drove
// terminal. The sweep runs AFTER detection so an actual merge wins the race.
func (w *Watcher) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// A sent-back row can be re-armed below as soon as its ticket completes a
	// fast sent_back -> queued -> running -> needs_review cycle. Project that
	// human rejection before seedRows clears the legacy disposition.
	if err := w.projectRearmableSentBack(ctx); err != nil {
		return err
	}
	// A nil mirror cache is the production projection-only mode. Terminal
	// legacy facts still reconcile into durable generic outcomes, while every
	// operation that can touch a live Git remote stays behind explicit mirror
	// configuration.
	if w.cache != nil {
		// one `git fetch --prune` per repo per tick, regardless of ticket count
		synced := map[string]bool{}
		if err := w.seedRows(synced); err != nil {
			return err
		}
		if err := w.detectMerges(synced); err != nil {
			return err
		}
	}
	if err := w.sweepTerminal(); err != nil {
		return err
	}
	return w.projectTerminalFacts(ctx)
}

func (w *Watcher) projectRearmableSentBack(ctx context.Context) error {
	if w.genericProjector == nil {
		return nil
	}
	ticketsNeedingReview, err := w.tickets.List(tickets.ListFilter{State: tickets.StateNeedsReview})
	if err != nil {
		return err
	}
	for _, ticket := range ticketsNeedingReview {
		outcome, found, err := w.outcomes.Get(ticket.ID)
		if err != nil {
			return err
		}
		if !found || outcome.MergeState != outcomes.ClosedUnmerged || outcome.Disposition != outcomes.DispositionSentBack {
			continue
		}
		fact, projectable := terminalFactFor(ticket, *outcome)
		if !projectable {
			continue
		}
		if err := w.genericProjector.Project(ctx, fact); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) projectTerminalFacts(ctx context.Context) error {
	if w.genericProjector == nil {
		return nil
	}
	store, ok := w.outcomes.(outcomes.TerminalLister)
	if !ok {
		return fmt.Errorf("outcome watcher: generic projection requires terminal outcome listing")
	}
	rows, err := store.ListTerminal()
	if err != nil {
		return err
	}
	var projectionErrors []error
	for _, row := range rows {
		ticket, found, err := w.tickets.Get(row.TicketID)
		if err != nil {
			projectionErrors = append(projectionErrors, fmt.Errorf("outcome watcher: load terminal ticket %d: %w", row.TicketID, err))
			continue
		}
		if !found {
			continue
		}
		fact, projectable := terminalFactFor(*ticket, row)
		if !projectable {
			continue
		}
		if err := w.genericProjector.Project(ctx, fact); err != nil {
			projectionErrors = append(projectionErrors, fmt.Errorf("outcome watcher: project terminal ticket %d: %w", row.TicketID, err))
		}
	}
	return errors.Join(projectionErrors...)
}

func terminalFactFor(ticket tickets.Ticket, outcome outcomes.Outcome) (TerminalFact, bool) {
	fact := TerminalFact{TicketID: ticket.ID, Actor: "agent-outcome-watcher"}
	actor := strings.TrimSpace(outcome.DisposedBy)
	if actor != "" {
		fact.Actor = actor
		fact.HumanIntervention = true
	}
	switch outcome.MergeState {
	case outcomes.Merged:
		fact.Kind = TerminalMerged
		return fact, true
	case outcomes.MergedWithFixes:
		fact.Kind = TerminalMergedWithFixes
		fact.HumanIntervention = true
		return fact, true
	case outcomes.MergeConcluded:
		fact.Kind = TerminalConcluded
		return fact, true
	case outcomes.ClosedUnmerged:
		switch outcome.Disposition {
		case outcomes.DispositionSentBack:
			if actor == "" {
				return TerminalFact{}, false
			}
			fact.Kind = TerminalSentBack
			return fact, true
		case outcomes.DispositionAbandoned:
			fact.Kind = TerminalAbandoned
			return fact, true
		case outcomes.DispositionConcluded:
			fact.Kind = TerminalConcluded
			return fact, true
		}
		switch ticket.State {
		case tickets.StateAbandoned:
			fact.Kind = TerminalAbandoned
			return fact, true
		case tickets.StateConcluded:
			fact.Kind = TerminalConcluded
			return fact, true
		}
	}
	return TerminalFact{}, false
}

// sync fetches repo's mirror at most once per tick.
func (w *Watcher) sync(synced map[string]bool, repo string) error {
	if synced[repo] {
		return nil
	}
	if err := w.cache.Sync(repo); err != nil {
		return err
	}
	synced[repo] = true
	return nil
}

// seedRows is the §1.11.1 BACKSTOP: harvest seeds rows with authoritative
// shas at push time (exec.HarvestStep.seedOutcome). Here we only (a)
// create a fallback row when none exists — pushed_sha = remote branch
// head at first sync (weaker baseline), base_sha = ” (diff 404s until a
// re-push seeds it) — and (b) re-arm a sent_back row (F6) when harvest's
// own Ensure did not. An existing OPEN row is left alone: the backstop
// must never overwrite harvest-seeded shas with fallback values.
func (w *Watcher) seedRows(synced map[string]bool) error {
	tks, err := w.tickets.List(tickets.ListFilter{State: tickets.StateNeedsReview})
	if err != nil {
		return err
	}
	for _, tk := range tks {
		if tk.Branch == "" || tk.Repo == "" {
			continue
		}
		row, found, err := w.outcomes.Get(tk.ID)
		if err != nil {
			return err
		}
		rearmable := found &&
			row.MergeState == outcomes.ClosedUnmerged &&
			row.Disposition == outcomes.DispositionSentBack
		if found && !rearmable {
			continue // open rows keep their (possibly authoritative) shas; terminal rows are terminal
		}
		pushed, base := w.fallbackShas(synced, tk)
		if err := w.outcomes.Ensure(&outcomes.Outcome{
			TicketID: tk.ID, Repo: tk.Repo, Branch: tk.Branch,
			PushedSha: pushed, BaseSha: base,
		}); err != nil {
			return err
		}
	}
	return nil
}

// fallbackShas: remote branch head at first sync; base unknown. A git or
// network fault yields empty shas — the row still anchors created_at and a
// later harvest re-push seeds the real values.
func (w *Watcher) fallbackShas(synced map[string]bool, tk tickets.Ticket) (pushed, base string) {
	if err := w.sync(synced, tk.Repo); err != nil {
		return "", ""
	}
	head, err := w.cache.BranchHead(tk.Repo, tk.Branch)
	if err != nil {
		return "", ""
	}
	return head, ""
}

// detectMerges runs the heuristics on every open row and records hits.
// Touches every row scanned. A git/network fault on one repo must not
// abort the whole tick — the next tick retries.
func (w *Watcher) detectMerges(synced map[string]bool) error {
	open, err := w.outcomes.ListOpen()
	if err != nil {
		return err
	}
	for _, o := range open {
		_ = w.outcomes.Touch(o.TicketID)
		tk, found, err := w.tickets.Get(o.TicketID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := w.sync(synced, o.Repo); err != nil {
			continue // per-repo fault: skip, keep the tick going
		}
		res, err := w.cache.Detect(o.Repo, o.BaseSha, o.PushedSha, o.Branch, tk.TargetBranch)
		if err != nil {
			continue // per-row git fault (e.g. unknown sha): skip, keep the tick going
		}
		if res == nil {
			continue // still open — the honest answer
		}
		if err := w.recordAndTransition(tk, res); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) recordAndTransition(tk *tickets.Ticket, res *gitcheck.Result) error {
	state := outcomes.MergeState(res.State) // "merged" | "merged_with_fixes"
	if err := w.outcomes.RecordMerge(tk.ID, outcomes.MergeResult{
		State: state, MergedSha: res.MergedSha,
		HumanCommitCount:  res.HumanCommitCount,
		HumanLinesAdded:   res.HumanLinesAdded,
		HumanLinesDeleted: res.HumanLinesDeleted,
	}); err != nil {
		return err
	}
	// The needs_review re-check gates ONLY the transition, not the record
	// above: a ticket a human already raw-transitioned to merged still gets
	// its row closed with the full delta once the mirror shows the merge.
	if tk.State != tickets.StateNeedsReview {
		return nil
	}
	// Single-writer discipline: ticket state changes ONLY via Transition.
	to := tickets.StateMerged
	if state == outcomes.MergedWithFixes {
		to = tickets.StateMergedWithFixes
	}
	err := w.tickets.Transition(tk.ID, tickets.StateNeedsReview, to, tickets.TransitionMeta{})
	if errors.Is(err, tickets.ErrStaleTransition) {
		return nil // benign: a human raced us; the merge fact is recorded
	}
	return err
}

// sweepTerminal closes OPEN rows whose ticket a bypassing writer already
// drove terminal (§1.11.1 writer reconciliation): abandoned →
// closed_unmerged, concluded → concluded — never inventing disposition
// taxonomy. merged/merged_with_fixes tickets are NOT swept: organic
// detection closes those rows with a full delta when the mirror shows
// the merge.
func (w *Watcher) sweepTerminal() error {
	open, err := w.outcomes.ListOpen()
	if err != nil {
		return err
	}
	for _, row := range open {
		tk, found, err := w.tickets.Get(row.TicketID)
		if err != nil || !found {
			continue
		}
		switch tk.State {
		case tickets.StateAbandoned:
			if err := w.outcomes.Close(row.TicketID, outcomes.ClosedUnmerged); err != nil {
				return err
			}
		case tickets.StateConcluded:
			if err := w.outcomes.Close(row.TicketID, outcomes.MergeConcluded); err != nil {
				return err
			}
		}
	}
	return nil
}
