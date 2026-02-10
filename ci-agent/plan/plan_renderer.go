package plan

import (
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

// RenderPlan renders a PlanOutput to Markdown with phases, tasks, key files, and risks.
func RenderPlan(input *schema.PlanningInput, p *adapter.PlanOutput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Implementation Plan: %s\n\n", input.Title))

	for _, phase := range p.Phases {
		sb.WriteString(fmt.Sprintf("## %s\n\n", phase.Name))
		for _, task := range phase.Tasks {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", task.Description))
			if len(task.Files) > 0 {
				sb.WriteString(fmt.Sprintf("  - Files: %s\n", strings.Join(task.Files, ", ")))
			}
		}
		sb.WriteString("\n")
	}

	if len(p.KeyFiles) > 0 {
		sb.WriteString("## Key Files\n\n")
		sb.WriteString("| File | Change |\n")
		sb.WriteString("|------|--------|\n")
		for _, kf := range p.KeyFiles {
			sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", kf.Path, kf.Change))
		}
		sb.WriteString("\n")
	}

	if len(p.Risks) > 0 {
		sb.WriteString("## Risks\n\n")
		for _, r := range p.Risks {
			sb.WriteString(fmt.Sprintf("- %s\n", r))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
