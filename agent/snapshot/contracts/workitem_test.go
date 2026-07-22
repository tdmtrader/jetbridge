package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestWorkItemContractValidatesImmutableCapturedDocument(t *testing.T) {
	workflowVersion := 3
	workflowDefinitionID := 91
	valid := contracts.WorkItemDocument{
		SchemaVersion: "1.0.0",
		Adapter:       "issue-tracker",
		ExternalID:    "ENG-42",
		Revision:      "rev-7",
		CapturedAt:    "2026-07-22T12:00:00Z",
		Title:         "Upgrade database",
		Body:          "Move to the supported release.",
		State:         "open",
		Workflow: &contracts.WorkItemWorkflowSelection{
			Name: "version-upgrade", Version: &workflowVersion, DefinitionID: &workflowDefinitionID,
		},
		Spec: &contracts.WorkItemRevision{Revision: "spec-2", Content: "requirements"},
		Plan: &contracts.WorkItemRevision{Revision: "plan-3", Content: "steps"},
		Comments: []contracts.WorkItemCommentRevision{{
			ExternalID: "comment-9", Revision: "comment-rev-1", Content: "approved",
		}},
	}
	encoded := marshalDocument(t, valid)
	if _, err := validateFiles(t, "work-item/v1", map[string][]byte{"work-item.json": encoded}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid work item error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(*contracts.WorkItemDocument)
		want  string
	}{
		{"wrong version", func(d *contracts.WorkItemDocument) { d.SchemaVersion = "1" }, "1.0.0"},
		{"blank revision", func(d *contracts.WorkItemDocument) { d.Revision = " " }, "revision"},
		{"invalid captured time", func(d *contracts.WorkItemDocument) { d.CapturedAt = "today" }, "captured_at"},
		{"blank optional spec content", func(d *contracts.WorkItemDocument) { d.Spec.Content = "" }, "content"},
		{"workflow version must be positive", func(d *contracts.WorkItemDocument) { zero := 0; d.Workflow.Version = &zero }, "positive"},
		{"pinned workflow requires name", func(d *contracts.WorkItemDocument) { d.Workflow.Name = "" }, "name"},
		{"blank comment revision", func(d *contracts.WorkItemDocument) { d.Comments[0].Revision = "" }, "revision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := valid
			spec, plan := *valid.Spec, *valid.Plan
			workflow := *valid.Workflow
			document.Spec, document.Plan = &spec, &plan
			document.Workflow = &workflow
			document.Comments = append([]contracts.WorkItemCommentRevision(nil), valid.Comments...)
			tc.setup(&document)
			if _, err := validateFiles(t, "work-item/v1", map[string][]byte{"work-item.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWorkItemContractRejectsUnknownAndTrailingJSON(t *testing.T) {
	valid := []byte(`{"schema_version":"1.0.0","adapter":"a","external_id":"1","revision":"r","captured_at":"2026-07-22T12:00:00Z","title":"t","body":"b","state":"open"}`)
	for name, document := range map[string][]byte{
		"unknown":  []byte(`{"schema_version":"1.0.0","adapter":"a","external_id":"1","revision":"r","captured_at":"2026-07-22T12:00:00Z","title":"t","body":"b","state":"open","unknown":true}`),
		"trailing": append(valid, []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateFiles(t, "work-item/v1", map[string][]byte{"work-item.json": document}, emptyValidationContext(t)); err == nil {
				t.Fatal("validation succeeded, want strict JSON error")
			}
		})
	}
}

func marshalDocument(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return encoded
}
