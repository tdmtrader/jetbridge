package reviews_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
)

const validReview = `{
	"schema_version": "1.0.0",
	"metadata": {"repo": "concourse", "commit": "abc123", "branch": "jetbridge", "agent_model": "claude-sonnet-5", "duration_seconds": 120},
	"score": {"value": 7.5, "max": 10, "pass": true, "threshold": 7.0},
	"proven_issues": [{"id": "PI-1", "severity": "high", "title": "nil deref", "file": "a.go", "line": 10, "category": "correctness", "test_file": "a_test.go", "test_name": "TestNil"}],
	"observations": [{"id": "OB-1", "title": "long func", "file": "b.go", "line": 5, "category": "maintainability"}],
	"summary": "one bug"
}`

func TestMemoryStoreUpsertsProjectedReviewsBySnapshotID(t *testing.T) {
	store := reviews.NewMemoryStore()
	first, second := snapshot.SnapshotID(301), snapshot.SnapshotID(302)
	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		if err := store.Upsert(&reviews.StoredReview{
			SnapshotID: &snapshotID, TeamName: "main", Repo: "same", CommitSha: "same", Score: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Upsert(&reviews.StoredReview{
		SnapshotID: &first, TeamName: "main", Repo: "same", CommitSha: "same", Score: 9,
	}); err != nil {
		t.Fatal(err)
	}
	firstReview, found, err := store.GetBySnapshot("main", first)
	if err != nil || !found {
		t.Fatalf("get first: found=%v err=%v", found, err)
	}
	if firstReview.Score != 9 {
		t.Fatalf("first score = %v, want upserted 9", firstReview.Score)
	}
	secondReview, found, err := store.GetBySnapshot("main", second)
	if err != nil || !found || secondReview.Score != 1 {
		t.Fatalf("second = %#v found=%v err=%v", secondReview, found, err)
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
