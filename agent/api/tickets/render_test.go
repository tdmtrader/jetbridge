package tickets_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func TestRenderSpecMarkdown(t *testing.T) {
	version := 2
	ticket := tickets.Ticket{
		ID: 12, Title: "fix flaky spec", Body: "worker_cache_test flakes under load",
		Origin: "fly", Repo: "tdmtrader/concourse", TargetBranch: "jetbridge",
		WorkflowName: "standard-dev", WorkflowVersion: &version,
	}
	spec := &tickets.Spec{
		Version: 2, Title: "Deflake worker cache spec",
		Body:               "Root cause: refresh interval racing the clock.",
		AcceptanceCriteria: []string{"suite green 10x", "no timeout bumps"},
		Links:              []tickets.Link{{Title: "flake log", URL: "https://ci/build/9"}},
	}

	got := string(tickets.RenderSpecMarkdown(ticket, spec))
	want := `# Ticket #12: fix flaky spec

- repo: tdmtrader/concourse
- target branch: jetbridge
- origin: fly
- workflow: standard-dev v2

## Problem statement

worker_cache_test flakes under load

## Spec v2: Deflake worker cache spec

Root cause: refresh interval racing the clock.

### Acceptance criteria

- [ ] suite green 10x
- [ ] no timeout bumps

### Links

- [flake log](https://ci/build/9)
`
	if got != want {
		t.Errorf("RenderSpecMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// specless ticket: envelope + problem statement only
	got = string(tickets.RenderSpecMarkdown(tickets.Ticket{
		ID: 3, Title: "t", Body: "b", Origin: "web", Repo: "r", TargetBranch: "main",
	}, nil))
	want = `# Ticket #3: t

- repo: r
- target branch: main
- origin: web

## Problem statement

b
`
	if got != want {
		t.Errorf("specless mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderPlanMarkdown(t *testing.T) {
	ticket := tickets.Ticket{ID: 12}
	got := string(tickets.RenderPlanMarkdown(ticket, []tickets.Task{
		{PlanVersion: 3, Ordering: 1, Title: "write failing test", Status: tickets.TaskDone},
		{PlanVersion: 3, Ordering: 2, Title: "fix the race", Status: tickets.TaskInProgress,
			Detail: "clock injection\nsecond line"},
		{PlanVersion: 3, Ordering: 3, Title: "run suite 10x", Status: tickets.TaskPending},
		{PlanVersion: 3, Ordering: 4, Title: "skipped idea", Status: tickets.TaskSkipped},
		{PlanVersion: 3, Ordering: 5, Title: "blocked on infra", Status: tickets.TaskBlocked},
	}))
	want := `# Plan v3 — ticket #12

- [x] 1. write failing test
- [~] 2. fix the race
  clock injection
  second line
- [ ] 3. run suite 10x
- [-] 4. skipped idea
- [!] 5. blocked on infra
`
	if got != want {
		t.Errorf("RenderPlanMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	got = string(tickets.RenderPlanMarkdown(ticket, nil))
	want = "# Plan — ticket #12\n\nNo plan submitted yet.\n"
	if got != want {
		t.Errorf("empty plan mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}
