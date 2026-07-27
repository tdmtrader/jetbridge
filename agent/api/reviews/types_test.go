package reviews_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/snapshot"
)

func projected(id snapshot.SnapshotID, rec reviews.StoredReview) *reviews.StoredReview {
	production := snapshot.DatabaseID(int64(id))
	rec.SnapshotID = id
	rec.ProductionID = &production
	if rec.Review == nil {
		rec.Review = json.RawMessage(`{}`)
	}
	return &rec
}

func TestMemoryStoreUpsertsProjectedReviewsBySnapshotID(t *testing.T) {
	store := reviews.NewMemoryStore()
	first, second := snapshot.SnapshotID(301), snapshot.SnapshotID(302)
	for _, snapshotID := range []snapshot.SnapshotID{first, second} {
		if err := store.UpsertReviewProjection(context.Background(), projected(snapshotID, reviews.StoredReview{
			TeamName: "main", Conclusion: "inconclusive",
		})); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertReviewProjection(context.Background(), projected(first, reviews.StoredReview{
		TeamName: "main", Conclusion: "accept",
	})); err != nil {
		t.Fatal(err)
	}
	firstReview, found, err := store.GetBySnapshot("main", first)
	if err != nil || !found {
		t.Fatalf("get first: found=%v err=%v", found, err)
	}
	if firstReview.Conclusion != "accept" {
		t.Fatalf("first conclusion = %q, want the re-projected accept", firstReview.Conclusion)
	}
	secondReview, found, err := store.GetBySnapshot("main", second)
	if err != nil || !found || secondReview.Conclusion != "inconclusive" {
		t.Fatalf("second = %#v found=%v err=%v", secondReview, found, err)
	}
}

func TestMemoryStoreRejectsAReviewWithNoSnapshotIdentity(t *testing.T) {
	store := reviews.NewMemoryStore()
	if err := store.UpsertReviewProjection(context.Background(), &reviews.StoredReview{
		TeamName: "main", Conclusion: "accept",
	}); err == nil {
		t.Fatal("a review with no snapshot identity was stored")
	}
}

func TestMemoryStoreGetByBuildKeepsEveryReviewOfTheBuild(t *testing.T) {
	store := reviews.NewMemoryStore()
	for _, rec := range []*reviews.StoredReview{
		projected(701, reviews.StoredReview{BuildID: 7, Summary: "first"}),
		projected(702, reviews.StoredReview{BuildID: 7, Summary: "second"}),
		projected(703, reviews.StoredReview{BuildID: 8, Summary: "other build"}),
	} {
		if err := store.UpsertReviewProjection(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetByBuild(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Summary != "first" || got[1].Summary != "second" {
		t.Fatalf("GetByBuild not oldest-first, or lost a review: %+v", got)
	}
}

func TestMemoryStoreListByTeamNewestFirstWithLimit(t *testing.T) {
	store := reviews.NewMemoryStore()
	for index, id := range []snapshot.SnapshotID{801, 802, 803} {
		if err := store.UpsertReviewProjection(context.Background(), projected(id, reviews.StoredReview{
			TeamName: "main", Summary: string(rune('a' + index)),
		})); err != nil {
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
	// Newest first: Limit keeps the two most recently projected.
	if got[0].Summary != "c" || got[1].Summary != "b" {
		t.Errorf("ListByTeam not newest-first: %+v", got)
	}
}

func TestMemoryStoreCopiesOnUpsert(t *testing.T) {
	store := reviews.NewMemoryStore()
	rec := projected(901, reviews.StoredReview{BuildID: 1, Conclusion: "accept"})
	if err := store.UpsertReviewProjection(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Conclusion = "changes-required" // caller mutation must not alter the store
	got, err := store.GetByBuild(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Conclusion != "accept" {
		t.Errorf("store affected by caller mutation: %+v", got)
	}
	if got[0].CreatedAt == 0 {
		t.Error("CreatedAt not defaulted on upsert")
	}
}

func TestStoredReviewFindingCountTotalsSeverityCounts(t *testing.T) {
	rec := reviews.StoredReview{SeverityCounts: map[string]int{
		"critical": 1, "medium": 2, "observation": 3,
	}}
	if rec.FindingCount() != 6 {
		t.Errorf("FindingCount = %d, want 6", rec.FindingCount())
	}
	if (reviews.StoredReview{}).FindingCount() != 0 {
		t.Error("an empty severity map must count zero findings")
	}
}

func TestStoredReviewMarshalsSnapshotIdentityAndNoLegacyScore(t *testing.T) {
	runID := snapshot.WorkflowRunID(9007199254740995)
	production := snapshot.DatabaseID(9007199254740997)
	data, err := json.Marshal(reviews.StoredReview{
		SnapshotID: 9007199254740993, WorkflowRunID: &runID, ProductionID: &production,
		Conclusion: "changes-required", SeverityCounts: map[string]int{"critical": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Identities are strings on the wire: a float64 round-trip in the browser
	// would silently renumber them.
	for _, want := range []string{
		`"snapshot_id":"9007199254740993"`,
		`"workflow_run_id":"9007199254740995"`,
		`"production_id":"9007199254740997"`,
		`"conclusion":"changes-required"`,
		`"severity_counts":{"critical":1}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	for _, gone := range []string{"score", "max_score", `"pass"`, "agent_model", "duration_seconds", `"repo"`, "commit_sha", "branch", "ticket_id", "pipeline_run_id", "proven_count", "observation_count"} {
		if strings.Contains(string(data), gone) {
			t.Errorf("v1 envelope field %s survived: %s", gone, data)
		}
	}

	bare, err := json.Marshal(reviews.StoredReview{SnapshotID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "workflow_run_id") || strings.Contains(string(bare), "production_id") {
		t.Errorf("absent occurrence identity must be omitted, got %s", bare)
	}
}
