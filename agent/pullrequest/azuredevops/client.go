package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/pullrequest"
)

const (
	apiVersion         = "7.1"
	userAgent          = "concourse-agent-azuredevops-observer/1.0"
	maxBodyBytes       = 1 << 20
	maxPages           = 8
	maxContinuation    = 1024
	maxRetrySeconds    = int64((1<<63 - 1) / time.Second)
	defaultHTTPTimeout = 30 * time.Second
)

// Observer translates the Azure DevOps Git REST 7.1 read surface into the
// provider-neutral pull-request observation contract. Day 1 intentionally
// uses OAuth-style Bearer credentials; PAT Basic authentication is not
// inferred from token contents.
type Observer struct {
	organizationURL *url.URL
	project         string
	repositoryID    string
	token           pullrequest.TokenSource
	client          *http.Client
}

func NewObserver(organizationURL, projectName, repositoryID string, token pullrequest.TokenSource, client *http.Client) (*Observer, error) {
	if token == nil {
		return nil, fmt.Errorf("azure devops bearer token source is required")
	}
	parsed, err := url.Parse(organizationURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, fmt.Errorf("azure devops organization URL must be an absolute http or https URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && (path.Clean(parsed.Path) != parsed.Path || strings.Contains(parsed.Path, "//")) {
		return nil, fmt.Errorf("azure devops organization URL path must be canonical")
	}
	if err := validateSegment("project", projectName); err != nil {
		return nil, err
	}
	if err := validateSegment("repository id", repositoryID); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if cloned.Timeout == 0 || cloned.Timeout > defaultHTTPTimeout {
		cloned.Timeout = defaultHTTPTimeout
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Observer{
		organizationURL: parsed,
		project:         projectName,
		repositoryID:    repositoryID,
		token:           token,
		client:          &cloned,
	}, nil
}

func validateSegment(label, value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || len(value) > 256 || value == "." || value == ".." || strings.ContainsAny(value, "/\\?#\x00") {
		return fmt.Errorf("azure devops %s must be a canonical path segment", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("azure devops %s must be a canonical path segment", label)
		}
	}
	return nil
}

func (observer *Observer) endpoint(pullRequestID, suffix, continuation string) *url.URL {
	target := *observer.organizationURL
	target.Path = observer.organizationURL.Path +
		"/" + observer.project +
		"/_apis/git/repositories/" + observer.repositoryID +
		"/pullRequests/" + pullRequestID + suffix
	target.RawPath = ""
	query := url.Values{"api-version": []string{apiVersion}}
	if continuation != "" {
		query.Set("continuationToken", continuation)
	}
	target.RawQuery = query.Encode()
	return &target
}

func (observer *Observer) webURL(pullRequestID string) string {
	target := *observer.organizationURL
	target.Path = observer.organizationURL.Path +
		"/" + observer.project +
		"/_git/" + observer.repositoryID +
		"/pullrequest/" + pullRequestID
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	target.User = nil
	return target.String()
}

func (observer *Observer) getPage(ctx context.Context, target *url.URL, destination any) (string, error) {
	if err := observer.validateRequestURL(target); err != nil {
		return "", err
	}
	credential, err := observer.token.Token(ctx)
	if err != nil || strings.TrimSpace(credential) == "" {
		return "", fmt.Errorf("azure devops bearer credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("azure devops request construction failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("User-Agent", userAgent)
	response, err := observer.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("azure devops request failed: %s", redact(err.Error(), credential))
	}
	defer response.Body.Close()
	if response.Request == nil || observer.validateRequestURL(response.Request.URL) != nil {
		return "", fmt.Errorf("azure devops response came from outside the configured organization")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode > 299 {
		return "", azureStatusError(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("azure devops response read failed")
	}
	if len(raw) > maxBodyBytes {
		return "", fmt.Errorf("azure devops response exceeds %d bytes", maxBodyBytes)
	}
	if err := decodeJSON(raw, destination); err != nil {
		return "", fmt.Errorf("azure devops response is invalid JSON")
	}
	continuation, err := continuationToken(response.Header)
	if err != nil {
		return "", err
	}
	return continuation, nil
}

func (observer *Observer) validateRequestURL(candidate *url.URL) error {
	if candidate == nil || candidate.User != nil || candidate.ForceQuery || candidate.Fragment != "" || candidate.Scheme != observer.organizationURL.Scheme || !strings.EqualFold(candidate.Host, observer.organizationURL.Host) {
		return fmt.Errorf("azure devops request URL is outside the configured organization")
	}
	prefix := observer.organizationURL.Path + "/" + observer.project + "/_apis/git/repositories/" + observer.repositoryID + "/pullRequests/"
	if !strings.HasPrefix(candidate.Path, prefix) {
		return fmt.Errorf("azure devops request URL is outside the configured API path")
	}
	query := candidate.Query()
	if values := query["api-version"]; len(values) != 1 || values[0] != apiVersion {
		return fmt.Errorf("azure devops request URL does not pin REST 7.1")
	}
	if len(query) > 2 {
		return fmt.Errorf("azure devops request URL has unexpected query parameters")
	}
	for key, values := range query {
		switch key {
		case "api-version":
		case "continuationToken":
			if len(values) != 1 || validateContinuation(values[0]) != nil {
				return fmt.Errorf("azure devops continuation token is invalid")
			}
		default:
			return fmt.Errorf("azure devops request URL has unexpected query parameters")
		}
	}
	if candidate.RawQuery != canonicalQuery(query) {
		return fmt.Errorf("azure devops request URL query is not canonical")
	}
	return nil
}

func canonicalQuery(query url.Values) string {
	canonical := url.Values{"api-version": []string{apiVersion}}
	if continuation := query.Get("continuationToken"); continuation != "" {
		canonical.Set("continuationToken", continuation)
	}
	return canonical.Encode()
}

func continuationToken(header http.Header) (string, error) {
	values := header.Values("x-ms-continuationtoken")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || validateContinuation(values[0]) != nil {
		return "", fmt.Errorf("azure devops continuation token is invalid")
	}
	return values[0], nil
}

func validateContinuation(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || len(value) > maxContinuation {
		return fmt.Errorf("invalid continuation token")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("invalid continuation token")
		}
	}
	return nil
}

func azureStatusError(response *http.Response) error {
	if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable) && validRetryAfter(response.Header.Get("Retry-After")) {
		return &pullrequest.RateLimitError{RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	return fmt.Errorf("azure devops request failed with status %d", response.StatusCode)
}

func validRetryAfter(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return seconds >= 0 && seconds <= maxRetrySeconds
	}
	instant, err := http.ParseTime(raw)
	return err == nil && instant.After(time.Now())
}

func retryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 && seconds <= maxRetrySeconds {
		return time.Duration(seconds) * time.Second
	}
	if instant, err := http.ParseTime(raw); err == nil && instant.After(time.Now()) {
		return time.Until(instant)
	}
	return 0
}

func redact(value, credential string) string {
	if credential == "" {
		return value
	}
	value = strings.ReplaceAll(value, "Bearer "+credential, "Bearer [redacted]")
	return strings.ReplaceAll(value, credential, "[redacted]")
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
