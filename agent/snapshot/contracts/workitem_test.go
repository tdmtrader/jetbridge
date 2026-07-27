package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestWorkItemContractValidatesImmutableCapturedDocument(t *testing.T) {
	valid := contracts.WorkItemDocument{
		SchemaVersion: "1.0.0",
		Adapter:       "issue-tracker",
		ExternalID:    "ENG-42",
		Revision:      "rev-7",
		CapturedAt:    "2026-07-22T12:00:00Z",
		Title:         "Upgrade database",
		Body:          "Move to the supported release.",
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
		{"blank body", func(d *contracts.WorkItemDocument) { d.Body = "  " }, "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := valid
			tc.setup(&document)
			if _, err := validateFiles(t, "work-item/v1", map[string][]byte{"work-item.json": marshalDocument(t, document)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

// The document describes the work item's authored content, not the consumer that
// picks it up, and its prose lives in one place. A capture that still carried the
// ticket's lifecycle state, the selected workflow, or a separate spec/plan
// sub-document would be a second truth, so a strict decode must reject every one
// of those keys outright rather than tolerating them.
func TestWorkItemContractRejectsTheFieldsItNoLongerEmbeds(t *testing.T) {
	base := `{"schema_version":"1.0.0","adapter":"a","external_id":"1","revision":"r","captured_at":"2026-07-22T12:00:00Z","title":"t","body":"b"`
	for name, document := range map[string][]byte{
		"state":    []byte(base + `,"state":"open"}`),
		"workflow": []byte(base + `,"workflow":{"name":"version-upgrade","version":3}}`),
		"spec":     []byte(base + `,"spec":{"revision":"spec-2","content":"requirements"}}`),
		"plan":     []byte(base + `,"plan":{"revision":"plan-3","content":"steps"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateFiles(t, "work-item/v1", map[string][]byte{"work-item.json": document}, emptyValidationContext(t)); err == nil ||
				!strings.Contains(err.Error(), name) {
				t.Fatalf("validation error = %v, want a strict-decode rejection naming %q", err, name)
			}
		})
	}
}

func TestWorkItemContractRejectsUnknownAndTrailingJSON(t *testing.T) {
	valid := []byte(`{"schema_version":"1.0.0","adapter":"a","external_id":"1","revision":"r","captured_at":"2026-07-22T12:00:00Z","title":"t","body":"b"}`)
	for name, document := range map[string][]byte{
		"unknown":  []byte(`{"schema_version":"1.0.0","adapter":"a","external_id":"1","revision":"r","captured_at":"2026-07-22T12:00:00Z","title":"t","body":"b","unknown":true}`),
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
