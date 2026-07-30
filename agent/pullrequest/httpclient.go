package pullrequest

import (
	"context"
	"fmt"
	"time"
)

// TokenSource obtains a short-lived provider credential. Observers call it for
// every request so a rotated credential is never retained in an adapter.
type TokenSource interface {
	Token(context.Context) (string, error)
}

// RateLimitError lets the caller retry at a provider-directed time. It never
// contains provider response text or credentials.
type RateLimitError struct {
	RetryAfter time.Duration
	ResetAt    time.Time
}

func (err *RateLimitError) Error() string {
	if err == nil {
		return "provider rate limited"
	}
	if err.RetryAfter > 0 {
		return fmt.Sprintf("provider rate limited; retry after %s", err.RetryAfter)
	}
	if !err.ResetAt.IsZero() {
		return fmt.Sprintf("provider rate limited; reset at %s", err.ResetAt.UTC().Format(time.RFC3339))
	}
	return "provider rate limited"
}
