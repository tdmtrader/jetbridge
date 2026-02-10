package plan

import (
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

// RenderSpec renders a SpecOutput to Markdown with standard sections.
func RenderSpec(input *schema.PlanningInput, spec *adapter.SpecOutput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s\n\n", input.Title))
	sb.WriteString("## Overview\n\n")
	sb.WriteString(spec.SpecMarkdown)
	sb.WriteString("\n\n")

	if len(input.AcceptanceCriteria) > 0 {
		sb.WriteString("## Acceptance Criteria\n\n")
		for _, ac := range input.AcceptanceCriteria {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", ac))
		}
		sb.WriteString("\n")
	}

	if len(spec.Assumptions) > 0 {
		sb.WriteString("## Assumptions\n\n")
		for _, a := range spec.Assumptions {
			sb.WriteString(fmt.Sprintf("- %s\n", a))
		}
		sb.WriteString("\n")
	}

	if len(spec.OutOfScope) > 0 {
		sb.WriteString("## Out of Scope\n\n")
		for _, o := range spec.OutOfScope {
			sb.WriteString(fmt.Sprintf("- %s\n", o))
		}
		sb.WriteString("\n")
	}

	if len(spec.UnresolvedQuestions) > 0 {
		sb.WriteString("## Unresolved Questions\n\n")
		for _, q := range spec.UnresolvedQuestions {
			sb.WriteString(fmt.Sprintf("- %s\n", q))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
