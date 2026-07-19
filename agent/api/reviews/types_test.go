package reviews_test

import (
	"encoding/json"
	"strings"
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
		"no build_id":       `{"review": ` + validReview + `}`,
		"negative build_id": `{"build_id": -1, "review": ` + validReview + `}`,
		"no review":         `{"build_id": 42}`,
		"no repo":           `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"commit":"abc"},"score":{"value":1},"summary":"x"}}`,
		"no commit":         `{"build_id": 42, "review": {"schema_version":"1.0.0","metadata":{"repo":"r"},"score":{"value":1},"summary":"x"}}`,
		"bad json":          `{`,
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

func TestMemoryStoreUpsertDistinctKeys(t *testing.T) {
	store := reviews.NewMemoryStore()
	rec := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c1", Score: 5, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	rec2 := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c2", Score: 9, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec2); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByBuild(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records for distinct keys, got %d: %+v", len(got), got)
	}
	// GetByBuild returns oldest-first (insertion order).
	if got[0].CommitSha != "c1" || got[1].CommitSha != "c2" {
		t.Errorf("GetByBuild not oldest-first: %+v", got)
	}
}

func TestMemoryStoreGetByBuildMultipleRecords(t *testing.T) {
	store := reviews.NewMemoryStore()
	records := []*reviews.StoredReview{
		{BuildID: 7, Repo: "repo-a", CommitSha: "aaa", Score: 1, Review: json.RawMessage(`{}`)},
		{BuildID: 7, Repo: "repo-b", CommitSha: "bbb", Score: 2, Review: json.RawMessage(`{}`)},
		{BuildID: 8, Repo: "repo-a", CommitSha: "ccc", Score: 3, Review: json.RawMessage(`{}`)},
	}
	for _, rec := range records {
		if err := store.Upsert(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetByBuild(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records for build 7, got %d: %+v", len(got), got)
	}
	if got[0].Repo != "repo-a" || got[1].Repo != "repo-b" {
		t.Errorf("wrong records or order: %+v", got)
	}
}

func TestMemoryStoreListByTeamNewestFirstWithLimit(t *testing.T) {
	store := reviews.NewMemoryStore()
	records := []*reviews.StoredReview{
		{BuildID: 1, TeamName: "main", Repo: "r", CommitSha: "c1", Score: 1, Review: json.RawMessage(`{}`)},
		{BuildID: 2, TeamName: "main", Repo: "r", CommitSha: "c2", Score: 2, Review: json.RawMessage(`{}`)},
		{BuildID: 3, TeamName: "main", Repo: "r", CommitSha: "c3", Score: 3, Review: json.RawMessage(`{}`)},
	}
	for _, rec := range records {
		if err := store.Upsert(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListByTeam("main", reviews.ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d: %+v", len(got), got)
	}
	// Newest first: Limit keeps the two most recently inserted.
	if got[0].CommitSha != "c3" || got[1].CommitSha != "c2" {
		t.Errorf("ListByTeam not newest-first: %+v", got)
	}
}

func TestMemoryStoreCopiesOnUpsert(t *testing.T) {
	store := reviews.NewMemoryStore()
	rec := &reviews.StoredReview{BuildID: 1, Repo: "r", CommitSha: "c", Score: 5, Review: json.RawMessage(`{}`)}
	if err := store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	rec.Score = 99 // caller mutation after Upsert must not alter the store
	got, err := store.GetByBuild(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Score != 5 {
		t.Errorf("store affected by caller mutation: %+v", got)
	}
	if got[0].CreatedAt == 0 {
		t.Error("CreatedAt not defaulted on upsert")
	}
}

func TestStoredReviewTicketLinkageMarshalling(t *testing.T) {
	tid, prid := 42, 7
	rec := reviews.StoredReview{BuildID: 1, Repo: "o/r", CommitSha: "abc",
		TicketID: &tid, PipelineRunID: &prid}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ticket_id":42`) || !strings.Contains(string(data), `"pipeline_run_id":7`) {
		t.Errorf("linkage fields must marshal when set, got %s", data)
	}

	bare, err := json.Marshal(reviews.StoredReview{BuildID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "ticket_id") || strings.Contains(string(bare), "pipeline_run_id") {
		t.Errorf("nil linkage must omit both fields, got %s", bare)
	}
}

func TestMemoryStoreListByTicketOldestFirst(t *testing.T) {
	store := reviews.NewMemoryStore()
	tid := 42
	for _, rec := range []*reviews.StoredReview{
		{BuildID: 2, Repo: "o/r", CommitSha: "b", TicketID: &tid},
		{BuildID: 1, Repo: "o/r", CommitSha: "a", TicketID: &tid},
		{BuildID: 3, Repo: "o/r", CommitSha: "c"}, // unlinked: excluded
	} {
		if err := store.Upsert(rec); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListByTicket(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].BuildID != 1 || got[1].BuildID != 2 {
		t.Fatalf("want linked reviews oldest-first [1 2], got %+v", got)
	}
}
