package exec

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestRecordAuthorityEnvUsesExactInputsAndBuiltInSchemaIdentity(t *testing.T) {
	inputs := snapshotInputBindings{
		order: []string{"change"},
		refs: map[string]snapshot.SnapshotRef{
			"change": {
				ID: 7, Type: "repository-change/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
			},
		},
	}
	rows, err := recordAuthorityEnv(inputs, map[string]snapshot.TypeRef{
		"review": "review/v1",
		"plain":  "opaque/v1",
	}, []string{"EXISTING=value"})
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := recordSchemaIdentity("review/v1")
	want := []string{
		"AGENT_INPUT_CHANGE_SNAPSHOT_DIGEST=sha256:" + strings.Repeat("a", 64),
		"AGENT_INPUT_CHANGE_SNAPSHOT_TYPE=repository-change/v1",
		"AGENT_OUTPUT_REVIEW_RECORD_SCHEMA=" + schema,
		"AGENT_OUTPUT_REVIEW_RECORD_TYPE=review/v1",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

func TestRecordAuthorityEnvRejectsAuthoredAndNormalizedNameCollisions(t *testing.T) {
	inputs := snapshotInputBindings{
		order: []string{"change"},
		refs: map[string]snapshot.SnapshotRef{
			"change": {
				ID: 7, Type: "repository-change/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
			},
		},
	}
	if _, err := recordAuthorityEnv(inputs, nil, []string{
		"AGENT_INPUT_CHANGE_SNAPSHOT_TYPE=forged",
	}); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("authored collision error = %v", err)
	}
	if _, err := recordAuthorityEnv(snapshotInputBindings{}, map[string]snapshot.TypeRef{
		"review-result": "review/v1",
		"review_result": "review/v1",
	}, nil); err == nil || !strings.Contains(err.Error(), "normalize") {
		t.Fatalf("normalized collision error = %v", err)
	}
}
