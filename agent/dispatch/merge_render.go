package dispatch

import (
	"fmt"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// MergeRenderInput is everything the merge renderer needs. It is
// deliberately NOT a workflow render: the merge happens after review,
// potentially days after the ticket's run finished, so it is its own
// one-shot pipeline rather than a step appended to the original plan.
type MergeRenderInput struct {
	Ticket      tickets.Ticket // ID, Repo, TargetBranch
	Branch      string         // delivered branch, e.g. agent/ticket-42
	Method      string         // merge | squash; empty = merge
	Message     string
	Push        bool // false = speculative: answer, do not land
	GatePolicy  workflow.GatePolicy
	RepoBaseURL string
}

// RenderMerge builds the one-shot merge pipeline: get the repo, then merge.
//
// The `repo` resource tracks the TARGET branch — the runner fetches every
// ref from that clone and pushes the integrated result back to it. Nothing
// here is defaulted: a wrong target silently merges into the wrong branch,
// so incomplete input is a render refusal.
func RenderMerge(in MergeRenderInput) (atc.Config, error) {
	if in.Ticket.ID <= 0 {
		return atc.Config{}, fmt.Errorf("merge render requires a ticket id")
	}
	if in.Ticket.Repo == "" {
		return atc.Config{}, fmt.Errorf("ticket %d has no repo", in.Ticket.ID)
	}
	if in.Ticket.TargetBranch == "" {
		return atc.Config{}, fmt.Errorf("ticket %d has no target_branch — refusing to guess where to merge", in.Ticket.ID)
	}
	if in.Branch == "" {
		return atc.Config{}, fmt.Errorf("ticket %d has no delivered branch to merge", in.Ticket.ID)
	}
	switch in.Method {
	case "", "merge", "squash":
	default:
		return atc.Config{}, fmt.Errorf("unsupported merge method %q (merge|squash)", in.Method)
	}

	// Gates run against the MERGED tree. Only full-scope gates are
	// enforceable by the v0.5 in-pod engine, matching harvest's boundary —
	// refuse rather than silently drop, so an author is never misled into
	// thinking the merged result was gated when it was not.
	for _, g := range in.GatePolicy.Gates {
		if g.Scope != "full" {
			return atc.Config{}, fmt.Errorf("merge gate %q scope %q is not enforceable (dev-mcp is the wave-3 executor for affected scopes)", g.Gate, g.Scope)
		}
	}

	mergeStep := &atc.MergeStep{
		Name:         "merge",
		Workspace:    "repo",
		Repo:         in.Ticket.Repo,
		TargetBranch: in.Ticket.TargetBranch,
		Branch:       in.Branch,
		TicketID:     in.Ticket.ID,
		Method:       in.Method,
		Message:      in.Message,
		Push:         in.Push,
		GatePolicy:   harvestGatePolicy(in.GatePolicy),
		Env: map[string]string{
			"AGENT_TICKET_ID":       strconv.Itoa(in.Ticket.ID),
			"AGENT_PIPELINE_RUN_ID": "((run_id))",
		},
	}
	if mergeStep.Message == "" {
		mergeStep.Message = fmt.Sprintf("Merge %s into %s (ticket #%d)",
			in.Branch, in.Ticket.TargetBranch, in.Ticket.ID)
	}

	return atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name: "merge",
			PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "repo"}},
				{Config: mergeStep},
			},
		}},
		Resources: atc.ResourceConfigs{{
			Name: "repo",
			Type: "git",
			Source: atc.Source{
				"uri":    in.RepoBaseURL + "/" + in.Ticket.Repo + ".git",
				"branch": in.Ticket.TargetBranch,
			},
		}},
	}, nil
}
