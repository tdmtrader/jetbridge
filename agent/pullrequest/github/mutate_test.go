package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestMutatorRecoversExactMarkedClosedPullRequestBeforeCreating(t *testing.T) {
	key := mutationOperationKey('1')
	var server *httptest.Server
	var requests int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet {
			http.Error(response, "unexpected mutation request", http.StatusBadRequest)
			return
		}
		pull := map[string]any{
			"number": 42, "html_url": server.URL + "/acme/widget/pull/42",
			"state": "closed", "merged": true,
			"body": "Validated.\n\n<!-- Jetbridge-Operation: create_pr " + key + " -->",
			"head": map[string]any{"ref": "agent/upgrade", "sha": sha('c'), "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "sha": sha('b'), "repo": map[string]any{"full_name": "acme/widget"}},
		}
		switch request.URL.Path {
		case "/repos/acme/widget/pulls":
			query := request.URL.Query()
			if query.Get("state") != "all" || query.Get("head") != "acme:agent/upgrade" ||
				query.Get("base") != "main" || query.Get("per_page") != "100" {
				http.Error(response, "unexpected pull query", http.StatusBadRequest)
				return
			}
			writeJSON(response, []any{pull})
		case "/repos/acme/widget/pulls/42":
			writeJSON(response, pull)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	mutator := newMutationTestMutator(t, server)
	result, err := mutator.FindOrCreatePullRequest(context.Background(), pullrequest.CreateRequest{
		Locator:      pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
		SourceRef:    "refs/heads/agent/upgrade",
		SourceSHA:    sha('c'),
		TargetRef:    "refs/heads/main",
		TargetSHA:    sha('b'),
		Title:        "Upgrade widget",
		Body:         "Validated.",
		OperationKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalID != "42" || result.State != contracts.PullRequestCompleted ||
		result.SourceSHA != sha('c') || result.TargetSHA != sha('b') {
		t.Fatalf("recovered pull request = %+v", result)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want search plus exact-detail recovery reads", requests)
	}
}

func TestMutatorRejectsRecoveredPullRequestWhoseProviderHeadsMoved(t *testing.T) {
	for _, test := range []struct {
		name      string
		sourceSHA string
		targetSHA string
	}{
		{name: "source", sourceSHA: sha('d'), targetSHA: sha('b')},
		{name: "target", sourceSHA: sha('c'), targetSHA: sha('d')},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := mutationOperationKey('6')
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				pull := map[string]any{
					"number": 42, "html_url": server.URL + "/acme/widget/pull/42",
					"state": "open", "merged": false,
					"body": "Validated.\n\n<!-- Jetbridge-Operation: create_pr " + key + " -->",
					"head": map[string]any{"ref": "agent/upgrade", "sha": test.sourceSHA, "repo": map[string]any{"full_name": "acme/widget"}},
					"base": map[string]any{"ref": "main", "sha": test.targetSHA, "repo": map[string]any{"full_name": "acme/widget"}},
				}
				if request.URL.Path == "/repos/acme/widget/pulls" {
					writeJSON(response, []any{pull})
					return
				}
				if request.URL.Path == "/repos/acme/widget/pulls/42" {
					writeJSON(response, pull)
					return
				}
				http.NotFound(response, request)
			}))
			defer server.Close()

			mutator := newMutationTestMutator(t, server)
			_, err := mutator.FindOrCreatePullRequest(context.Background(), pullrequest.CreateRequest{
				Locator:      pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
				SourceRef:    "refs/heads/agent/upgrade",
				SourceSHA:    sha('c'),
				TargetRef:    "refs/heads/main",
				TargetSHA:    sha('b'),
				Title:        "Upgrade widget",
				Body:         "Validated.",
				OperationKey: key,
			})
			if err == nil || !strings.Contains(err.Error(), "exact operation") {
				t.Fatalf("moved-%s recovery error = %v", test.name, err)
			}
		})
	}
}

func TestMutatorFailsClosedForMatchingPullRequestWithMissingMarker(t *testing.T) {
	key := mutationOperationKey('2')
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, []any{map[string]any{
			"number": 42, "html_url": server.URL + "/acme/widget/pull/42",
			"state": "open", "merged": false, "body": "Human-authored body only.",
			"head": map[string]any{"ref": "agent/upgrade", "sha": sha('c'), "repo": map[string]any{"full_name": "acme/widget"}},
			"base": map[string]any{"ref": "main", "sha": sha('b'), "repo": map[string]any{"full_name": "acme/widget"}},
		}})
	}))
	defer server.Close()

	mutator := newMutationTestMutator(t, server)
	_, err := mutator.FindOrCreatePullRequest(context.Background(), pullrequest.CreateRequest{
		Locator:      pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
		SourceRef:    "refs/heads/agent/upgrade",
		SourceSHA:    sha('c'),
		TargetRef:    "refs/heads/main",
		TargetSHA:    sha('b'),
		Title:        "Upgrade widget",
		Body:         "Validated.",
		OperationKey: key,
	})
	if err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("missing-marker error = %v", err)
	}
}

func TestMutatorDoesNotFollowCreationRedirectOrForwardCredential(t *testing.T) {
	key := mutationOperationKey('5')
	var forwardedRequests int
	var forwardedAuthorization string
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		request *http.Request,
	) {
		forwardedRequests++
		forwardedAuthorization = request.Header.Get("Authorization")
	}))
	defer redirectTarget.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method == http.MethodGet {
			writeJSON(response, []any{})
			return
		}
		http.Redirect(response, request, redirectTarget.URL+"/capture", http.StatusFound)
	}))
	defer origin.Close()

	mutator := newMutationTestMutator(t, origin)
	_, err := mutator.FindOrCreatePullRequest(context.Background(), pullrequest.CreateRequest{
		Locator: pullrequest.Locator{
			Provider: pullrequest.ProviderGitHub, Repository: "acme/widget",
		},
		SourceRef: "refs/heads/agent/upgrade", SourceSHA: sha('c'),
		TargetRef: "refs/heads/main", TargetSHA: sha('b'),
		Title: "Upgrade widget", Body: "Validated.", OperationKey: key,
	})
	if err == nil {
		t.Fatal("creation redirect was accepted")
	}
	if forwardedRequests != 0 || forwardedAuthorization != "" {
		t.Fatalf(
			"redirect target received %d requests with authorization %q",
			forwardedRequests, forwardedAuthorization,
		)
	}
}

func TestMutatorRecoversDuplicateExactValidationStatuses(t *testing.T) {
	key := mutationOperationKey('3')
	contextName := "jetbridge/" + key
	var server *httptest.Server
	var posts int
	var pullReads int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/acme/widget/pulls/42":
			pullReads++
			writeJSON(response, mutationPullFixture(server.URL, "open"))
		case "/repos/acme/widget/commits/" + sha('c') + "/statuses":
			status := map[string]any{
				"id": 12, "url": server.URL + "/statuses/12", "state": "success",
				"description": "Jetbridge validation passed",
				"target_url":  "https://ci.example/runs/91",
				"context":     contextName,
			}
			writeJSON(response, []any{status, status})
		default:
			if request.Method == http.MethodPost {
				posts++
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	mutator := newMutationTestMutator(t, server)
	result, err := mutator.PublishValidationStatus(context.Background(), pullrequest.StatusRequest{
		Locator: pullrequest.Locator{
			Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
		},
		SourceSHA: sha('c'), State: "success",
		Description:  "Jetbridge validation passed",
		TargetURL:    "https://ci.example/runs/91",
		OperationKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationKey != key || result.ExternalID != "12" ||
		result.URL != server.URL+"/statuses/12" || posts != 0 || pullReads != 0 {
		t.Fatalf("recovered status = %+v, posts/pull reads = %d/%d", result, posts, pullReads)
	}
}

func TestMutatorRecoversReviewOutputsIndependentlyAndEnforcesThreadAuthority(t *testing.T) {
	key := mutationOperationKey('4')
	summaryMarker := "<!-- Jetbridge-Operation: respond_to_review " + key + " summary -->"
	thread100Marker := "<!-- Jetbridge-Operation: respond_to_review " + key + " thread thread-100 -->"
	thread200Marker := "<!-- Jetbridge-Operation: respond_to_review " + key + " thread thread-200 -->"
	var server *httptest.Server
	var mu sync.Mutex
	posts := make([]string, 0)
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/pulls/42":
			writeJSON(response, mutationPullFixture(server.URL, "open"))
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/issues/42/comments":
			writeJSON(response, []any{map[string]any{
				"id": 51, "html_url": server.URL + "/comments/51",
				"body": "Addressed.\n\n" + summaryMarker,
			}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/pulls/42/comments":
			writeJSON(response, []any{map[string]any{
				"id": 52, "html_url": server.URL + "/comments/52",
				"body":           "Updated first thread.\n\n" + thread100Marker,
				"in_reply_to_id": 100,
			}})
		case request.Method == http.MethodPost && request.URL.Path == "/repos/acme/widget/pulls/42/comments/200/replies":
			var body struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				http.Error(response, "bad body", http.StatusBadRequest)
				return
			}
			if body.Body != "Updated second thread.\n\n"+thread200Marker {
				http.Error(response, "wrong marker", http.StatusBadRequest)
				return
			}
			mu.Lock()
			posts = append(posts, request.URL.Path)
			mu.Unlock()
			response.WriteHeader(http.StatusCreated)
			writeJSON(response, map[string]any{
				"id": 53, "html_url": server.URL + "/comments/53", "body": body.Body,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	mutator := newMutationTestMutator(t, server)
	request := pullrequest.ResponseRequest{
		Locator: pullrequest.Locator{
			Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
		},
		Batch: pullrequest.ReviewBatch{
			ID: "review-10", ReviewID: "10", CommitSHA: sha('c'),
			Reviewer: "github-user-7", Ready: true,
			ThreadIDs: []string{"thread-100", "thread-200"},
		},
		Response: contracts.PullRequestResponseBody{
			BatchID: "review-10", Summary: "Addressed.",
			Replies: []contracts.PullRequestThreadResponse{
				{ThreadID: "thread-100", Body: "Updated first thread."},
				{ThreadID: "thread-200", Body: "Updated second thread."},
			},
		},
		OperationKey: key,
	}
	result, err := mutator.PublishReviewResponse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationKey != key || result.ExternalID != "review-10" ||
		result.URL != server.URL+"/acme/widget/pull/42" {
		t.Fatalf("review response result = %+v", result)
	}
	mu.Lock()
	if len(posts) != 1 || posts[0] != "/repos/acme/widget/pulls/42/comments/200/replies" {
		t.Fatalf("review response posts = %#v", posts)
	}
	mu.Unlock()

	request.Response.Replies[1].ThreadID = "thread-999"
	if _, err := mutator.PublishReviewResponse(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "authorized") {
		t.Fatalf("unauthorized response error = %v", err)
	}
}

func TestMutatorRejectsRecoveredReplyMarkerOnTheWrongAuthorizedThread(t *testing.T) {
	key := mutationOperationKey('7')
	summaryMarker := "<!-- Jetbridge-Operation: respond_to_review " + key + " summary -->"
	threadMarker := "<!-- Jetbridge-Operation: respond_to_review " + key + " thread thread-100 -->"
	var server *httptest.Server
	var posts int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/pulls/42":
			writeJSON(response, mutationPullFixture(server.URL, "open"))
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/issues/42/comments":
			writeJSON(response, []any{map[string]any{
				"id": 51, "html_url": server.URL + "/comments/51",
				"body": "Addressed.\n\n" + summaryMarker,
			}})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/pulls/42/comments":
			writeJSON(response, []any{map[string]any{
				"id": 52, "html_url": server.URL + "/comments/52",
				"body":           "Updated first thread.\n\n" + threadMarker,
				"in_reply_to_id": 999,
			}})
		default:
			if request.Method == http.MethodPost {
				posts++
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	mutator := newMutationTestMutator(t, server)
	_, err := mutator.PublishReviewResponse(context.Background(), pullrequest.ResponseRequest{
		Locator: pullrequest.Locator{
			Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42",
		},
		Batch: pullrequest.ReviewBatch{
			ID: "review-10", ReviewID: "10", CommitSHA: sha('c'),
			Reviewer: "github-user-7", Ready: true, ThreadIDs: []string{"thread-100"},
		},
		Response: contracts.PullRequestResponseBody{
			BatchID: "review-10", Summary: "Addressed.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: "thread-100", Body: "Updated first thread.",
			}},
		},
		OperationKey: key,
	})
	if err == nil || !strings.Contains(err.Error(), "thread") {
		t.Fatalf("wrong-thread recovery error = %v", err)
	}
	if posts != 0 {
		t.Fatalf("provider posts = %d, want none after wrong-thread recovery", posts)
	}
}

type successfulBranchWriter struct{}

func (successfulBranchWriter) CompareAndSwapBranch(
	_ context.Context,
	mutation pullrequest.BranchMutation,
) (pullrequest.BranchResult, error) {
	return pullrequest.BranchResult{HeadSHA: mutation.NewSourceSHA, Applied: true}, nil
}

func newMutationTestMutator(t *testing.T, server *httptest.Server) *Mutator {
	t.Helper()
	mutator, err := NewMutator(
		server.URL,
		tokenFunc(func(context.Context) (string, error) { return "test-token", nil }),
		successfulBranchWriter{},
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return mutator
}

func mutationPullFixture(serverURL, state string) map[string]any {
	return map[string]any{
		"number": 42, "html_url": serverURL + "/acme/widget/pull/42",
		"state": state, "merged": false, "body": "body",
		"head": map[string]any{"ref": "agent/upgrade", "sha": sha('c'), "repo": map[string]any{"full_name": "acme/widget"}},
		"base": map[string]any{"ref": "main", "sha": sha('b'), "repo": map[string]any{"full_name": "acme/widget"}},
	}
}

func mutationOperationKey(character rune) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		panic(fmt.Sprintf("write test JSON: %v", err))
	}
}
