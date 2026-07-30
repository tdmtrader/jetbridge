package azuredevops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/conformance"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestMutatorCreatesRefWithExactAzureREST71CAS(t *testing.T) {
	var server *httptest.Server
	var updateRequests int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		if request.URL.Query().Get("api-version") != "7.1" {
			http.Error(response, "REST version is not pinned", http.StatusBadRequest)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == mutationRepositoryPath()+"/refs":
			switch request.URL.Query().Get("filter") {
			case "heads/main":
				writeMutationJSON(t, response, map[string]any{
					"count": 1,
					"value": []any{map[string]any{
						"name": "refs/heads/main", "objectId": sha('b'),
					}},
				})
			case "heads/agent/upgrade":
				writeMutationJSON(t, response, map[string]any{
					"count": 0, "value": []any{},
				})
			default:
				http.Error(response, "unexpected ref filter", http.StatusBadRequest)
			}
		case request.Method == http.MethodPost && request.URL.Path == mutationRepositoryPath()+"/refs":
			updateRequests++
			var updates []struct {
				Name        string `json:"name"`
				OldObjectID string `json:"oldObjectId"`
				NewObjectID string `json:"newObjectId"`
			}
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&updates); err != nil {
				t.Errorf("decode update refs request: %v", err)
				http.Error(response, "invalid request", http.StatusBadRequest)
				return
			}
			if len(updates) != 1 ||
				updates[0].Name != "refs/heads/agent/upgrade" ||
				updates[0].OldObjectID != strings.Repeat("0", 40) ||
				updates[0].NewObjectID != sha('c') {
				t.Errorf("update refs request = %#v", updates)
				http.Error(response, "wrong lease", http.StatusConflict)
				return
			}
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(fixture(t, "ref_update_succeeded.json"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.CompareAndSwapBranch(context.Background(), pullrequest.BranchMutation{
		Locator: pullrequest.Locator{
			Provider:   pullrequest.ProviderAzureDevOps,
			Repository: testProject + "/" + testRepositoryID,
		},
		Ref:               "refs/heads/agent/upgrade",
		ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: false},
		TargetRef:         "refs/heads/main",
		ExpectedTargetSHA: sha('b'),
		NewSourceSHA:      sha('c'),
		OperationKey:      mutationOperationKey('1'),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadSHA != sha('c') || !result.Applied || updateRequests != 1 {
		t.Fatalf("branch result = %+v, update requests = %d", result, updateRequests)
	}
}

func TestMutatorFindsThenCreatesExactlyMarkedPullRequest(t *testing.T) {
	key := mutationOperationKey('2')
	var server *httptest.Server
	var createRequests int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		if request.URL.Query().Get("api-version") != "7.1" {
			http.Error(response, "REST version is not pinned", http.StatusBadRequest)
			return
		}
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests":
			query := request.URL.Query()
			if query.Get("searchCriteria.sourceRefName") != "refs/heads/agent/upgrade" ||
				query.Get("searchCriteria.targetRefName") != "refs/heads/main" ||
				query.Get("searchCriteria.status") != "all" {
				http.Error(response, "wrong pull request search", http.StatusBadRequest)
				return
			}
			writeMutationJSON(t, response, map[string]any{
				"count": 0, "value": []any{},
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests":
			createRequests++
			var body map[string]any
			decoder := json.NewDecoder(request.Body)
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode pull request request: %v", err)
				http.Error(response, "invalid request", http.StatusBadRequest)
				return
			}
			wantDescription := "Validated change.\n\n<!-- Jetbridge-Operation: create_pr " +
				key + " -->"
			if len(body) != 4 ||
				body["sourceRefName"] != "refs/heads/agent/upgrade" ||
				body["targetRefName"] != "refs/heads/main" ||
				body["title"] != "Upgrade widget" ||
				body["description"] != wantDescription {
				t.Errorf("create pull request body = %#v", body)
				http.Error(response, "wrong create request", http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusCreated)
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				wantDescription,
			))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.FindOrCreatePullRequest(
		context.Background(),
		pullrequest.CreateRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
			},
			SourceRef:    "refs/heads/agent/upgrade",
			SourceSHA:    sha('c'),
			TargetRef:    "refs/heads/main",
			TargetSHA:    sha('b'),
			Title:        "Upgrade widget",
			Body:         "Validated change.",
			OperationKey: key,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalID != "42" ||
		result.Repository != testProject+"/"+testRepositoryID ||
		result.Provider != pullrequest.ProviderAzureDevOps ||
		result.State != contracts.PullRequestActive ||
		result.SourceRef != "refs/heads/agent/upgrade" ||
		result.SourceSHA != sha('c') ||
		result.TargetRef != "refs/heads/main" ||
		result.TargetSHA != sha('b') ||
		result.URL != server.URL+"/project/_git/repo-id/pullrequest/42" ||
		createRequests != 1 {
		t.Fatalf("created pull request = %+v, creates = %d", result, createRequests)
	}
}

func TestMutatorPublishesValidationStatusForExactIterationAndHead(t *testing.T) {
	key := mutationOperationKey('3')
	var server *httptest.Server
	var statusPosts int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		if request.URL.Query().Get("api-version") != "7.1" {
			http.Error(response, "REST version is not pinned", http.StatusBadRequest)
			return
		}
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/iterations":
			writeMutationJSON(t, response, map[string]any{
				"count": 2,
				"value": []any{
					map[string]any{
						"id":              1,
						"sourceRefCommit": map[string]any{"commitId": sha('a')},
						"targetRefCommit": map[string]any{"commitId": sha('b')},
					},
					map[string]any{
						"id":              2,
						"sourceRefCommit": map[string]any{"commitId": sha('c')},
						"targetRefCommit": map[string]any{"commitId": sha('b')},
					},
				},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/statuses":
			writeMutationJSON(t, response, map[string]any{
				"count": 0, "value": []any{},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Validated.",
			))
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/statuses":
			statusPosts++
			var body struct {
				State       string `json:"state"`
				Description string `json:"description"`
				TargetURL   string `json:"targetUrl"`
				Context     struct {
					Name  string `json:"name"`
					Genre string `json:"genre"`
				} `json:"context"`
				IterationID int64 `json:"iterationId"`
			}
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode status request: %v", err)
				http.Error(response, "invalid request", http.StatusBadRequest)
				return
			}
			if body.State != "succeeded" ||
				body.Description != "Jetbridge validation passed" ||
				body.TargetURL != "https://ci.example/runs/91" ||
				body.Context.Name != "jetbridge/"+key ||
				body.Context.Genre != "jetbridge" ||
				body.IterationID != 2 {
				t.Errorf("status request = %#v", body)
				http.Error(response, "wrong status request", http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusCreated)
			writeMutationJSON(t, response, map[string]any{
				"id":          77,
				"state":       body.State,
				"description": body.Description,
				"targetUrl":   body.TargetURL,
				"context": map[string]any{
					"name": body.Context.Name, "genre": body.Context.Genre,
				},
				"iterationId": body.IterationID,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.PublishValidationStatus(
		context.Background(),
		pullrequest.StatusRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			SourceSHA:    sha('c'),
			State:        "success",
			Description:  "Jetbridge validation passed",
			TargetURL:    "https://ci.example/runs/91",
			OperationKey: key,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationKey != key || result.ExternalID != "77" ||
		result.URL != server.URL+"/project/_git/repo-id/pullrequest/42" ||
		statusPosts != 1 {
		t.Fatalf("status result = %+v, posts = %d", result, statusPosts)
	}
}

func TestMutatorPublishesSummaryAndRepliesOnlyToAuthorizedThreads(t *testing.T) {
	key := mutationOperationKey('4')
	summaryMarker := "<!-- Jetbridge-Operation: respond_to_review " + key + " summary -->"
	replyMarker := "<!-- Jetbridge-Operation: respond_to_review " + key + " thread thread-148 -->"
	var server *httptest.Server
	var summaryPosts, replyPosts int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		if request.URL.Query().Get("api-version") != "7.1" {
			http.Error(response, "REST version is not pinned", http.StatusBadRequest)
			return
		}
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Validated.",
			))
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/threads":
			writeMutationJSON(t, response, map[string]any{
				"count": 1,
				"value": []any{map[string]any{
					"id":     148,
					"status": "active",
					"comments": []any{map[string]any{
						"id": 1, "parentCommentId": 0,
						"content":     "Please make the edge case explicit.",
						"commentType": "text", "isDeleted": false,
					}},
					"properties": map[string]any{},
					"isDeleted":  false,
				}},
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/threads":
			summaryPosts++
			var body struct {
				Comments []struct {
					ParentCommentID int64  `json:"parentCommentId"`
					Content         string `json:"content"`
					CommentType     int    `json:"commentType"`
				} `json:"comments"`
				Status int `json:"status"`
			}
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode summary thread request: %v", err)
				http.Error(response, "invalid request", http.StatusBadRequest)
				return
			}
			want := "Addressed all completed feedback.\n\n" + summaryMarker
			if len(body.Comments) != 1 ||
				body.Comments[0].ParentCommentID != 0 ||
				body.Comments[0].Content != want ||
				body.Comments[0].CommentType != 1 ||
				body.Status != 1 {
				t.Errorf("summary thread request = %#v", body)
				http.Error(response, "wrong summary", http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusCreated)
			writeMutationJSON(t, response, map[string]any{
				"id": 200,
				"comments": []any{map[string]any{
					"id": 1, "parentCommentId": 0, "content": want,
					"commentType": "text", "isDeleted": false,
				}},
				"properties": map[string]any{}, "isDeleted": false,
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/threads/148/comments":
			replyPosts++
			var body struct {
				ParentCommentID int64  `json:"parentCommentId"`
				Content         string `json:"content"`
				CommentType     int    `json:"commentType"`
			}
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode thread reply request: %v", err)
				http.Error(response, "invalid request", http.StatusBadRequest)
				return
			}
			want := "Made the edge case explicit.\n\n" + replyMarker
			if body.ParentCommentID != 1 || body.Content != want ||
				body.CommentType != 1 {
				t.Errorf("thread reply request = %#v", body)
				http.Error(response, "wrong reply", http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusCreated)
			writeMutationJSON(t, response, map[string]any{
				"id": 2, "parentCommentId": 1, "content": want,
				"commentType": "text", "isDeleted": false,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.PublishReviewResponse(
		context.Background(),
		pullrequest.ResponseRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			Batch: pullrequest.ReviewBatch{
				ID:        "vote-144",
				ReviewID:  "144",
				CommitSHA: sha('c'),
				Reviewer:  "azure-user:reviewer-id",
				Ready:     true,
				ThreadIDs: []string{"thread-148"},
			},
			Response: contracts.PullRequestResponseBody{
				BatchID: "vote-144",
				Summary: "Addressed all completed feedback.",
				Replies: []contracts.PullRequestThreadResponse{{
					ThreadID: "thread-148",
					Body:     "Made the edge case explicit.",
				}},
			},
			OperationKey: key,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationKey != key || result.ExternalID != "vote-144" ||
		result.URL != server.URL+"/project/_git/repo-id/pullrequest/42" ||
		summaryPosts != 1 || replyPosts != 1 {
		t.Fatalf(
			"response result = %+v, summary/reply posts = %d/%d",
			result, summaryPosts, replyPosts,
		)
	}
}

func TestMutatorProviderNeutralConformance(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid conformance request reached Azure DevOps")
		return nil, nil
	})}
	adapter, err := New(
		"https://dev.azure.com/acme",
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	conformance.RunMutationSuite(t, conformance.MutationSubject{
		Mutator:    adapter,
		Provider:   pullrequest.ProviderAzureDevOps,
		Repository: testProject + "/" + testRepositoryID,
		ExternalID: "42",
	})
}

func TestMutatorPaginatesExactRefsBeforeClassifyingStaleCAS(t *testing.T) {
	var server *httptest.Server
	var updateRequests int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/refs":
			filter := request.URL.Query().Get("filter")
			continuation := request.URL.Query().Get("continuationToken")
			switch {
			case filter == "heads/main" && continuation == "":
				response.Header().Set("x-ms-continuationtoken", "next-target")
				writeMutationJSON(t, response, map[string]any{
					"count": 1,
					"value": []any{map[string]any{
						"name": "refs/heads/main-old", "objectId": sha('a'),
					}},
				})
			case filter == "heads/main" && continuation == "next-target":
				writeMutationJSON(t, response, map[string]any{
					"count": 1,
					"value": []any{map[string]any{
						"name": "refs/heads/main", "objectId": sha('b'),
					}},
				})
			case filter == "heads/agent/upgrade" && continuation == "":
				writeMutationJSON(t, response, map[string]any{
					"count": 1,
					"value": []any{map[string]any{
						"name": "refs/heads/agent/upgrade", "objectId": sha('a'),
					}},
				})
			default:
				http.Error(response, "unexpected ref page", http.StatusBadRequest)
			}
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/refs":
			updateRequests++
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(fixture(t, "ref_update_stale.json"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.CompareAndSwapBranch(
		context.Background(),
		pullrequest.BranchMutation{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			Ref: "refs/heads/agent/upgrade",
			ExpectedSource: contracts.PullRequestHeadExpectation{
				Exists: true, SHA: sha('a'),
			},
			TargetRef:         "refs/heads/main",
			ExpectedTargetSHA: sha('b'),
			NewSourceSHA:      sha('c'),
			OperationKey:      mutationOperationKey('5'),
		},
	)
	if !errors.Is(err, ErrStaleSource) || updateRequests != 1 {
		t.Fatalf("stale ref update = %v, requests = %d", err, updateRequests)
	}
}

func TestMutatorRejectsSummaryRecoveryInsideAuthorizedReviewThread(t *testing.T) {
	key := mutationOperationKey('6')
	marker := "<!-- Jetbridge-Operation: respond_to_review " + key + " summary -->"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Validated.",
			))
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/threads":
			writeMutationJSON(t, response, map[string]any{
				"count": 1,
				"value": []any{map[string]any{
					"id": 148,
					"comments": []any{map[string]any{
						"id": 1, "parentCommentId": 0,
						"content":     "Addressed.\n\n" + marker,
						"commentType": "text", "isDeleted": false,
					}},
					"properties": map[string]any{}, "isDeleted": false,
				}},
			})
		default:
			if request.Method == http.MethodPost {
				t.Errorf("provider write followed invalid summary recovery")
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PublishReviewResponse(
		context.Background(),
		pullrequest.ResponseRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			Batch: pullrequest.ReviewBatch{
				ID: "vote-144", ReviewID: "144", CommitSHA: sha('c'),
				Reviewer:  "azure-user:reviewer-id",
				Ready:     true,
				ThreadIDs: []string{"thread-148"},
			},
			Response: contracts.PullRequestResponseBody{
				BatchID: "vote-144", Summary: "Addressed.",
			},
			OperationKey: key,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("authorized-thread summary recovery error = %v", err)
	}
}

func TestMutatorRejectsUnexpectedPaginationOnCreatedPullRequest(t *testing.T) {
	key := mutationOperationKey('7')
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests":
			writeMutationJSON(t, response, map[string]any{
				"count": 0, "value": []any{},
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests":
			response.Header().Set("x-ms-continuationtoken", "unexpected")
			response.WriteHeader(http.StatusCreated)
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Validated.\n\n<!-- Jetbridge-Operation: create_pr "+key+" -->",
			))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.FindOrCreatePullRequest(
		context.Background(),
		pullrequest.CreateRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
			},
			SourceRef: "refs/heads/agent/upgrade", SourceSHA: sha('c'),
			TargetRef: "refs/heads/main", TargetSHA: sha('b'),
			Title: "Upgrade widget", Body: "Validated.", OperationKey: key,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "paginated") {
		t.Fatalf("unexpected create pagination error = %v", err)
	}
}

func TestMutatorRejectsStatusWhenLatestIterationTargetIsNotCurrent(t *testing.T) {
	key := mutationOperationKey('8')
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/iterations":
			writeMutationJSON(t, response, map[string]any{
				"count": 2,
				"value": []any{
					map[string]any{
						"id":              2,
						"sourceRefCommit": map[string]any{"commitId": sha('c')},
						"targetRefCommit": map[string]any{"commitId": sha('b')},
					},
					map[string]any{
						"id":              3,
						"sourceRefCommit": map[string]any{"commitId": sha('c')},
						"targetRefCommit": map[string]any{"commitId": sha('d')},
					},
				},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/statuses":
			writeMutationJSON(t, response, map[string]any{
				"count": 0, "value": []any{},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Validated.",
			))
		case request.Method == http.MethodPost &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/statuses":
			writeMutationJSON(t, response, map[string]any{
				"id": 77, "state": "succeeded",
				"description": "Jetbridge validation passed",
				"targetUrl":   "https://ci.example/runs/91",
				"context": map[string]any{
					"name": "jetbridge/" + key, "genre": "jetbridge",
				},
				"iterationId": 3,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.PublishValidationStatus(
		context.Background(),
		pullrequest.StatusRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			SourceSHA: sha('c'), State: "success",
			Description:  "Jetbridge validation passed",
			TargetURL:    "https://ci.example/runs/91",
			OperationKey: key,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "iteration") {
		t.Fatalf("mismatched latest iteration error = %v", err)
	}
}

func TestMutatorRecoversExactStatusBeforeReadingAdvancedCurrentHead(t *testing.T) {
	key := mutationOperationKey('9')
	var server *httptest.Server
	var pullReads, statusPosts int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/iterations":
			writeMutationJSON(t, response, map[string]any{
				"count": 2,
				"value": []any{
					map[string]any{
						"id":              2,
						"sourceRefCommit": map[string]any{"commitId": sha('c')},
						"targetRefCommit": map[string]any{"commitId": sha('b')},
					},
					map[string]any{
						"id":              3,
						"sourceRefCommit": map[string]any{"commitId": sha('d')},
						"targetRefCommit": map[string]any{"commitId": sha('b')},
					},
				},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42/statuses":
			writeMutationJSON(t, response, map[string]any{
				"count": 1,
				"value": []any{map[string]any{
					"id": 77, "state": "succeeded",
					"description": "Jetbridge validation passed",
					"targetUrl":   "https://ci.example/runs/91",
					"context": map[string]any{
						"name": "jetbridge/" + key, "genre": "jetbridge",
					},
					"iterationId": 2,
				}},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			pullReads++
			http.Error(response, "recovery must precede current-head read", http.StatusConflict)
		default:
			if request.Method == http.MethodPost {
				statusPosts++
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.PublishValidationStatus(
		context.Background(),
		pullrequest.StatusRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
				ExternalID: "42",
			},
			SourceSHA: sha('c'), State: "success",
			Description:  "Jetbridge validation passed",
			TargetURL:    "https://ci.example/runs/91",
			OperationKey: key,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalID != "77" || pullReads != 0 || statusPosts != 0 {
		t.Fatalf(
			"recovered status = %+v, pull reads/posts = %d/%d",
			result, pullReads, statusPosts,
		)
	}
}

func TestMutatorDetailReadsAzureTruncatedSearchDescriptionBeforeRecovery(t *testing.T) {
	key := mutationOperationKey('a')
	exactDescription := "Validated.\n\n<!-- Jetbridge-Operation: create_pr " + key + " -->"
	var server *httptest.Server
	var detailReads, creates int
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertMutationRequest(t, request)
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests":
			candidate := azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				"Azure collection descriptions are truncated before this marker",
			)
			writeMutationJSON(t, response, map[string]any{
				"count": 1, "value": []any{candidate},
			})
		case request.Method == http.MethodGet &&
			request.URL.Path == mutationRepositoryPath()+"/pullRequests/42":
			detailReads++
			writeMutationJSON(t, response, azureMutationPull(
				42,
				"refs/heads/agent/upgrade", sha('c'),
				"refs/heads/main", sha('b'),
				exactDescription,
			))
		default:
			if request.Method == http.MethodPost {
				creates++
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	adapter, err := New(
		server.URL,
		testProject,
		testRepositoryID,
		tokenFunc(func(context.Context) (string, error) { return "mutation-secret", nil }),
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.FindOrCreatePullRequest(
		context.Background(),
		pullrequest.CreateRequest{
			Locator: pullrequest.Locator{
				Provider:   pullrequest.ProviderAzureDevOps,
				Repository: testProject + "/" + testRepositoryID,
			},
			SourceRef: "refs/heads/agent/upgrade", SourceSHA: sha('c'),
			TargetRef: "refs/heads/main", TargetSHA: sha('b'),
			Title: "Upgrade widget", Body: "Validated.", OperationKey: key,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalID != "42" || detailReads != 1 || creates != 0 {
		t.Fatalf(
			"recovered pull request = %+v, detail reads/creates = %d/%d",
			result, detailReads, creates,
		)
	}
}

func mutationRepositoryPath() string {
	return "/" + testProject + "/_apis/git/repositories/" + testRepositoryID
}

func mutationOperationKey(character rune) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func assertMutationRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer mutation-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Errorf("accept = %q", got)
	}
}

func writeMutationJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func azureMutationPull(
	id int64,
	sourceRef, sourceSHA, targetRef, targetSHA, description string,
) map[string]any {
	return map[string]any{
		"repository": map[string]any{
			"id": testRepositoryID, "name": "widget", "isFork": false,
			"project": map[string]any{"id": "project-id", "name": testProject},
		},
		"pullRequestId": id,
		"status":        "active",
		"mergeStatus":   "queued",
		"sourceRefName": sourceRef,
		"targetRefName": targetRef,
		"lastMergeSourceCommit": map[string]any{
			"commitId": sourceSHA,
		},
		"lastMergeTargetCommit": map[string]any{
			"commitId": targetSHA,
		},
		"description":        description,
		"supportsIterations": true,
		"forkSource":         nil,
	}
}
