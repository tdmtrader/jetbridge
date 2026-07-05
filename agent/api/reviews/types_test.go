package reviews_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/api/reviews"
)

const validReview = `{
	"schema_version": "1.0.0",
	"metadata": {"repo": "concourse", "commit": "abc123", "branch": "jetbridge", "agent_model": "claude-sonnet-5", "duration_seconds": 120},
	"score": {"value": 7.5, "max": 10, "pass": true, "threshold": 7.0},
	"proven_issues": [{"id": "PI-1", "severity": "high", "title": "nil deref", "file": "a.go", "line": 10, "category": "correctness", "test_file": "a_test.go", "test_name": "TestNil"}],
	"observations": [{"id": "OB-1", "title": "long func", "file": "b.go", "line": 5, "category": "maintainability"}],
	"summary": "one bug"
}`

func TestParseSubmission(t *testing.T) {
	body := `{"build_id": 42, "review": ` + validReview + `}`
	sub, err := reviews.ParseSubmission([]byte(body))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sub.BuildID != 42 {
		t.Errorf("build id = %d, want 42", sub.BuildID)
	}
	if sub.Payload.Metadata.Repo != "concourse" {
		t.Errorf("repo = %q", sub.Payload.Metadata.Repo)
	}
	if len(sub.Payload.ProvenIssues) != 1 || len(sub.Payload.Observations) != 1 {
		t.Errorf("issue counts wrong: %d/%d", len(sub.Payload.ProvenIssues), len(sub.Payload.Observations))
	}
}

func TestParseSubmissionRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no build_id": `{"review": ` + validReview + `}`,
		"no review":   `{"build_id": 42}`,
		"no repo":     `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"commit":"abc"},"score":{"value":1},"summary":"x"}}`,
		"no commit":   `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"repo":"r"},"score":{"value":1},"summary":"x"}}`,
		"bad json":    `{`,
	}
	for name, body := range cases {
		if _, err := reviews.ParseSubmission([]byte(body)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestStoredReviewFromSubmission(t *testing.T) {
	body := `{"build_id": 42, "review": ` + validReview + `}`
	sub, err := reviews.ParseSubmission([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	rec := sub.ToStoredReview(reviews.BuildContext{
		BuildName: "3", TeamName: "main", PipelineName: "concourse-self", JobName: "agent-review",
	})
	if rec.BuildID != 42 || rec.TeamName != "main" || rec.JobName != "agent-review" {
		t.Errorf("build context not applied: %+v", rec)
	}
	if rec.Score != 7.5 || !rec.Pass || rec.ProvenCount != 1 || rec.ObservationCount != 1 {
		t.Errorf("denormalized fields wrong: %+v", rec)
	}
	if !json.Valid(rec.Review) {
		t.Error("raw review payload not preserved")
	}
}

func TestMemoryStoreUpsert(t *testing.T) {
	store := reviews.NewMemoryStore()
	rec := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c", Score: 5, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	rec2 := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c", Score: 9, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec2); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByBuild(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Score != 9 {
		t.Errorf("upsert did not replace: %+v", got)
	}
}
