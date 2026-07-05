// Package publish uploads a review.json to the ATC's agent reviews API,
// keyed by the build that produced it.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Options configures a call to Publish.
//
// RetryDelay is the base delay between retry attempts. A zero value means
// "use the default" (2s), which is appropriate for production use. Pass a
// negative value (e.g. -1) to disable the delay entirely, which is useful
// in tests that want to exercise the retry path without waiting.
type Options struct {
	ATCURL     string
	BuildID    string
	Token      string
	ReviewPath string
	HTTPClient *http.Client
	RetryDelay time.Duration
}

const maxAttempts = 3

func Publish(ctx context.Context, opts Options) error {
	if opts.ATCURL == "" {
		return fmt.Errorf("ATC_EXTERNAL_URL is not set")
	}
	if opts.BuildID == "" {
		return fmt.Errorf("BUILD_ID is not set")
	}
	if opts.Token == "" {
		return fmt.Errorf("AGENT_REVIEW_PUBLISH_TOKEN is not set")
	}

	buildID, err := strconv.Atoi(opts.BuildID)
	if err != nil {
		return fmt.Errorf("invalid BUILD_ID %q: %w", opts.BuildID, err)
	}

	review, err := os.ReadFile(opts.ReviewPath)
	if err != nil {
		return fmt.Errorf("reading review: %w", err)
	}
	if !json.Valid(review) {
		return fmt.Errorf("review file %s is not valid JSON", opts.ReviewPath)
	}

	body, err := json.Marshal(map[string]any{
		"build_id": buildID,
		"review":   json.RawMessage(review),
	})
	if err != nil {
		return fmt.Errorf("encoding submission: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	delay := opts.RetryDelay
	if delay == 0 {
		delay = 2 * time.Second
	}
	if delay < 0 {
		delay = 0
	}

	url := strings.TrimSuffix(opts.ATCURL, "/") + "/api/v1/agent/reviews"

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+opts.Token)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("publish failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return lastErr
			}
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay * time.Duration(attempt)):
			}
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}
