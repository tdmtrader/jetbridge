package storage

import (
	"context"
	"sync"

	"github.com/concourse/ci-agent/schema"
)

// FeedbackStore defines the interface for persisting feedback records.
type FeedbackStore interface {
	SaveFeedback(ctx context.Context, rec *schema.FeedbackRecord) error
	GetFeedbackByReview(ctx context.Context, repo, commit string) ([]schema.FeedbackRecord, error)
	GetFeedbackSummary(ctx context.Context, repo string) (*schema.VerdictSummary, error)
}

// feedbackKey is the unique key for upsert logic.
type feedbackKey struct {
	repo      string
	commit    string
	findingID string
	reviewer  string
}

// MemoryFeedbackStore is an in-memory implementation of FeedbackStore for testing.
type MemoryFeedbackStore struct {
	mu      sync.Mutex
	records map[feedbackKey]*schema.FeedbackRecord
}

// NewMemoryFeedbackStore creates a new in-memory feedback store.
func NewMemoryFeedbackStore() FeedbackStore {
	return &MemoryFeedbackStore{
		records: make(map[feedbackKey]*schema.FeedbackRecord),
	}
}

func (m *MemoryFeedbackStore) SaveFeedback(ctx context.Context, rec *schema.FeedbackRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := feedbackKey{
		repo:      rec.ReviewRef.Repo,
		commit:    rec.ReviewRef.Commit,
		findingID: rec.FindingID,
		reviewer:  rec.Reviewer,
	}
	m.records[key] = rec
	return nil
}

func (m *MemoryFeedbackStore) GetFeedbackByReview(ctx context.Context, repo, commit string) ([]schema.FeedbackRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []schema.FeedbackRecord
	for _, rec := range m.records {
		if rec.ReviewRef.Repo == repo && rec.ReviewRef.Commit == commit {
			results = append(results, *rec)
		}
	}
	return results, nil
}

func (m *MemoryFeedbackStore) GetFeedbackSummary(ctx context.Context, repo string) (*schema.VerdictSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	summary := &schema.VerdictSummary{
		ByVerdict:  make(map[string]int),
		ByCategory: make(map[string]int),
		BySeverity: make(map[string]int),
	}

	for _, rec := range m.records {
		if repo != "" && rec.ReviewRef.Repo != repo {
			continue
		}
		summary.Total++
		summary.ByVerdict[string(rec.Verdict)]++
	}

	if summary.Total > 0 {
		accurate := summary.ByVerdict[string(schema.VerdictAccurate)]
		fp := summary.ByVerdict[string(schema.VerdictFalsePositive)]
		summary.AccuracyRate = float64(accurate) / float64(summary.Total)
		summary.FPRate = float64(fp) / float64(summary.Total)
	}

	return summary, nil
}
