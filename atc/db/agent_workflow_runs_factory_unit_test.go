package db

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

func TestValidateWorkflowRunResourceSourceInputSet(t *testing.T) {
	publicRef := workflowRunResourceSourceTestRef(11, "question/v1", "a")
	sourceRef := workflowRunResourceSourceTestRef(12, "repository/v1", "b")
	public := []workflow.SignaturePort{{
		Name: "question", Type: "question/v1",
	}}
	sources := []workflow.ResourceSource{{
		Name: "repo", Type: "repository/v1",
	}}
	bindings := map[string]snapshot.SnapshotID{"repo": sourceRef.ID}

	tests := []struct {
		name     string
		inputs   map[string]snapshot.SnapshotRef
		public   []workflow.SignaturePort
		sources  []workflow.ResourceSource
		bindings map[string]snapshot.SnapshotID
		wantErr  string
	}{
		{
			name: "exact public and source inputs",
			inputs: map[string]snapshot.SnapshotRef{
				"question": publicRef,
				"repo":     sourceRef,
			},
			public: public, sources: sources, bindings: bindings,
		},
		{
			name: "authorized surplus input",
			inputs: map[string]snapshot.SnapshotRef{
				"question": publicRef,
				"repo":     sourceRef,
				"surplus":  workflowRunResourceSourceTestRef(13, "repository/v1", "c"),
			},
			public: public, sources: sources, bindings: bindings,
			wantErr: "neither a public input nor a source binding",
		},
		{
			name: "missing supplied source",
			inputs: map[string]snapshot.SnapshotRef{
				"question": publicRef,
			},
			public: public, sources: sources, bindings: bindings,
			wantErr: "differ from ready admission bindings",
		},
		{
			name: "mismatched supplied source ID",
			inputs: map[string]snapshot.SnapshotRef{
				"question": publicRef,
				"repo":     workflowRunResourceSourceTestRef(14, "repository/v1", "d"),
			},
			public: public, sources: sources, bindings: bindings,
			wantErr: "differ from ready admission bindings",
		},
		{
			name: "surplus admission binding",
			inputs: map[string]snapshot.SnapshotRef{
				"question": publicRef,
				"repo":     sourceRef,
			},
			public: public, sources: sources,
			bindings: map[string]snapshot.SnapshotID{
				"repo": sourceRef.ID, "undeclared": 15,
			},
			wantErr: "differ from workflow source declarations",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWorkflowRunResourceSourceInputSet(
				test.inputs,
				test.public,
				test.sources,
				test.bindings,
			)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validate exact source input set: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func workflowRunResourceSourceTestRef(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	digestCharacter string,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID:   id,
		Type: typ,
		Digest: snapshot.Digest(
			"sha256:" + strings.Repeat(digestCharacter, 64),
		),
	}
}
