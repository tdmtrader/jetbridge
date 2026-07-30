package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
)

const (
	userAgent    = "concourse-agent-pullrequest-observer/1.0"
	maxBodyBytes = 1 << 20
	maxPages     = 8
)

// Observer translates only GitHub's read API into the provider-neutral
// observation contract. It owns no scheduler or mutation authority.
type Observer struct {
	baseURL *url.URL
	token   pullrequest.TokenSource
	client  *http.Client
}

func NewObserver(baseURL string, token pullrequest.TokenSource, client *http.Client) (*Observer, error) {
	if token == nil {
		return nil, fmt.Errorf("github token source is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("github base URL must be an absolute http or https URL without query or fragment")
	}
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Observer{baseURL: parsed, token: token, client: &cloned}, nil
}

func (observer *Observer) endpoint(path string) *url.URL {
	copy := *observer.baseURL
	copy.Path = observer.baseURL.Path + path
	copy.RawPath = ""
	return &copy
}

func (observer *Observer) get(ctx context.Context, target *url.URL, destination any) error {
	if err := observer.validateURL(target); err != nil {
		return err
	}
	token, err := observer.token.Token(ctx)
	if err != nil {
		return fmt.Errorf("github credential unavailable")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("github credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("github request construction failed")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)
	response, err := observer.client.Do(request)
	if err != nil {
		return fmt.Errorf("github request failed: %s", redact(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return githubStatusError(response, token)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("github response read failed")
	}
	if len(raw) > maxBodyBytes {
		return fmt.Errorf("github response exceeds %d bytes", maxBodyBytes)
	}
	if err := decodeJSON(raw, destination); err != nil {
		return fmt.Errorf("github response is invalid JSON")
	}
	return nil
}

func (observer *Observer) getPage(ctx context.Context, target *url.URL, destination any) (*url.URL, error) {
	if err := observer.validateURL(target); err != nil {
		return nil, err
	}
	token, err := observer.token.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("github credential unavailable")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("github credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("github request construction failed")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)
	response, err := observer.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %s", redact(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, githubStatusError(response, token)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("github response read failed")
	}
	if len(raw) > maxBodyBytes {
		return nil, fmt.Errorf("github response exceeds %d bytes", maxBodyBytes)
	}
	if err := decodeJSON(raw, destination); err != nil {
		return nil, fmt.Errorf("github response is invalid JSON")
	}
	next, err := observer.nextURL(response.Header.Get("Link"))
	if err != nil {
		return nil, err
	}
	return next, nil
}

func (observer *Observer) nextURL(link string) (*url.URL, error) {
	if link == "" {
		return nil, nil
	}
	for _, component := range strings.Split(link, ",") {
		if !strings.Contains(component, `rel="next"`) {
			continue
		}
		start, end := strings.IndexByte(component, '<'), strings.IndexByte(component, '>')
		if start < 0 || end <= start+1 {
			return nil, fmt.Errorf("github pagination link is malformed")
		}
		parsed, err := url.Parse(strings.TrimSpace(component[start+1 : end]))
		if err != nil {
			return nil, fmt.Errorf("github pagination link is malformed")
		}
		if err := observer.validateURL(parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	return nil, nil
}

func (observer *Observer) validateURL(candidate *url.URL) error {
	if candidate == nil || candidate.Scheme != observer.baseURL.Scheme || !strings.EqualFold(candidate.Host, observer.baseURL.Host) || !strings.HasPrefix(candidate.Path, observer.baseURL.Path+"/") && candidate.Path != observer.baseURL.Path {
		return fmt.Errorf("github request URL is outside the configured API origin")
	}
	return nil
}

func githubStatusError(response *http.Response, _ string) error {
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden && githubRateLimited(response.Header) {
		return &pullrequest.RateLimitError{RetryAfter: retryAfter(response.Header), ResetAt: rateLimitReset(response.Header)}
	}
	return fmt.Errorf("github request failed with status %d", response.StatusCode)
}

func githubRateLimited(header http.Header) bool {
	return header.Get("Retry-After") != "" || header.Get("X-RateLimit-Remaining") == "0" && rateLimitReset(header).After(time.Unix(0, 0))
}

func retryAfter(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if instant, err := http.ParseTime(raw); err == nil && instant.After(time.Now()) {
		return time.Until(instant)
	}
	return 0
}

func rateLimitReset(header http.Header) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func redact(value, token string) string {
	value = strings.ReplaceAll(value, token, "[redacted]")
	return strings.ReplaceAll(value, "Bearer "+token, "Bearer [redacted]")
}
