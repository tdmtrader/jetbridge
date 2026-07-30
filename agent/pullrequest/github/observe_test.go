package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	if first.SourceSHA != sha('a') || first.TargetSHA != sha('b') || first.SourceRef != "feature/widget" || first.TargetRef != "main" {
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
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
