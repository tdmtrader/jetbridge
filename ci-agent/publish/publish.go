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
		return fmt.Errorf("AGENT_REVIEW_PRINCIPAL_TOKEN is not set")
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

// CostsOptions configures PublishCosts.
type CostsOptions struct {
	ATCURL     string
	BuildID    string
	Token      string
	CostsPath  string
	Phase      string // step_name prefix, e.g. "review"
	UserName   string // optional attribution (AGENT_COST_USER)
	HTTPClient *http.Client
}

// stepCostRecord mirrors phaserunner.StepCost (wire-format coupling only;
// ci-agent is a standalone module).
type stepCostRecord struct {
	Step                string  `json:"step"`
	Model               string  `json:"model,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Turns               int     `json:"turns"`
	CostUSD             float64 `json:"cost_usd"`
	DurationMS          int     `json:"duration_ms"`
}

// PublishCosts POSTs each costs.json record to /api/v1/agent/costs
// (source ci_agent). A missing costs.json is a silent skip; any HTTP
// failure returns an error that the CLI downgrades to a warning — cost
// reporting must never fail a build.
func PublishCosts(ctx context.Context, opts CostsOptions) error {
	if opts.ATCURL == "" {
		return fmt.Errorf("ATC_EXTERNAL_URL is not set")
	}
	if opts.Token == "" {
		return fmt.Errorf("AGENT_COST_PRINCIPAL_TOKEN is not set")
	}
	buildID, err := strconv.Atoi(opts.BuildID)
	if err != nil {
		return fmt.Errorf("invalid BUILD_ID %q: %w", opts.BuildID, err)
	}

	data, err := os.ReadFile(opts.CostsPath)
	if os.IsNotExist(err) {
		return nil // phase produced no costs — nothing to do
	}
	if err != nil {
		return fmt.Errorf("reading costs: %w", err)
	}
	var records []stepCostRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parsing %s: %w", opts.CostsPath, err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	url := strings.TrimSuffix(opts.ATCURL, "/") + "/api/v1/agent/costs"

	for _, rec := range records {
		stepName := rec.Step
		if opts.Phase != "" {
			stepName = opts.Phase + "/" + rec.Step
		}
		body, err := json.Marshal(map[string]any{
			"source":                "ci_agent",
			"provider":              "anthropic",
			"build_id":              buildID,
			"step_name":             stepName,
			"user_name":             opts.UserName,
			"model":                 rec.Model,
			"input_tokens":          rec.InputTokens,
			"output_tokens":         rec.OutputTokens,
			"cache_read_tokens":     rec.CacheReadTokens,
			"cache_creation_tokens": rec.CacheCreationTokens,
			"turns":                 rec.Turns,
			"cost_usd":              rec.CostUSD,
		})
		if err != nil {
			return fmt.Errorf("encoding cost record: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+opts.Token)

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("posting cost record: %w", err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("cost publish failed: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		}
	}
	return nil
}
