// Package outcomewatcher polls target repos natively (no webhooks) to record
// merge outcomes and the human-touch delta (spec §9, shared-contracts
// §1.11.1). It is the agent_outcome_watcher RunnableComponent: POLLING-ONLY,
// never notify-only — the notifications bus silently drops (fork lesson).
package outcomewatcher

import (
	"context"
	"errors"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/gitcheck"
)

// Watcher is METRICS-INDEPENDENT by design (delivery-outcomes remainder,
// amendment C3-1): the authoritative pushed_sha/base_sha source is harvest's
// push-time outcome seeding (exec.HarvestStep.seedOutcome), NOT
// agent_run_metrics — whether or not harvest writes metrics rows. It takes
// exactly three collaborators.
type Watcher struct {
	tickets  tickets.Store
	outcomes outcomes.Store
	cache    *MirrorCache
}

func New(t tickets.Store, o outcomes.Store, cache *MirrorCache) *Watcher {
	return &Watcher{tickets: t, outcomes: o, cache: cache}
}

// Run performs one tick: backstop-seed rows, detect merges on open rows,
// then sweep open rows whose tickets a bypassing writer already drove
// terminal. The sweep runs AFTER detection so an actual merge wins the race.
func (w *Watcher) Run(_ context.Context) error {
	// one `git fetch --prune` per repo per tick, regardless of ticket count
	synced := map[string]bool{}
	if err := w.seedRows(synced); err != nil {
		return err
	}
	if err := w.detectMerges(synced); err != nil {
		return err
	}
	return w.sweepTerminal()
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
// head at first sync (weaker baseline), base_sha = '' (diff 404s until a
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
