package contracts

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// WorkItemDocument is the immutable capture of one work item's authored content
// at one revision.
//
// It deliberately carries NEITHER the ticket's lifecycle state NOR the identity
// of the workflow that consumes it. Both are properties of the consumer, not of
// the value: the durable workflow run already records which function ran over
// which snapshot, and embedding a mutable state or a workflow selection here
// would freeze a second, immediately stale copy of that truth inside a value
// whose whole point is that it never changes.
//
// It also carries no separate spec/plan sub-documents. Those mirrored the
// agent_ticket_specs / agent_ticket_tasks tables, which are gone: a work item's
// prose lives in exactly one place, its body.
type WorkItemDocument struct {
	SchemaVersion string `json:"schema_version"`
	Adapter       string `json:"adapter"`
	ExternalID    string `json:"external_id"`
	Revision      string `json:"revision"`
	CapturedAt    string `json:"captured_at"`
	Title         string `json:"title"`
	Body          string `json:"body"`
}

func (d WorkItemDocument) Validate() error {
	if d.SchemaVersion != "1.0.0" {
		return fmt.Errorf("schema_version must be exactly 1.0.0")
	}
	for name, value := range map[string]string{
		"adapter": d.Adapter, "external_id": d.ExternalID, "revision": d.Revision,
		"captured_at": d.CapturedAt, "title": d.Title, "body": d.Body,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if _, err := time.Parse(time.RFC3339, d.CapturedAt); err != nil {
		return fmt.Errorf("captured_at must be RFC3339: %w", err)
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
