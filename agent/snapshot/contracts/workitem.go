package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type WorkItemDocument struct {
	SchemaVersion string                     `json:"schema_version"`
	Adapter       string                     `json:"adapter"`
	ExternalID    string                     `json:"external_id"`
	Revision      string                     `json:"revision"`
	CapturedAt    string                     `json:"captured_at"`
	Title         string                     `json:"title"`
	Body          string                     `json:"body"`
	State         string                     `json:"state"`
	Workflow      *WorkItemWorkflowSelection `json:"workflow,omitempty"`
	Spec          *WorkItemRevision          `json:"spec,omitempty"`
	Plan          *WorkItemRevision          `json:"plan,omitempty"`
	Comments      []WorkItemCommentRevision  `json:"comments,omitempty"`
}

type WorkItemWorkflowSelection struct {
	Name         string `json:"name,omitempty"`
	Version      *int   `json:"version,omitempty"`
	DefinitionID *int   `json:"definition_id,omitempty"`
}

func (selection WorkItemWorkflowSelection) validate() error {
	if selection.Version != nil && *selection.Version <= 0 {
		return fmt.Errorf("workflow.version must be positive")
	}
	if selection.DefinitionID != nil && *selection.DefinitionID <= 0 {
		return fmt.Errorf("workflow.definition_id must be positive")
	}
	if (selection.Version != nil || selection.DefinitionID != nil) && strings.TrimSpace(selection.Name) == "" {
		return fmt.Errorf("workflow.name is required when a version or definition is selected")
	}
	return nil
}

type WorkItemRevision struct {
	Revision string `json:"revision"`
	Content  string `json:"content"`
}

type WorkItemCommentRevision struct {
	ExternalID string `json:"external_id"`
	Revision   string `json:"revision"`
	Content    string `json:"content"`
}

func (d WorkItemDocument) Validate() error {
	if d.SchemaVersion != "1.0.0" {
		return fmt.Errorf("schema_version must be exactly 1.0.0")
	}
	for name, value := range map[string]string{
		"adapter": d.Adapter, "external_id": d.ExternalID, "revision": d.Revision,
		"captured_at": d.CapturedAt, "title": d.Title, "body": d.Body, "state": d.State,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if _, err := time.Parse(time.RFC3339, d.CapturedAt); err != nil {
		return fmt.Errorf("captured_at must be RFC3339: %w", err)
	}
	if d.Spec != nil {
		if err := d.Spec.validate("spec"); err != nil {
			return err
		}
	}
	if d.Workflow != nil {
		if err := d.Workflow.validate(); err != nil {
			return err
		}
	}
	if d.Plan != nil {
		if err := d.Plan.validate("plan"); err != nil {
			return err
		}
	}
	for i, comment := range d.Comments {
		for name, value := range map[string]string{
			"external_id": comment.ExternalID, "revision": comment.Revision, "content": comment.Content,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("comments[%d].%s is required", i, name)
			}
		}
	}
	return nil
}

func (r WorkItemRevision) validate(name string) error {
	if strings.TrimSpace(r.Revision) == "" {
		return fmt.Errorf("%s.revision is required", name)
	}
	if strings.TrimSpace(r.Content) == "" {
		return fmt.Errorf("%s.content is required", name)
	}
	return nil
}

type workItemValidator struct{}

func (workItemValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	var document WorkItemDocument
	if err := decodeStrictDocument(ctx, root, "work-item.json", &document); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := document.Validate(); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: work-item.json: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
