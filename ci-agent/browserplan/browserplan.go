package browserplan

import (
	"context"
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/specparser"
)

// AgentRunner is a general-purpose agent invocation interface.
type AgentRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// GenerateBrowserPlan generates a Markdown browser QA plan for UI requirements.
func GenerateBrowserPlan(ctx context.Context, agent AgentRunner, spec *specparser.Spec, results []schema.RequirementResult, targetURL string) (string, error) {
	// Filter to UI-relevant requirements
	uiItems := filterUIRequirements(spec)
	if len(uiItems) == 0 {
		return "", nil
	}

	if agent == nil {
		return generateStaticPlan(uiItems, targetURL), nil
	}

	prompt := buildBrowserPlanPrompt(uiItems, results, targetURL)
	output, err := agent.Run(ctx, prompt)
	if err != nil {
		// Fallback to static plan on agent error
		return generateStaticPlan(uiItems, targetURL), nil
	}

	return output, nil
}

func filterUIRequirements(spec *specparser.Spec) []specparser.SpecItem {
	uiKeywords := []string{"page", "button", "form", "ui", "click", "display",
		"screen", "modal", "dialog", "input", "select", "table", "list",
		"nav", "menu", "sidebar", "header", "footer", "dashboard", "view",
		"browse", "login", "logout", "signup", "register"}

	var items []specparser.SpecItem
	for _, item := range spec.AllItems() {
		textLower := strings.ToLower(item.Text)
		for _, kw := range uiKeywords {
			if strings.Contains(textLower, kw) {
				items = append(items, item)
				break
			}
		}
	}
	return items
}

func generateStaticPlan(items []specparser.SpecItem, targetURL string) string {
	var sb strings.Builder
	sb.WriteString("# Browser QA Plan\n\n")
	sb.WriteString(fmt.Sprintf("**Target URL:** %s\n\n", targetURL))

	for i, item := range items {
		sb.WriteString(fmt.Sprintf("## Flow %d: %s\n\n", i+1, item.Text))
		sb.WriteString(fmt.Sprintf("**Verifies:** %s\n\n", item.ID))
		sb.WriteString("### Steps\n\n")
		sb.WriteString(fmt.Sprintf("1. Navigate to %s\n", targetURL))
		sb.WriteString(fmt.Sprintf("2. Verify: %s\n\n", item.Text))
	}

	return sb.String()
}

func buildBrowserPlanPrompt(items []specparser.SpecItem, results []schema.RequirementResult, targetURL string) string {
	var sb strings.Builder
	sb.WriteString("Generate a browser QA plan for the following UI requirements.\n\n")
	sb.WriteString(fmt.Sprintf("Target URL: %s\n\n", targetURL))
	sb.WriteString("## UI Requirements\n")
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", item.ID, item.Text))
	}
	sb.WriteString("\nFor each requirement, generate a test flow with:\n")
	sb.WriteString("- Title\n- Verifies (requirement IDs)\n- Numbered steps (navigate, fill, click, assert)\n- Assertions\n\n")
	sb.WriteString("Output as Markdown.\n")
	return sb.String()
}
