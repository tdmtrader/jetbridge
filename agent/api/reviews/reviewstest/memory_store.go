// Package reviewstest provides the in-memory review-projection store the
// reviews API tests and the atc/api suite run against. It lives outside the
// production package so no test double is compiled into the web binary.
package reviewstest

import (
	"context"
	"fmt"
	"github.com/concourse/concourse/agent/api/reviews"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// MemoryStore is an in-memory reviews.Store. It has the same single writer
// as the database store: a review exists because a review/v1 snapshot was
// sealed and projected, never because something posted one.
type MemoryStore struct {
	mu      sync.Mutex
	records []*reviews.StoredReview
}

// The in-memory store is held to exactly the contract the database store
// implements — including the single writer — so a test cannot seed a review the
// platform could never have produced.
var (
	_ reviews.Store            = (*MemoryStore)(nil)
	_ reviews.ProjectionReader = (*MemoryStore)(nil)
	_ reviews.ProjectionWriter = (*MemoryStore)(nil)
)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) UpsertReviewProjection(_ context.Context, rec *reviews.StoredReview) error {
	if rec == nil {
		return fmt.Errorf("reviews: projected review is required")
	}
	if err := rec.SnapshotID.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Store a copy so caller mutation after the upsert can't alter the store.
	cp := *rec
	if cp.CreatedAt == 0 {
		cp.CreatedAt = time.Now().Unix()
	}
	for i, existing := range m.records {
		if existing.SnapshotID == cp.SnapshotID {
			m.records[i] = &cp
			return nil
		}
	}
	m.records = append(m.records, &cp)
	return nil
}

func (m *MemoryStore) GetBySnapshot(teamName string, id snapshot.SnapshotID) (reviews.StoredReview, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.records {
		if rec.SnapshotID == id && rec.TeamName == teamName {
			return *rec, true, nil
		}
	}
	return reviews.StoredReview{}, false, nil
}

func (m *MemoryStore) ListByWorkflowRun(teamName, _ string, id snapshot.WorkflowRunID) ([]reviews.StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []reviews.StoredReview{}
	for _, rec := range m.records {
		if rec.WorkflowRunID != nil && *rec.WorkflowRunID == id && rec.TeamName == teamName {
			results = append(results, *rec)
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].CreatedAt < results[j].CreatedAt })
	return results, nil
}

func (m *MemoryStore) GetByBuild(buildID int) ([]reviews.StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []reviews.StoredReview{}
	for _, rec := range m.records {
		if rec.BuildID == buildID {
			results = append(results, *rec)
		}
	}
	return results, nil
}

func (m *MemoryStore) ListByTeam(team string, filter reviews.ListFilter) ([]reviews.StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []reviews.StoredReview{}
	// Iterate newest-first (reverse insertion order) so Limit keeps the
	// newest N, matching the Postgres implementation's created-descending
	// ordering.
	for i := len(m.records) - 1; i >= 0; i-- {
		rec := m.records[i]
		if rec.TeamName != team {
			continue
		}
		if filter.Pipeline != "" && rec.PipelineName != filter.Pipeline {
			continue
		}
		results = append(results, *rec)
		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}
	return results, nil
}
