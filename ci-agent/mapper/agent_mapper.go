package mapper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/specparser"
)

// AgentRunner is a general-purpose agent invocation interface.
type AgentRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// RefineMapping uses an agent to refine the initial automated mapping.
func RefineMapping(ctx context.Context, a AgentRunner, spec *specparser.Spec, index *TestIndex, initialMap []RequirementMapping) ([]RequirementMapping, error) {
	if a == nil {
		return initialMap, nil
	}

	prompt := buildRefinementPrompt(spec, index, initialMap)
	output, err := a.Run(ctx, prompt)
	if err != nil {
		// Graceful degradation: return initial mapping on adapter error
		return initialMap, nil
	}

	refined, err := parseRefinedMappings(output, initialMap)
	if err != nil {
		return initialMap, nil
	}

	return refined, nil
}

func buildRefinementPrompt(spec *specparser.Spec, index *TestIndex, mappings []RequirementMapping) string {
	var sb strings.Builder
	sb.WriteString("Review and refine the following requirement-to-test mappings.\n\n")

	sb.WriteString("## Requirements\n")
	for _, item := range spec.AllItems() {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", item.ID, item.Text))
	}

	sb.WriteString("\n## Current Mappings\n")
	for _, m := range mappings {
		sb.WriteString(fmt.Sprintf("- %s [%s]: %d matches\n", m.SpecItem.ID, m.Status, len(m.Matches)))
	}

	sb.WriteString(`
Respond with a JSON array of refined mappings:
[{"id":"R1","status":"covered|partial|uncovered","reason":"..."}]
`)
	return sb.String()
}

type refinedEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func parseRefinedMappings(output string, initial []RequirementMapping) ([]RequirementMapping, error) {
	var entries []refinedEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return nil, fmt.Errorf("parse refined mappings: %w", err)
	}

	entryMap := make(map[string]refinedEntry)
	for _, e := range entries {
		entryMap[e.ID] = e
	}

	result := make([]RequirementMapping, len(initial))
	copy(result, initial)

	for i, m := range result {
		if e, ok := entryMap[m.SpecItem.ID]; ok {
			if e.Status == "covered" || e.Status == "partial" || e.Status == "uncovered" {
				result[i].Status = e.Status
			}
		}
	}

	return result, nil
}
