package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc"
)

func mergeInput() dispatch.MergeRenderInput {
	return dispatch.MergeRenderInput{
		Ticket: tickets.Ticket{
			ID: 42, Repo: "tdmtrader/jetbridge", TargetBranch: "jetbridge",
		},
		Branch:      "agent/ticket-42",
		Push:        true,
		RepoBaseURL: "https://github.com",
	}
}

func mergeStepOf(t *testing.T, cfg atc.Config) *atc.MergeStep {
	t.Helper()
	for _, s := range cfg.Jobs[0].PlanSequence {
		if ms, ok := s.Config.(*atc.MergeStep); ok {
			return ms
		}
	}
	t.Fatal("no merge step in the rendered plan")
	return nil
}

func TestRenderMergeProducesGetThenMerge(t *testing.T) {
	cfg, err := dispatch.RenderMerge(mergeInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Jobs) != 1 {
		t.Fatalf("expected exactly one job, got %d", len(cfg.Jobs))
	}
	plan := cfg.Jobs[0].PlanSequence
	if len(plan) != 2 {
		t.Fatalf("expected get+merge, got %d steps", len(plan))
	}
	get, ok := plan[0].Config.(*atc.GetStep)
	if !ok || get.Name != "repo" {
		t.Fatalf("first step must be `get: repo`, got %#v", plan[0].Config)
	}
	ms := mergeStepOf(t, cfg)
	if ms.Workspace != "repo" {
		t.Fatalf("merge must consume the repo artifact, got %q", ms.Workspace)
	}
}

// The resource must track the TARGET branch: the merge runner fetches all
// refs from it, and the push lands there.
func TestRenderMergeResourceTracksTheTargetBranch(t *testing.T) {
	cfg, err := dispatch.RenderMerge(mergeInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Resources) != 1 {
		t.Fatalf("expected one resource, got %d", len(cfg.Resources))
	}
	r := cfg.Resources[0]
	if r.Name != "repo" || r.Type != "git" {
		t.Fatalf("expected a git resource named repo, got %+v", r)
	}
	if r.Source["branch"] != "jetbridge" {
		t.Fatalf("resource must track the target branch, got %v", r.Source["branch"])
	}
	if r.Source["uri"] != "https://github.com/tdmtrader/jetbridge.git" {
		t.Fatalf("unexpected uri %v", r.Source["uri"])
	}
}

func TestRenderMergeCarriesTicketIdentity(t *testing.T) {
	cfg, err := dispatch.RenderMerge(mergeInput())
	if err != nil {
		t.Fatal(err)
	}
	ms := mergeStepOf(t, cfg)
	if ms.TicketID != 42 || ms.Branch != "agent/ticket-42" || ms.TargetBranch != "jetbridge" {
		t.Fatalf("identity not carried: %+v", ms)
	}
	if !ms.Push {
		t.Fatal("Push must be carried through")
	}
	if ms.Env["AGENT_TICKET_ID"] != "42" {
		t.Fatalf("expected AGENT_TICKET_ID env row, got %v", ms.Env)
	}
}

// Speculative render: same shape, no push. This is what a "would this land?"
// check runs.
func TestRenderMergeSpeculative(t *testing.T) {
	in := mergeInput()
	in.Push = false
	cfg, err := dispatch.RenderMerge(in)
	if err != nil {
		t.Fatal(err)
	}
	if mergeStepOf(t, cfg).Push {
		t.Fatal("a speculative render must not set Push")
	}
}

func TestRenderMergeRefusesIncompleteInput(t *testing.T) {
	for name, mutate := range map[string]func(*dispatch.MergeRenderInput){
		"no ticket":  func(in *dispatch.MergeRenderInput) { in.Ticket.ID = 0 },
		"no repo":    func(in *dispatch.MergeRenderInput) { in.Ticket.Repo = "" },
		"no target":  func(in *dispatch.MergeRenderInput) { in.Ticket.TargetBranch = "" },
		"no branch":  func(in *dispatch.MergeRenderInput) { in.Branch = "" },
		"bad method": func(in *dispatch.MergeRenderInput) { in.Method = "rebase" },
	} {
		in := mergeInput()
		mutate(&in)
		if _, err := dispatch.RenderMerge(in); err == nil {
			t.Errorf("%s: expected a render refusal", name)
		}
	}
}
