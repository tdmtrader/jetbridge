package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/workflow"
)

// ErrNotReviewable is returned when a merge is requested for a ticket that
// is not awaiting review. A merge is only meaningful from needs_review:
// earlier states have nothing delivered, later ones are terminal.
var ErrNotReviewable = errors.New("ticket is not awaiting review")

// MergeOptions are the per-merge knobs a caller may set. The zero value is
// a speculative merge-commit integration with no gates.
type MergeOptions struct {
	Method     string // merge | squash; empty = merge
	Message    string
	Push       bool // false = speculative: answer, do not land
	GatePolicy workflow.GatePolicy
}

// MergeResult is what a triggered merge reports back.
type MergeResult struct {
	RunID        int
	PipelineName string
}

// MergeOne renders and triggers the platform-owned merge for a reviewed
// ticket (design 2026-07-20 §2).
//
// It deliberately does NOT transition the ticket. The merge can still
// conflict, fail a gate on the merged tree, or lose a push race, and only a
// real exit-0 landing is a merge — exec.MergeStep records the transition
// and the outcome row when the push actually succeeds. Moving the ticket
// here would report merges that never happened.
//
// It also needs no budget admission and no per-run credential: the merge
// runs a deterministic binary, not an agent, so it spends no LLM budget and
// needs no Anthropic token — only the §8.3 git credential the exec mounts.
func MergeOne(ctx context.Context, deps Deps, ticketID int, mergedBy string, opts MergeOptions) (MergeResult, error) {
	t, found, err := deps.Tickets.Get(ticketID)
	if err != nil {
		return MergeResult{}, err
	}
	if !found {
		return MergeResult{}, tickets.ErrTicketNotFound
	}
	if t.State != tickets.StateNeedsReview {
		return MergeResult{}, fmt.Errorf("%w (state: %s)", ErrNotReviewable, t.State)
	}
	if t.Branch == "" {
		return MergeResult{}, fmt.Errorf("ticket %d has no delivered branch — nothing was harvested to merge", ticketID)
	}

	// Gate policy: default to the ticket's OWN workflow. Same repo, same
	// checks — so the merged tree gets at least what the branch got. An
	// explicit policy on the request wins (a human deliberately choosing).
	gates := opts.GatePolicy
	if len(gates.Gates) == 0 {
		gates, err = ticketGatePolicy(deps, t)
		if err != nil {
			return MergeResult{}, err
		}
	}

	cfg, err := RenderMerge(MergeRenderInput{
		Ticket:      *t,
		Branch:      t.Branch,
		Method:      opts.Method,
		Message:     opts.Message,
		Push:        opts.Push,
		GatePolicy:  gates,
		RepoBaseURL: deps.RepoBaseURL,
	})
	if err != nil {
		return MergeResult{}, fmt.Errorf("%w: render merge for ticket %d: %s", ErrRenderRefused, ticketID, err)
	}

	// Distinct from the ticket's agent-ticket-<id> pipeline: the merge is a
	// separate one-shot run, and reusing that name would clobber the run
	// history the review is still being read from.
	pipelineName := fmt.Sprintf("agent-merge-%d", ticketID)
	pipelineID, err := deps.Templates.SaveTemplate(pipelineName, cfg)
	if err != nil {
		return MergeResult{}, fmt.Errorf("save template pipeline %s: %w", pipelineName, err)
	}

	run, err := deps.Runs.CreateRun(pipelineID, nil, mergedBy)
	if err != nil {
		return MergeResult{}, fmt.Errorf("create merge run: %w", err)
	}

	return MergeResult{RunID: run.ID(), PipelineName: pipelineName}, nil
}

// ticketGatePolicy resolves the gate policy the ticket's own workflow used.
// The version was frozen onto the ticket at dispatch, so this reproduces
// exactly the checks the delivered branch passed.
//
// An unresolvable workflow is an ERROR rather than a silent ungated merge:
// merging a branch whose gates we cannot reproduce is precisely the case
// worth stopping on. A caller who genuinely wants it can pass an explicit
// policy (including an empty one) on the request.
func ticketGatePolicy(deps Deps, t *tickets.Ticket) (workflow.GatePolicy, error) {
	if deps.Workflows == nil || t.WorkflowName == "" {
		return workflow.GatePolicy{}, nil
	}

	var (
		def   *workflow.Definition
		found bool
		err   error
	)
	if t.WorkflowVersion != nil {
		def, found, err = deps.Workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	} else {
		def, found, err = deps.Workflows.Live(t.WorkflowName)
	}
	if err != nil {
		return workflow.GatePolicy{}, fmt.Errorf("resolve gate policy for ticket %d: %w", t.ID, err)
	}
	if !found {
		return workflow.GatePolicy{}, fmt.Errorf(
			"workflow %q for ticket %d is unresolvable — refusing to merge with gates we cannot reproduce; pass an explicit gate policy to override",
			t.WorkflowName, t.ID)
	}
	return def.Config.GatePolicy, nil
}
