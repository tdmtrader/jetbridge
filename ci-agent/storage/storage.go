package storage

import (
	"context"

	"github.com/concourse/ci-agent/schema"
)

// Store defines the interface for persisting review outputs.
type Store interface {
	SaveReview(ctx context.Context, review *schema.ReviewOutput) error
	GetReview(ctx context.Context, repo, commit string) (*schema.ReviewOutput, error)
	ListReviews(ctx context.Context, repo string, limit int) ([]*schema.ReviewOutput, error)
}

// NewStore creates a Store based on the database URL.
// Returns a NoopStore when databaseURL is empty.
func NewStore(databaseURL string) (Store, error) {
	if databaseURL == "" {
		return &NoopStore{}, nil
	}
	// PostgreSQL implementation deferred — would use pgx here.
	return &NoopStore{}, nil
}

// NoopStore is a no-op implementation of Store for when no database is configured.
type NoopStore struct{}

func (n *NoopStore) SaveReview(ctx context.Context, review *schema.ReviewOutput) error {
	return nil
}

func (n *NoopStore) GetReview(ctx context.Context, repo, commit string) (*schema.ReviewOutput, error) {
	return nil, nil
}

func (n *NoopStore) ListReviews(ctx context.Context, repo string, limit int) ([]*schema.ReviewOutput, error) {
	return nil, nil
}
