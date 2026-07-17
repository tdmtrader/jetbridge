package tickets

import (
	"fmt"
	"strings"
)

// RenderSpecMarkdown produces the read-only spec.md workspace input for
// a ticket. Dispatch's renderer materializes it into run inputs at
// render time. Deterministic: identical rows produce identical bytes
// (golden-tested).
func RenderSpecMarkdown(t Ticket, spec *Spec) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ticket #%d: %s\n\n", t.ID, t.Title)
	fmt.Fprintf(&b, "- repo: %s\n", t.Repo)
	fmt.Fprintf(&b, "- target branch: %s\n", t.TargetBranch)
	fmt.Fprintf(&b, "- origin: %s\n", t.Origin)
	if t.WorkflowName != "" {
		if t.WorkflowVersion != nil {
			fmt.Fprintf(&b, "- workflow: %s v%d\n", t.WorkflowName, *t.WorkflowVersion)
		} else {
			fmt.Fprintf(&b, "- workflow: %s (live)\n", t.WorkflowName)
		}
	}
	b.WriteString("\n## Problem statement\n\n")
	b.WriteString(strings.TrimRight(t.Body, "\n"))
	b.WriteString("\n")

	if spec != nil {
		fmt.Fprintf(&b, "\n## Spec v%d: %s\n\n", spec.Version, spec.Title)
		b.WriteString(strings.TrimRight(spec.Body, "\n"))
		b.WriteString("\n")
		if len(spec.AcceptanceCriteria) > 0 {
			b.WriteString("\n### Acceptance criteria\n\n")
			for _, c := range spec.AcceptanceCriteria {
				fmt.Fprintf(&b, "- [ ] %s\n", c)
			}
		}
		if len(spec.Links) > 0 {
			b.WriteString("\n### Links\n\n")
			for _, l := range spec.Links {
				fmt.Fprintf(&b, "- [%s](%s)\n", l.Title, l.URL)
			}
		}
	}
	return []byte(b.String())
}

// taskGlyph maps a task status to its plan.md checkbox glyph (contract
// addendum): pending [ ], in_progress [~], done [x], skipped [-],
// blocked [!].
func taskGlyph(s TaskStatus) string {
	switch s {
	case TaskDone:
		return "[x]"
	case TaskInProgress:
		return "[~]"
	case TaskSkipped:
		return "[-]"
	case TaskBlocked:
		return "[!]"
	default:
		return "[ ]"
	}
}

// RenderPlanMarkdown produces the read-only plan.md workspace input
// from the ticket's active plan (tasks ordered by Ordering, as
// Store.ActivePlan returns them).
func RenderPlanMarkdown(t Ticket, tasks []Task) []byte {
	var b strings.Builder
	if len(tasks) == 0 {
		fmt.Fprintf(&b, "# Plan — ticket #%d\n\nNo plan submitted yet.\n", t.ID)
		return []byte(b.String())
	}
	fmt.Fprintf(&b, "# Plan v%d — ticket #%d\n\n", tasks[0].PlanVersion, t.ID)
	for _, task := range tasks {
		fmt.Fprintf(&b, "- %s %d. %s\n", taskGlyph(task.Status), task.Ordering, task.Title)
		if task.Detail != "" {
			for _, line := range strings.Split(strings.TrimRight(task.Detail, "\n"), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	return []byte(b.String())
}
