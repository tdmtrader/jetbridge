package adapter

import (
	"context"
	"fmt"

	"github.com/concourse/ci-agent/schema"
)

// Adapter generates spec and plan documents from a planning input.
type Adapter interface {
	GenerateSpec(ctx context.Context, input *schema.PlanningInput, opts SpecOpts) (*SpecOutput, error)
	GeneratePlan(ctx context.Context, input *schema.PlanningInput, specMarkdown string, opts PlanOpts) (*PlanOutput, error)
}

// SpecOpts configures spec generation.
type SpecOpts struct {
	Model string
}

// PlanOpts configures plan generation.
type PlanOpts struct {
	Model string
}

// SpecOutput is the structured result of spec generation.
type SpecOutput struct {
	SpecMarkdown        string   `json:"spec_markdown"`
	UnresolvedQuestions []string `json:"unresolved_questions,omitempty"`
	Assumptions         []string `json:"assumptions,omitempty"`
	OutOfScope          []string `json:"out_of_scope,omitempty"`
}

// Validate checks that a SpecOutput has required fields.
func (s *SpecOutput) Validate() error {
	if s.SpecMarkdown == "" {
		return fmt.Errorf("spec_markdown is required")
	}
	return nil
}

// PlanOutput is the structured result of plan generation.
type PlanOutput struct {
	PlanMarkdown string    `json:"plan_markdown"`
	Phases       []Phase   `json:"phases"`
	KeyFiles     []KeyFile `json:"key_files,omitempty"`
	Risks        []string  `json:"risks,omitempty"`
}

// Validate checks that a PlanOutput has required fields.
func (p *PlanOutput) Validate() error {
	if p.PlanMarkdown == "" {
		return fmt.Errorf("plan_markdown is required")
	}
	if len(p.Phases) == 0 {
		return fmt.Errorf("phases is required")
	}
	return nil
}

// Phase is a group of related tasks.
type Phase struct {
	Name  string `json:"name"`
	Tasks []Task `json:"tasks"`
}

// Task is a single unit of work.
type Task struct {
	Description string   `json:"description"`
	Files       []string `json:"files,omitempty"`
}

// KeyFile identifies a file that will be created or modified.
type KeyFile struct {
	Path   string `json:"path"`
	Change string `json:"change"` // "NEW" or "MODIFY"
}
