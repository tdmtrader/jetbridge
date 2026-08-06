package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type rotatingToken struct{ calls int }

func (source *rotatingToken) Token(context.Context) (string, error) {
	source.calls++
	return fmt.Sprintf("token-%d", source.calls), nil
}

func TestObserveNormalizesSubmittedReviewsAndAcknowledgesOneBatch(t *testing.T) {
	t.Parallel()
	var serverURL string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("accept = %q", got)
		}
		if got := request.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("version = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("agent = %q", got)
		}
		if got := request.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer token-") {
			t.Errorf("authorization = %q", got)
		}
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			if request.URL.Query().Get("page") == "2" {
				writeFixture(t, response, "reviews_page_2.json")
			} else {
				response.Header().Set("Link", "<"+serverURL+"/repos/acme/widget/pulls/42/reviews?page=2&per_page=100>; rel=\"next\"")
				writeFixture(t, response, "reviews_page_1.json")
			}
		case "/repos/acme/widget/pulls/42/comments":
			writeFixture(t, response, "review_comments_page_1.json")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	tokens := &rotatingToken{}
	observer, err := NewObserver(server.URL, tokens, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}
	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != contracts.PullRequestActive || first.Mergeability != contracts.PullRequestMergeable {
		t.Fatalf("state = %s/%s", first.State, first.Mergeability)
	}
	if first.SourceSHA != sha('a') || first.TargetSHA != sha('b') || first.SourceRef != "refs/heads/feature/widget" || first.TargetRef != "refs/heads/main" {
		t.Fatalf("heads = %#v", first)
	}
	if len(first.ReviewBatches) != 1 || first.ReviewBatches[0].ReviewID != "10" || first.ReviewBatches[0].CommitSHA != sha('a') {
		t.Fatalf("batches = %#v", first.ReviewBatches)
	}
	if got := first.ReviewBatches[0].ThreadIDs; len(got) != 1 || got[0] != "thread-100" {
		t.Fatalf("reply authority = %#v", got)
	}
	if len(first.Threads) != 2 || first.Threads[0].ID != "review-10-body" || first.Threads[1].ID != "thread-100" {
		t.Fatalf("threads = %#v", first.Threads)
	}
	if first.Threads[0].Anchor != nil || first.Threads[0].Comments[0].Body != "overall context" {
		t.Fatalf("overall thread = %#v", first.Threads[0])
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("observation invalid: %v", err)
	}
	replay, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Cursor != first.Cursor || replay.ReviewBatches[0].ID != first.ReviewBatches[0].ID {
		t.Fatalf("replay = %#v", replay)
	}
	second, err := observer.Observe(context.Background(), locator, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ReviewBatches) != 1 || second.ReviewBatches[0].ReviewID != "12" {
		t.Fatalf("second batches = %#v", second.ReviewBatches)
	}
	if requests < 12 || tokens.calls != requests {
		t.Fatalf("requests/tokens = %d/%d", requests, tokens.calls)
	}
}

func TestObserveTerminalAndMergeabilityMappings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, fixture string
		wantState     contracts.PullRequestState
		wantMerge     contracts.PullRequestMergeability
	}{
		{"merged", "pull_request_merged.json", contracts.PullRequestCompleted, contracts.PullRequestMergeable},
		{"closed", "pull_request_closed.json", contracts.PullRequestAbandoned, contracts.PullRequestUnknown},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var serverURL string
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				writeFixtureAt(t, response, test.fixture, serverURL)
			}))
			serverURL = server.URL
			defer server.Close()
			observer, err := NewObserver(server.URL, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			got, err := observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.wantState || got.Mergeability != test.wantMerge || got.Cursor == "" {
				t.Fatalf("got = %#v", got)
			}
		})
	}
}

func TestObserveRejectsUnsafeProviderResponsesAndCursor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		body      string
		cursor    pullrequest.Cursor
		wantRetry bool
	}{
		{"malformed cursor", 200, nil, `{}`, "bad", false},
		{"malformed JSON", 200, nil, `{`, "", false},
		{"ordinary forbidden", 403, nil, `forbidden`, "", false},
		{"rate limited forbidden", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1780000000"}, `slow`, "", true},
		{"rate limited", 429, map[string]string{"Retry-After": "60"}, `slow`, "", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				for key, value := range test.headers {
					response.Header().Set(key, value)
				}
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			observer, err := NewObserver(server.URL, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, test.cursor)
			if err == nil {
				t.Fatal("expected error")
			}
			_, retry := err.(*pullrequest.RateLimitError)
			if retry != test.wantRetry {
				t.Fatalf("retry = %t, error = %v", retry, err)
			}
		})
	}
}

func TestObserveRejectsURLMismatchRedirectAndRedactsToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(response, request, "/secret", http.StatusFound)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"number":42,"html_url":"https://evil.example/pull/42","state":"open","merged":false,"mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"` + sha('a') + `","repo":{"full_name":"acme/widget"}},"base":{"ref":"main","sha":"` + sha('b') + `"}}`))
	}))
	defer server.Close()
	observer, err := NewObserver(server.URL, tokenFunc(func(context.Context) (string, error) { return "top-secret", nil }), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestObserveRejectsUnsafePaginationAndOversizedResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		reviews string
		body    string
	}{
		{"cross origin next", `<https://example.invalid/reviews?page=2>; rel="next"`, ""},
		{"oversized", "", strings.Repeat("x", maxBodyBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var serverURL string
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				requests++
				switch request.URL.Path {
				case "/repos/acme/widget/pulls/42":
					writeFixtureAt(t, response, "pull_request_active.json", serverURL)
				case "/repos/acme/widget/pulls/42/reviews":
					if test.body != "" {
						_, _ = response.Write([]byte(test.body))
						return
					}
					response.Header().Set("Link", test.reviews)
					_, _ = response.Write([]byte("[]"))
				case "/repos/acme/widget/pulls/42/comments":
					_, _ = response.Write([]byte("[]"))
				}
			}))
			serverURL = server.URL
			defer server.Close()
			observer, err := NewObserver(server.URL, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
			if err == nil {
				t.Fatal("expected unsafe provider response to fail")
			}
			if test.reviews != "" && requests != 2 {
				t.Fatalf("unsafe pagination made %d requests, want 2", requests)
			}
		})
	}
}

func TestNormalizeMergeabilityIsConservative(t *testing.T) {
	t.Parallel()
	truth := true
	lie := false
	for _, test := range []struct {
		mergeable *bool
		state     string
		want      contracts.PullRequestMergeability
	}{
		{nil, "clean", contracts.PullRequestUnknown}, {&lie, "dirty", contracts.PullRequestConflicted}, {&lie, "clean", contracts.PullRequestUnknown},
		{&truth, "clean", contracts.PullRequestMergeable}, {&truth, "behind", contracts.PullRequestMergeable}, {&truth, "has_hooks", contracts.PullRequestMergeable},
		{&truth, "blocked", contracts.PullRequestPolicyBlocked}, {&truth, "draft", contracts.PullRequestPolicyBlocked}, {&truth, "unstable", contracts.PullRequestPolicyBlocked}, {&truth, "unexpected", contracts.PullRequestUnknown},
	} {
		if got := normalizeMergeability(pull{Mergeable: test.mergeable, MergeableState: test.state}, contracts.PullRequestActive); got != test.want {
			t.Fatalf("%q = %q, want %q", test.state, got, test.want)
		}
	}
}

func TestNormalizeReviewSortsEmittedThreadIDsLexically(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, time.July, 29, 10, 0, 0, 0, time.UTC)
	value := review{ID: 7, User: ghUser{ID: 9}, CommitID: sha('a'), SubmittedAt: &when}
	batch, threads, err := normalizeReview(value, []reviewComment{
		{ID: 2, ReviewID: int64Pointer(7), User: ghUser{ID: 9}, Body: "two", CommitID: sha('a')},
		{ID: 10, ReviewID: int64Pointer(7), User: ghUser{ID: 9}, Body: "ten", CommitID: sha('a')},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{threads[0].ID, threads[1].ID}; strings.Join(got, ",") != "thread-10,thread-2" {
		t.Fatalf("thread IDs = %v", got)
	}
	if got := batch.ThreadIDs; strings.Join(got, ",") != "thread-10,thread-2" {
		t.Fatalf("authority IDs = %v", got)
	}
	observation := pullrequest.Observation{Locator: pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, Cursor: "opaque", URL: "https://github.com/acme/widget/pull/42", State: contracts.PullRequestActive, Mergeability: contracts.PullRequestMergeable, SourceRef: "refs/heads/feature", SourceSHA: sha('a'), TargetRef: "refs/heads/main", TargetSHA: sha('b'), Iteration: "42", ReviewBatches: []pullrequest.ReviewBatch{batch}, Threads: threads}
	if err := observation.Validate(); err != nil {
		t.Fatalf("normalized observation is invalid: %v", err)
	}
}

func TestNormalizeStateRejectsContradictoryOpenMergedPullRequest(t *testing.T) {
	t.Parallel()
	if _, err := normalizeState(pull{State: "open", Merged: true}); err == nil {
		t.Fatal("open merged pull request was accepted")
	}
}

func TestNormalizeBranchRefAndRejectsUnsafeNames(t *testing.T) {
	t.Parallel()
	if got, err := normalizeBranchRef("feature/widget"); err != nil || got != "refs/heads/feature/widget" {
		t.Fatalf("short branch = %q, %v", got, err)
	}
	if got, err := normalizeBranchRef("refs/heads/release"); err != nil || got != "refs/heads/refs/heads/release" {
		t.Fatalf("prefix-looking branch = %q, %v", got, err)
	}
	if got, err := normalizeBranchRef("refs/tags/v1"); err != nil || got != "refs/heads/refs/tags/v1" {
		t.Fatalf("other prefix-looking branch = %q, %v", got, err)
	}
	for _, raw := range []string{"", "feature..bad", "feature lock", "feature~old", "feature/", "/feature"} {
		if _, err := normalizeBranchRef(raw); err == nil {
			t.Fatalf("unsafe branch %q was accepted", raw)
		}
	}
}

func TestNewObserverBoundsHTTPTimeout(t *testing.T) {
	t.Parallel()
	defaultObserver, err := NewObserver("https://api.github.com", &rotatingToken{}, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if defaultObserver.client.Timeout != defaultHTTPTimeout {
		t.Fatalf("default timeout = %s", defaultObserver.client.Timeout)
	}
	short := 3 * time.Second
	shortObserver, err := NewObserver("https://api.github.com", &rotatingToken{}, &http.Client{Timeout: short})
	if err != nil {
		t.Fatal(err)
	}
	if shortObserver.client.Timeout != short {
		t.Fatalf("short timeout = %s", shortObserver.client.Timeout)
	}
}

func TestNextURLRejectsWrongEndpointAndNonCanonicalQuery(t *testing.T) {
	t.Parallel()
	observer, err := NewObserver("https://api.github.com", &rotatingToken{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`<https://api.github.com/repos/acme/widget/pulls/42/comments?page=2&per_page=100>; rel="next"`,
		`<https://api.github.com/repos/acme/widget/pulls/42/reviews?page=2&per_page=100&sort=created>; rel="next"`,
		`<https://user@api.github.com/repos/acme/widget/pulls/42/reviews?page=2&per_page=100>; rel="next"`,
		`<https://api.github.com/repos/acme/widget/pulls/42/reviews?page=02&per_page=100>; rel="next"`,
	} {
		if _, err := observer.nextURL(raw, "/repos/acme/widget/pulls/42/reviews"); err == nil {
			t.Fatalf("unsafe link accepted: %s", raw)
		}
	}
}

func TestObserveClassifiesOnlyProvenRateLimits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status  int
		headers map[string]string
		want    bool
	}{
		{http.StatusTooManyRequests, nil, false}, {http.StatusTooManyRequests, map[string]string{"Retry-After": "17"}, true},
		{http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1780000000"}, true},
	} {
		t.Run(fmt.Sprintf("%d-%t", test.status, test.want), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				for key, value := range test.headers {
					response.Header().Set(key, value)
				}
				response.WriteHeader(test.status)
			}))
			defer server.Close()
			observer, err := NewObserver(server.URL, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
			limited, got := err.(*pullrequest.RateLimitError)
			if got != test.want {
				t.Fatalf("rate limit = %t, err = %v", got, err)
			}
			if got && test.headers["Retry-After"] == "17" && limited.RetryAfter != 17*time.Second {
				t.Fatalf("retry after = %s", limited.RetryAfter)
			}
			if got && test.headers["X-RateLimit-Reset"] != "" && limited.ResetAt.IsZero() {
				t.Fatal("rate-limit reset was not parsed")
			}
		})
	}
}

func TestObserveRefusesRedirectWithoutCredentialDisclosure(t *testing.T) {
	t.Parallel()
	secondRequests := 0
	secondAuthorization := ""
	second := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secondRequests++
		secondAuthorization = request.Header.Get("Authorization")
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, second.URL+request.URL.Path, http.StatusFound)
	}))
	defer first.Close()
	observer, err := NewObserver(first.URL, tokenFunc(func(context.Context) (string, error) { return "redirect-secret", nil }), first.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
	if err == nil {
		t.Fatal("redirect was accepted")
	}
	if secondRequests != 0 || secondAuthorization != "" {
		t.Fatalf("redirect target received %d requests with %q", secondRequests, secondAuthorization)
	}
}

func TestObserveRedactsTokenFromTransportFailure(t *testing.T) {
	t.Parallel()
	secret := "transport-secret"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("network failed with %s", secret) })}
	observer, err := NewObserver("https://api.github.com", tokenFunc(func(context.Context) (string, error) { return secret, nil }), client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Observe(context.Background(), pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, "")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked token: %v", err)
	}
}

func int64Pointer(value int64) *int64 { return &value }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type tokenFunc func(context.Context) (string, error)

func (function tokenFunc) Token(context context.Context) (string, error) { return function(context) }

func writeFixture(t *testing.T, response http.ResponseWriter, name string) {
	t.Helper()
	contents, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(contents)
}

func writeFixtureAt(t *testing.T, response http.ResponseWriter, name, host string) {
	t.Helper()
	contents, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.ReplaceAll(string(contents), "https://github.com", host))
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(contents)
}

func sha(character rune) string { return strings.Repeat(string(character), 40) }

// GitHub answers If-None-Match with a 304 it does not bill against the rate
// limit. At a 5m poll each watched PR spends ~864 requests/day against a
// 5,000/hr ceiling, so unchanged reads should cost nothing.
func TestObserveReusesCachedBodiesOnNotModified(t *testing.T) {
	t.Parallel()
	var serverURL string
	conditional := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") == `"etag-1"` {
			conditional++
			response.WriteHeader(http.StatusNotModified)
			return
		}
		response.Header().Set("ETag", `"etag-1"`)
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			writeFixture(t, response, "reviews_page_1.json")
		case "/repos/acme/widget/pulls/42/comments":
			writeFixture(t, response, "review_comments_page_1.json")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	observer, err := NewObserver(server.URL, tokenFunc(func(context.Context) (string, error) {
		return "token-1", nil
	}), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}

	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatalf("second Observe = %v, want the cached bodies replayed", err)
	}
	if conditional == 0 {
		t.Fatal("no conditional request was issued")
	}
	if first.SourceSHA != second.SourceSHA || len(first.Threads) != len(second.Threads) {
		t.Fatalf("cached observation diverged: %+v vs %+v", first, second)
	}
}

// Reproduces the shape captured from real GitHub: a human's root comment sits
// in review A, and the platform's reply is filed by GitHub under a NEW review B
// while in_reply_to_id still names the root in A. Advancing the cursor past
// review A used to fail permanently with "github review reply has no root",
// because the reply-chain index was built from one review's comments only.
// Every other fixture in testdata/ puts all comments under one review id, which
// is why the suite never caught this.
func TestObserveResolvesARepliesAcrossReviewBoundaries(t *testing.T) {
	t.Parallel()
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			writeFixtureAt(t, response, "pull_request_active.json", serverURL)
		case "/repos/acme/widget/pulls/42/reviews":
			writeFixture(t, response, "reviews_cross_review.json")
		case "/repos/acme/widget/pulls/42/comments":
			writeFixture(t, response, "review_comments_cross_review.json")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	observer, err := NewObserver(server.URL, tokenFunc(func(context.Context) (string, error) {
		return "token-1", nil
	}), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}

	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatalf("first Observe = %v, want the human's review", err)
	}
	// Advancing past review A lands on review B, which holds only the reply.
	second, err := observer.Observe(context.Background(), locator, first.Cursor)
	if err != nil {
		t.Fatalf("Observe advancing to the cross-review reply = %v, want success", err)
	}
	if len(second.Threads) != 1 {
		t.Fatalf("threads = %d, want the reply resolved into its root's thread", len(second.Threads))
	}
	if second.Threads[0].ID != "thread-3725191076" {
		t.Fatalf("thread id = %q, want it rooted at the human's comment", second.Threads[0].ID)
	}
	if len(second.Threads[0].Comments) != 2 {
		t.Fatalf("thread comments = %d, want the root and the reply together", len(second.Threads[0].Comments))
	}
}
