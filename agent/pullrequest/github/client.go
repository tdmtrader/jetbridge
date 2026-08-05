package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
)

const (
	userAgent          = "concourse-agent-pullrequest-observer/1.0"
	maxBodyBytes       = 1 << 20
	maxPages           = 8
	defaultHTTPTimeout = 30 * time.Second

	// maxCachedResponses bounds the conditional-request cache. Observing one
	// pull request touches a handful of URLs; the bound stops a deeply
	// paginated endpoint from growing the map without limit.
	maxCachedResponses = 64
)

// Observer translates only GitHub's read API into the provider-neutral
// observation contract. It owns no scheduler or mutation authority.
type Observer struct {
	baseURL *url.URL
	token   pullrequest.TokenSource
	client  *http.Client

	cacheMu sync.RWMutex
	cache   map[string]cachedResponse
}

// cachedResponse retains one validated GitHub read. Link is stored alongside
// the body so a 304 can replay pagination as faithfully as a 200.
type cachedResponse struct {
	etag string
	body []byte
	link string
}

func (observer *Observer) cachedFor(key string) (cachedResponse, bool) {
	observer.cacheMu.RLock()
	defer observer.cacheMu.RUnlock()
	entry, found := observer.cache[key]
	return entry, found
}

// storeCached retains an immutable copy. Bodies are already bounded by
// maxBodyBytes before this is reached; maxCachedResponses bounds the map so a
// deeply paginated endpoint cannot grow it without limit.
func (observer *Observer) storeCached(key, etag string, body []byte, link string) {
	if etag == "" {
		return
	}
	observer.cacheMu.Lock()
	defer observer.cacheMu.Unlock()
	if observer.cache == nil {
		observer.cache = make(map[string]cachedResponse, maxCachedResponses)
	}
	if len(observer.cache) >= maxCachedResponses {
		for existing := range observer.cache {
			delete(observer.cache, existing)
			break
		}
	}
	observer.cache[key] = cachedResponse{etag: etag, body: append([]byte(nil), body...), link: link}
}

func NewObserver(baseURL string, token pullrequest.TokenSource, client *http.Client) (*Observer, error) {
	if token == nil {
		return nil, fmt.Errorf("github token source is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fmt.Errorf("github base URL must be an absolute http or https URL without query or fragment")
	}
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cloned.Timeout == 0 || cloned.Timeout > defaultHTTPTimeout {
		cloned.Timeout = defaultHTTPTimeout
	}
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
	cacheKey := target.String()
	if entry, found := observer.cachedFor(cacheKey); found {
		request.Header.Set("If-None-Match", entry.etag)
	}
	response, err := observer.client.Do(request)
	if err != nil {
		return fmt.Errorf("github request failed: %s", redact(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		entry, found := observer.cachedFor(cacheKey)
		if !found {
			return fmt.Errorf("github returned 304 without a cached body")
		}
		if err := decodeJSON(entry.body, destination); err != nil {
			return fmt.Errorf("github response is invalid JSON")
		}
		return nil
	}
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
	observer.storeCached(cacheKey, response.Header.Get("ETag"), raw, "")
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
	cacheKey := target.String()
	if entry, found := observer.cachedFor(cacheKey); found {
		request.Header.Set("If-None-Match", entry.etag)
	}
	response, err := observer.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %s", redact(err.Error(), token))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		entry, found := observer.cachedFor(cacheKey)
		if !found {
			return nil, fmt.Errorf("github returned 304 without a cached body")
		}
		if err := decodeJSON(entry.body, destination); err != nil {
			return nil, fmt.Errorf("github response is invalid JSON")
		}
		return observer.nextURL(entry.link, target.Path)
	}
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
	link := response.Header.Get("Link")
	next, err := observer.nextURL(link, target.Path)
	if err != nil {
		return nil, err
	}
	observer.storeCached(cacheKey, response.Header.Get("ETag"), raw, link)
	return next, nil
}

func (observer *Observer) nextURL(link, endpointPath string) (*url.URL, error) {
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
		if err := observer.validatePageURL(parsed, endpointPath); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	return nil, nil
}

func (observer *Observer) validateURL(candidate *url.URL) error {
	if candidate == nil || candidate.User != nil || candidate.ForceQuery || candidate.Fragment != "" || candidate.Scheme != observer.baseURL.Scheme || !strings.EqualFold(candidate.Host, observer.baseURL.Host) || !strings.HasPrefix(candidate.Path, observer.baseURL.Path+"/") && candidate.Path != observer.baseURL.Path {
		return fmt.Errorf("github request URL is outside the configured API origin")
	}
	return nil
}

func (observer *Observer) validatePageURL(candidate *url.URL, endpointPath string) error {
	if err := observer.validateURL(candidate); err != nil {
		return err
	}
	if candidate.Path != endpointPath {
		return fmt.Errorf("github pagination link changes endpoint")
	}
	query := candidate.Query()
	if len(query) != 2 || len(query["page"]) != 1 || len(query["per_page"]) != 1 || query.Get("per_page") != "100" {
		return fmt.Errorf("github pagination link query is invalid")
	}
	page, err := strconv.ParseInt(query.Get("page"), 10, 64)
	if err != nil || page <= 0 || strconv.FormatInt(page, 10) != query.Get("page") {
		return fmt.Errorf("github pagination link page is invalid")
	}
	canonical := url.Values{"page": []string{query.Get("page")}, "per_page": []string{"100"}}.Encode()
	if candidate.RawQuery != canonical {
		return fmt.Errorf("github pagination link query is not canonical")
	}
	return nil
}

func githubStatusError(response *http.Response, _ string) error {
	if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden) && githubRateLimited(response.Header) {
		return &pullrequest.RateLimitError{RetryAfter: retryAfter(response.Header), ResetAt: rateLimitReset(response.Header)}
	}
	return fmt.Errorf("github request failed with status %d", response.StatusCode)
}

func githubRateLimited(header http.Header) bool {
	return validRetryAfter(header.Get("Retry-After")) || header.Get("X-RateLimit-Remaining") == "0" && !rateLimitReset(header).IsZero()
}

func validRetryAfter(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return seconds >= 0
	}
	_, err := http.ParseTime(raw)
	return err == nil
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
