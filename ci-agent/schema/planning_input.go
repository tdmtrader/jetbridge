package schema

import (
	"fmt"
	"strings"
)

// Priority for a planning input.
type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

var validPriorities = map[Priority]bool{
	PriorityCritical: true,
	PriorityHigh:     true,
	PriorityMedium:   true,
	PriorityLow:      true,
}

// StoryType classifies the kind of work.
type StoryType string

const (
	StoryFeature StoryType = "feature"
	StoryBug     StoryType = "bug"
	StoryChore   StoryType = "chore"
	StorySpike   StoryType = "spike"
)

var validStoryTypes = map[StoryType]bool{
	StoryFeature: true,
	StoryBug:     true,
	StoryChore:   true,
	StorySpike:   true,
}

// PlanningContext provides optional context for planning.
type PlanningContext struct {
	Repo         string   `json:"repo,omitempty"`
	Language     string   `json:"language,omitempty"`
	RelatedFiles []string `json:"related_files,omitempty"`
}

// PlanningInput is the input schema for the planning agent.
type PlanningInput struct {
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	Type               StoryType       `json:"type,omitempty"`
	Priority           Priority        `json:"priority,omitempty"`
	Labels             []string        `json:"labels,omitempty"`
	AcceptanceCriteria []string        `json:"acceptance_criteria,omitempty"`
	Context            *PlanningContext `json:"context,omitempty"`
}

// Validate checks that all required PlanningInput fields are present.
func (p *PlanningInput) Validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(p.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if p.Type != "" && !validStoryTypes[p.Type] {
		return fmt.Errorf("invalid type %q", p.Type)
	}
	if p.Priority != "" && !validPriorities[p.Priority] {
		return fmt.Errorf("invalid priority %q", p.Priority)
	}
	return nil
}
