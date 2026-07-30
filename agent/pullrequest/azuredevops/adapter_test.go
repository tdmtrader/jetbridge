package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	testProject      = "project"
	testRepositoryID = "repo-id"
	testExternalID   = "42"
)

type rotatingToken struct {
	mu    sync.Mutex
	calls int
}

func (source *rotatingToken) Token(context.Context) (string, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return fmt.Sprintf("azure-token-%d", source.calls), nil
}

func (source *rotatingToken) count() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

type tokenFunc func(context.Context) (string, error)

func (function tokenFunc) Token(ctx context.Context) (string, error) {
	return function(ctx)
}

type fixtureService struct {
	t       *testing.T
	mu      sync.Mutex
	bodies  map[string][]byte
	headers map[string]http.Header
	seen    []string
}

func newFixtureService(t *testing.T) *fixtureService {
	t.Helper()
	return &fixtureService{
		t: t,
		bodies: map[string][]byte{
			"":            fixture(t, "pull_request_active.json"),
			"/iterations": fixture(t, "iterations_page_1.json"),
			"/threads":    fixture(t, "threads_page_1.json"),
			"/reviewers":  fixture(t, "reviewers.json"),
		},
		headers: map[string]http.Header{},
	}
}

func (service *fixtureService) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if request.Method != http.MethodGet {
		service.t.Errorf("method = %s", request.Method)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		service.t.Errorf("accept = %q", got)
	}
	if got := request.Header.Get("User-Agent"); got != userAgent {
		service.t.Errorf("user agent = %q", got)
	}
	if got := request.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer azure-token-") {
		service.t.Errorf("authorization = %q", got)
	}
	query := request.URL.Query()
	if values := query["api-version"]; len(values) != 1 || values[0] != "7.1" {
		service.t.Errorf("api-version = %#v", values)
	}
	for key := range query {
		if key != "api-version" && key != "continuationToken" {
			service.t.Errorf("unexpected query key %q", key)
		}
	}
	prefix := "/" + testProject + "/_apis/git/repositories/" + testRepositoryID + "/pullRequests/" + testExternalID
	if !strings.HasPrefix(request.URL.Path, prefix) {
		service.t.Errorf("path = %q", request.URL.Path)
		http.NotFound(response, request)
		return
	}
	suffix := strings.TrimPrefix(request.URL.Path, prefix)
	service.seen = append(service.seen, suffix+"?"+request.URL.RawQuery)
	for key, values := range service.headers[suffix] {
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	body, found := service.bodies[suffix]
	if !found {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(body)
}

func (service *fixtureService) replace(name, fixtureName string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.bodies[name] = fixture(service.t, fixtureName)
}

func (service *fixtureService) setBody(name string, body []byte) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.bodies[name] = body
}

func (service *fixtureService) snapshotBody(name string) []byte {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]byte(nil), service.bodies[name]...)
}

func TestObserveUsesAzureREST71AndAcknowledgesOneVoteTransition(t *testing.T) {
	service := newFixtureService(t)
	server := httptest.NewServer(service)
	defer server.Close()
	tokens := &rotatingToken{}
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, tokens, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := testLocator()

	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != contracts.PullRequestActive || first.Mergeability != contracts.PullRequestMergeable {
		t.Fatalf("state = %s/%s", first.State, first.Mergeability)
	}
	if first.URL != server.URL+"/project/_git/repo-id/pullrequest/42" {
		t.Fatalf("derived url = %q", first.URL)
	}
	if first.SourceRef != "refs/heads/feature/widget" || first.SourceSHA != sha('a') || first.TargetRef != "refs/heads/main" || first.TargetSHA != sha('b') || first.Iteration != "2" {
		t.Fatalf("heads = %#v", first)
	}
	if len(first.ReviewBatches) != 1 {
		t.Fatalf("batches = %#v", first.ReviewBatches)
	}
	batch := first.ReviewBatches[0]
	if batch.ID != "vote-144" || batch.ReviewID != "144" || batch.Reviewer != "azure-user:reviewer-id" || batch.CommitSHA != sha('a') || !batch.Ready {
		t.Fatalf("batch = %#v", batch)
	}
	if got := strings.Join(batch.ThreadIDs, ","); got != "thread-148" {
		t.Fatalf("reply authority = %q", got)
	}
	if len(first.Threads) != 2 || first.Threads[0].ID != "thread-148" || first.Threads[1].ID != "thread-149" {
		t.Fatalf("threads = %#v", first.Threads)
	}
	anchored := first.Threads[0]
	if anchored.Iteration != "2" || anchored.Anchor == nil || anchored.Anchor.Path != "pkg/example.go" || anchored.Anchor.StartLine != 12 || anchored.Anchor.EndLine != 14 {
		t.Fatalf("anchored thread = %#v", anchored)
	}
	if len(anchored.Comments) != 2 || anchored.Comments[0].CommitSHA != sha('a') || anchored.Comments[0].Author != "azure-user:reviewer-id" {
		t.Fatalf("comments = %#v", anchored.Comments)
	}
	if first.Threads[1].Anchor != nil || first.Threads[1].Iteration != "2" {
		t.Fatalf("resolved context = %#v", first.Threads[1])
	}
	if first.Cursor == "" {
		t.Fatal("cursor is empty")
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("observation is invalid: %v", err)
	}

	replay, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Cursor != first.Cursor || len(replay.ReviewBatches) != 1 || replay.ReviewBatches[0].ID != batch.ID {
		t.Fatalf("empty-cursor replay = %#v", replay)
	}
	acknowledged, err := observer.Observe(context.Background(), locator, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.Cursor != first.Cursor || len(acknowledged.ReviewBatches) != 0 {
		t.Fatalf("acknowledged replay = %#v", acknowledged)
	}
	if tokens.count() != 12 {
		t.Fatalf("token calls = %d, want one per request", tokens.count())
	}
}

func TestObserveSuppressesRepeatedWaitingVoteAndRearmsAfterLeaving(t *testing.T) {
	service := newFixtureService(t)
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	locator := testLocator()
	first, err := observer.Observe(context.Background(), locator, "")
	if err != nil {
		t.Fatal(err)
	}

	page := decodeObject(t, service.snapshotBody("/threads"))
	threads := page["value"].([]any)
	threads = append(threads, voteThread(145, "2026-07-29T09:21:00Z", "reviewer-id", -5))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	repeated, err := observer.Observe(context.Background(), locator, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.ReviewBatches) != 0 {
		t.Fatalf("repeated -5 produced %#v", repeated.ReviewBatches)
	}

	threads = append(threads,
		voteThread(146, "2026-07-29T09:22:00Z", "reviewer-id", 0),
		voteThread(147, "2026-07-29T09:23:00Z", "reviewer-id", -5),
	)
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	rearmed, err := observer.Observe(context.Background(), locator, repeated.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(rearmed.ReviewBatches) != 1 || rearmed.ReviewBatches[0].ID != "vote-147" {
		t.Fatalf("rearmed batches = %#v", rearmed.ReviewBatches)
	}
}

func TestReadyBatchExcludesFeedbackPublishedAfterVoteTransition(t *testing.T) {
	service := newFixtureService(t)
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	page := decodeObject(t, service.snapshotBody("/threads"))
	threads := page["value"].([]any)
	threads = append(threads, feedbackThread(150, "2026-07-29T09:30:00Z"))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	replay, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Cursor != first.Cursor || len(replay.Threads) != len(first.Threads) {
		t.Fatalf("post-vote feedback changed completed batch: %#v", replay)
	}
}

func TestCursorBindsCanonicalPullRequestHeads(t *testing.T) {
	service := newFixtureService(t)
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	body := decodeObject(t, service.snapshotBody(""))
	body["lastMergeSourceCommit"].(map[string]any)["commitId"] = sha('f')
	service.setBody("", encodeJSON(t, body))
	iterations := decodeObject(t, service.snapshotBody("/iterations"))
	iterations["value"].([]any)[1].(map[string]any)["sourceRefCommit"].(map[string]any)["commitId"] = sha('f')
	service.setBody("/iterations", encodeJSON(t, iterations))
	changed, err := observer.Observe(context.Background(), testLocator(), first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Cursor == first.Cursor {
		t.Fatal("cursor did not bind the canonical source head")
	}
}

func TestConflictedCursorIgnoresOrdinaryVoteChurnButAdvancesForReadyTransition(t *testing.T) {
	service := newFixtureService(t)
	pull := decodeObject(t, service.snapshotBody(""))
	pull["mergeStatus"] = "conflicts"
	service.setBody("", encodeJSON(t, pull))
	page := decodeObject(t, service.snapshotBody("/threads"))
	allThreads := page["value"].([]any)
	threads := []any{allThreads[0], allThreads[2], allThreads[3]}
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	reviewers := decodeObject(t, service.snapshotBody("/reviewers"))
	reviewer := reviewers["value"].([]any)[0].(map[string]any)
	reviewer["vote"] = float64(10)
	service.setBody("/reviewers", encodeJSON(t, reviewers))
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	initial, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Mergeability != contracts.PullRequestConflicted || len(initial.ReviewBatches) != 0 {
		t.Fatalf("initial conflict = %#v", initial)
	}
	threads = append(threads, voteThread(145, "2026-07-29T09:21:00Z", "reviewer-id", 5))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	reviewer["vote"] = float64(5)
	service.setBody("/reviewers", encodeJSON(t, reviewers))
	ordinary, err := observer.Observe(context.Background(), testLocator(), initial.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Cursor != initial.Cursor || len(ordinary.ReviewBatches) != 0 {
		t.Fatalf("ordinary vote churn changed conflict identity: %#v", ordinary)
	}

	threads = append(threads, voteThread(146, "2026-07-29T09:22:00Z", "reviewer-id", -5))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	reviewer["vote"] = float64(-5)
	service.setBody("/reviewers", encodeJSON(t, reviewers))
	ready, err := observer.Observe(context.Background(), testLocator(), ordinary.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Cursor == ordinary.Cursor || len(ready.ReviewBatches) != 1 || ready.ReviewBatches[0].ID != "vote-146" {
		t.Fatalf("ready transition did not advance cursor: %#v", ready)
	}
}

func TestObserveSelectsEarliestQualifyingCurrentEpisode(t *testing.T) {
	service := newFixtureService(t)
	page := decodeObject(t, service.snapshotBody("/threads"))
	threads := page["value"].([]any)
	threads = append(threads, voteThread(150, "2026-07-29T09:30:00Z", "second-reviewer", -5))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	reviewers := decodeObject(t, service.snapshotBody("/reviewers"))
	values := reviewers["value"].([]any)
	values = append(values, map[string]any{"id": "second-reviewer", "vote": float64(-5), "isContainer": false, "inactive": false})
	reviewers["value"], reviewers["count"] = values, len(values)
	service.setBody("/reviewers", encodeJSON(t, reviewers))
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ReviewBatches) != 1 || first.ReviewBatches[0].ID != "vote-144" {
		t.Fatalf("first batch = %#v", first.ReviewBatches)
	}
	second, err := observer.Observe(context.Background(), testLocator(), first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ReviewBatches) != 1 || second.ReviewBatches[0].ID != "vote-150" {
		t.Fatalf("second batch = %#v", second.ReviewBatches)
	}
}

func TestRemovedHistoricalReviewerDoesNotBlockCurrentReadyReviewer(t *testing.T) {
	service := newFixtureService(t)
	page := decodeObject(t, service.snapshotBody("/threads"))
	threads := page["value"].([]any)
	threads = append(threads, voteThread(140, "2026-07-29T09:00:00Z", "former-reviewer", 10))
	page["value"], page["count"] = threads, len(threads)
	service.setBody("/threads", encodeJSON(t, page))
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ReviewBatches) != 1 || got.ReviewBatches[0].ID != "vote-144" {
		t.Fatalf("ready batch = %#v", got.ReviewBatches)
	}
}

func TestLifecycleAndMergeStatusMappingsAreClosed(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want contracts.PullRequestState
		ok   bool
	}{
		{"active", contracts.PullRequestActive, true},
		{"completed", contracts.PullRequestCompleted, true},
		{"abandoned", contracts.PullRequestAbandoned, true},
		{"notSet", "", false},
		{"all", "", false},
		{"future", "", false},
		{"", "", false},
	} {
		got, err := normalizeLifecycle(test.raw)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("lifecycle %q = %q, %v", test.raw, got, err)
		}
	}
	for _, test := range []struct {
		raw  string
		want contracts.PullRequestMergeability
	}{
		{"succeeded", contracts.PullRequestMergeable},
		{"conflicts", contracts.PullRequestConflicted},
		{"rejectedByPolicy", contracts.PullRequestPolicyBlocked},
		{"queued", contracts.PullRequestUnknown},
		{"notSet", contracts.PullRequestUnknown},
		{"failure", contracts.PullRequestUnknown},
		{"unknown", contracts.PullRequestUnknown},
		{"future", contracts.PullRequestUnknown},
		{"", contracts.PullRequestUnknown},
	} {
		if got := normalizeMergeStatus(test.raw); got != test.want {
			t.Fatalf("merge status %q = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestReviewerVotesAcceptOnlyTheFiveDocumentedAzureValues(t *testing.T) {
	values := []azureReviewer{
		{ID: "approved", Vote: 10},
		{ID: "suggestions", Vote: 5},
		{ID: "none", Vote: 0},
		{ID: "waiting", Vote: -5},
		{ID: "rejected", Vote: -10},
	}
	got, err := normalizeReviewers(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(values) {
		t.Fatalf("reviewers = %#v", got)
	}
	for _, vote := range []int{-11, -6, -1, 1, 6, 11} {
		if _, err := normalizeReviewers([]azureReviewer{{ID: "reviewer", Vote: vote}}); err == nil {
			t.Fatalf("undocumented vote %d was accepted", vote)
		}
	}
	for index, vote := range []int{10, 5, 0, -5, -10} {
		raw, err := json.Marshal(strconv.Itoa(vote))
		if err != nil {
			t.Fatal(err)
		}
		events, err := normalizeVoteEvents([]azureThread{{
			ID:            int64(index + 1),
			PublishedDate: fmt.Sprintf("2026-07-29T09:0%d:00Z", index),
			Comments: []azureComment{{
				ID: 1, Author: azureIdentity{ID: "service-account"}, Content: "vote update",
				PublishedDate: fmt.Sprintf("2026-07-29T09:0%d:00Z", index), CommentType: "system",
			}},
			Properties: map[string]azureProperty{
				"CodeReviewThreadType":  {Type: "System.String", Value: json.RawMessage(`"VoteUpdate"`)},
				"CodeReviewVotedByTfId": {Type: "System.String", Value: json.RawMessage(`"reviewer"`)},
				"CodeReviewVoteResult":  {Type: "System.String", Value: raw},
			},
		}})
		if err != nil || len(events) != 1 || events[0].Vote != vote {
			t.Fatalf("VoteUpdate %d = %#v, %v", vote, events, err)
		}
	}
}

func TestObserveMapsTerminalStatesWithoutReviewAuthority(t *testing.T) {
	for _, test := range []struct {
		fixture string
		state   contracts.PullRequestState
		merge   contracts.PullRequestMergeability
	}{
		{"pull_request_completed.json", contracts.PullRequestCompleted, contracts.PullRequestMergeable},
		{"pull_request_abandoned.json", contracts.PullRequestAbandoned, contracts.PullRequestUnknown},
	} {
		t.Run(string(test.state), func(t *testing.T) {
			service := newFixtureService(t)
			service.replace("", test.fixture)
			server := httptest.NewServer(service)
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			got, err := observer.Observe(context.Background(), testLocator(), "")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.state || got.Mergeability != test.merge || got.Cursor == "" || len(got.ReviewBatches) != 0 || len(got.Threads) != 0 {
				t.Fatalf("terminal observation = %#v", got)
			}
			if len(service.seen) != 2 {
				t.Fatalf("terminal requests = %#v", service.seen)
			}
		})
	}
}

func TestObserveRejectsMalformedVoteUpdateBagsAndReviewerMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fixtureService)
	}{
		{"missing voter", mutateVoteProperty("CodeReviewVotedByTfId", nil)},
		{"numeric vote", mutateVoteProperty("CodeReviewVoteResult", map[string]any{"$type": "System.Int32", "$value": float64(-5)})},
		{"unsupported vote", mutateVoteProperty("CodeReviewVoteResult", map[string]any{"$type": "System.String", "$value": "-6"})},
		{"malformed thread type", mutateVoteProperty("CodeReviewThreadType", map[string]any{"$type": "System.String", "$value": float64(7)})},
		{"vote hints without type", mutateVoteProperty("CodeReviewThreadType", nil)},
		{"deleted vote thread", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["isDeleted"] = true
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"missing system comment", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["comments"] = []any{}
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"non-system vote comment", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["comments"].([]any)[0].(map[string]any)["commentType"] = "text"
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"vote comment has no author", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["comments"].([]any)[0].(map[string]any)["author"] = map[string]any{}
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"vote comment has no content", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["comments"].([]any)[0].(map[string]any)["content"] = ""
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"vote comment publication is malformed", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["comments"].([]any)[0].(map[string]any)["publishedDate"] = "not-a-time"
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"non-UTC vote publication", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			page["value"].([]any)[1].(map[string]any)["publishedDate"] = "2026-07-29T02:20:00-07:00"
			service.setBody("/threads", encodeJSON(t, page))
		}},
		{"current reviewer vote disagrees", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/reviewers"))
			page["value"].([]any)[0].(map[string]any)["vote"] = float64(0)
			service.setBody("/reviewers", encodeJSON(t, page))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newFixtureService(t)
			test.mutate(service)
			server := httptest.NewServer(service)
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil {
				t.Fatal("malformed vote state was accepted")
			}
		})
	}
}

func TestObserveValidatesRepositoryPullRequestRefsAndHighestIteration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fixtureService)
	}{
		{"repository id", mutatePull("repository.id", "other-repo")},
		{"project", mutatePull("repository.project.name", "other-project")},
		{"pull request id", mutatePull("pullRequestId", float64(43))},
		{"repository fork", mutatePull("repository.isFork", true)},
		{"pull request fork", mutatePull("forkSource", map[string]any{"repository": map[string]any{"id": "fork"}})},
		{"source ref", mutatePull("sourceRefName", "refs/tags/not-a-branch")},
		{"unsafe target ref", mutatePull("targetRefName", "refs/heads/main..bad")},
		{"source head", mutatePull("lastMergeSourceCommit.commitId", sha('f'))},
		{"target head", mutatePull("lastMergeTargetCommit.commitId", "short")},
		{"iteration source mismatch", mutateIteration("sourceRefCommit.commitId", sha('f'))},
		{"iteration target mismatch", mutateIteration("targetRefCommit.commitId", sha('f'))},
		{"iteration id invalid", mutateIteration("id", float64(0))},
		{"duplicate iteration id", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/iterations"))
			page["value"].([]any)[0].(map[string]any)["id"] = float64(2)
			service.setBody("/iterations", encodeJSON(t, page))
		}},
		{"unsupported iterations", mutatePull("supportsIterations", false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newFixtureService(t)
			test.mutate(service)
			server := httptest.NewServer(service)
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil {
				t.Fatal("mismatched provider authority was accepted")
			}
		})
	}
}

func TestObserveValidatesFeedbackThreadsAndIterationContexts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown iteration", func(thread map[string]any) {
			thread["pullRequestThreadContext"].(map[string]any)["iterationContext"].(map[string]any)["secondComparingIteration"] = float64(99)
		}},
		{"inverted iteration", func(thread map[string]any) {
			thread["pullRequestThreadContext"].(map[string]any)["iterationContext"].(map[string]any)["firstComparingIteration"] = float64(3)
		}},
		{"incomplete anchor", func(thread map[string]any) {
			delete(thread["threadContext"].(map[string]any), "rightFileEnd")
		}},
		{"unsafe path", func(thread map[string]any) {
			thread["threadContext"].(map[string]any)["filePath"] = "/../secret"
		}},
		{"unknown status", func(thread map[string]any) { thread["status"] = "future" }},
		{"duplicate comment", func(thread map[string]any) {
			comments := thread["comments"].([]any)
			comments = append(comments, comments[0])
			thread["comments"] = comments
		}},
		{"orphan reply", func(thread map[string]any) {
			thread["comments"].([]any)[1].(map[string]any)["parentCommentId"] = float64(99)
		}},
		{"unknown comment type", func(thread map[string]any) {
			thread["comments"].([]any)[0].(map[string]any)["commentType"] = "future"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newFixtureService(t)
			page := decodeObject(t, service.snapshotBody("/threads"))
			thread := page["value"].([]any)[2].(map[string]any)
			test.mutate(thread)
			service.setBody("/threads", encodeJSON(t, page))
			server := httptest.NewServer(service)
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil {
				t.Fatal("malformed feedback was accepted")
			}
		})
	}
}

func TestObserveExcludesPendingAndDeletedFeedbackFromAuthority(t *testing.T) {
	service := newFixtureService(t)
	page := decodeObject(t, service.snapshotBody("/threads"))
	active := page["value"].([]any)[2].(map[string]any)
	active["status"] = "pending"
	resolved := page["value"].([]any)[3].(map[string]any)
	resolved["isDeleted"] = true
	service.setBody("/threads", encodeJSON(t, page))
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := observer.Observe(context.Background(), testLocator(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Threads) != 0 || len(got.ReviewBatches) != 1 || len(got.ReviewBatches[0].ThreadIDs) != 0 {
		t.Fatalf("pending/deleted feedback = %#v / %#v", got.Threads, got.ReviewBatches)
	}
}

func TestObserveRejectsMalformedEnvelopesBoundsAndPagination(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fixtureService)
	}{
		{"malformed json", func(service *fixtureService) { service.setBody("", []byte("{")) }},
		{"trailing json", func(service *fixtureService) { service.setBody("", append(service.snapshotBody(""), []byte("{}")...)) }},
		{"null collection", func(service *fixtureService) { service.setBody("/iterations", []byte(`{"count":0,"value":null}`)) }},
		{"count mismatch", func(service *fixtureService) { service.setBody("/reviewers", []byte(`{"count":2,"value":[]}`)) }},
		{"oversized body", func(service *fixtureService) { service.setBody("", bytes.Repeat([]byte("x"), (1<<20)+1)) }},
		{"too many threads", func(service *fixtureService) {
			values := make([]any, 513)
			for index := range values {
				values[index] = map[string]any{"id": float64(index + 1), "publishedDate": "2026-07-29T10:00:00Z", "comments": []any{}, "properties": map[string]any{}, "isDeleted": true}
			}
			service.setBody("/threads", encodeJSON(t, map[string]any{"count": len(values), "value": values}))
		}},
		{"too many comments", func(service *fixtureService) {
			page := decodeObject(t, service.snapshotBody("/threads"))
			thread := page["value"].([]any)[2].(map[string]any)
			comments := make([]any, 257)
			for index := range comments {
				comments[index] = map[string]any{"id": float64(index + 1), "parentCommentId": float64(0), "author": map[string]any{"id": "reviewer-id"}, "content": "feedback", "commentType": "text", "isDeleted": false}
			}
			thread["comments"] = comments
			service.setBody("/threads", encodeJSON(t, page))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newFixtureService(t)
			test.mutate(service)
			server := httptest.NewServer(service)
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil {
				t.Fatal("malformed or oversized response was accepted")
			}
		})
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/iterations") {
			response.Header().Set("x-ms-continuationtoken", strconv.Itoa(requests))
			_, _ = io.WriteString(response, `{"count":0,"value":[]}`)
			return
		}
		_, _ = response.Write(fixture(t, "pull_request_active.json"))
	}))
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, tokenFunc(func(context.Context) (string, error) { return "secret", nil }), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil || requests != 9 {
		t.Fatalf("pagination result = requests %d, err %v", requests, err)
	}
}

func TestObserveRejectsMalformedAndRepeatingContinuationTokens(t *testing.T) {
	for _, continuation := range []string{" same ", "bad\nvalue", strings.Repeat("x", 1025)} {
		t.Run(strconv.Itoa(len(continuation)), func(t *testing.T) {
			header := http.Header{"X-Ms-Continuationtoken": []string{continuation}}
			if _, err := continuationToken(header); err == nil {
				t.Fatalf("malformed continuation %q was accepted", continuation)
			}
		})
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/iterations") {
			response.Header().Set("x-ms-continuationtoken", "same")
			_, _ = io.WriteString(response, `{"count":0,"value":[]}`)
			return
		}
		_, _ = response.Write(fixture(t, "pull_request_active.json"))
	}))
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, tokenFunc(func(context.Context) (string, error) { return "secret", nil }), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil || requests != 3 {
		t.Fatalf("repeated continuation result = requests %d, err %v", requests, err)
	}
}

func TestObserveClassifiesOnlyProvenAzureRateLimits(t *testing.T) {
	for _, test := range []struct {
		status     int
		retryAfter string
		want       bool
	}{
		{http.StatusTooManyRequests, "", false},
		{http.StatusTooManyRequests, "17", true},
		{http.StatusServiceUnavailable, "", false},
		{http.StatusServiceUnavailable, "19", true},
		{http.StatusServiceUnavailable, "not-valid", false},
		{http.StatusServiceUnavailable, "9223372036854775807", false},
		{http.StatusForbidden, "20", false},
	} {
		t.Run(fmt.Sprintf("%d-%s", test.status, test.retryAfter), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if test.retryAfter != "" {
					response.Header().Set("Retry-After", test.retryAfter)
				}
				response.WriteHeader(test.status)
			}))
			defer server.Close()
			observer, err := NewObserver(server.URL, testProject, testRepositoryID, tokenFunc(func(context.Context) (string, error) { return "secret", nil }), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = observer.Observe(context.Background(), testLocator(), "")
			limited, got := err.(*pullrequest.RateLimitError)
			if got != test.want {
				t.Fatalf("rate limited = %t, err = %v", got, err)
			}
			if got && limited.RetryAfter != time.Duration(mustInt(t, test.retryAfter))*time.Second {
				t.Fatalf("retry after = %s", limited.RetryAfter)
			}
		})
	}
}

func TestObserverRejectsRedirectsUnsafeOriginsAndCredentials(t *testing.T) {
	for _, raw := range []string{
		"",
		"dev.azure.com/acme",
		"ftp://dev.azure.com/acme",
		"https://user:secret@dev.azure.com/acme",
		"https://dev.azure.com/acme?token=secret",
		"https://dev.azure.com/acme#fragment",
	} {
		if _, err := NewObserver(raw, testProject, testRepositoryID, &rotatingToken{}, http.DefaultClient); err == nil {
			t.Fatalf("unsafe organization URL accepted: %q", raw)
		}
	}
	for _, identity := range []struct{ project, repository string }{
		{"../project", testRepositoryID},
		{"project/name", testRepositoryID},
		{testProject, "../repo"},
		{testProject, "repo/name"},
		{" project", testRepositoryID},
	} {
		if _, err := NewObserver("https://dev.azure.com/acme", identity.project, identity.repository, &rotatingToken{}, http.DefaultClient); err == nil {
			t.Fatalf("unsafe identity accepted: %#v", identity)
		}
	}

	targetRequests := 0
	targetAuthorization := ""
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		targetRequests++
		targetAuthorization = request.Header.Get("Authorization")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL+request.URL.Path, http.StatusFound)
	}))
	defer source.Close()
	observer, err := NewObserver(source.URL, testProject, testRepositoryID, tokenFunc(func(context.Context) (string, error) { return "redirect-secret", nil }), source.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil {
		t.Fatal("redirect was accepted")
	}
	if targetRequests != 0 || targetAuthorization != "" {
		t.Fatalf("redirect target got %d requests with %q", targetRequests, targetAuthorization)
	}
}

func TestObserverRedactsCredentialsAndBoundsTimeout(t *testing.T) {
	secret := "transport-secret"
	client := &http.Client{
		Timeout: 90 * time.Second,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport included %s and Bearer %s", secret, secret)
		}),
	}
	observer, err := NewObserver("https://dev.azure.com/acme", testProject, testRepositoryID, tokenFunc(func(context.Context) (string, error) { return secret, nil }), client)
	if err != nil {
		t.Fatal(err)
	}
	if observer.client.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s", observer.client.Timeout)
	}
	_, err = observer.Observe(context.Background(), testLocator(), "")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked credential: %v", err)
	}

	short := 3 * time.Second
	observer, err = NewObserver("https://dev.azure.com/acme", testProject, testRepositoryID, &rotatingToken{}, &http.Client{Timeout: short})
	if err != nil {
		t.Fatal(err)
	}
	if observer.client.Timeout != short {
		t.Fatalf("short timeout = %s", observer.client.Timeout)
	}
	for _, source := range []pullrequest.TokenSource{
		tokenFunc(func(context.Context) (string, error) { return "", nil }),
		tokenFunc(func(context.Context) (string, error) { return "", errors.New("credential detail") }),
	} {
		observer, err = NewObserver("https://dev.azure.com/acme", testProject, testRepositoryID, source, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("request made without a credential")
			return nil, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := observer.Observe(context.Background(), testLocator(), ""); err == nil || strings.Contains(err.Error(), "credential detail") {
			t.Fatalf("credential error = %v", err)
		}
	}
}

func TestObserveRejectsUnsafeLocatorAndOpaqueCursor(t *testing.T) {
	service := newFixtureService(t)
	server := httptest.NewServer(service)
	defer server.Close()
	observer, err := NewObserver(server.URL, testProject, testRepositoryID, &rotatingToken{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []pullrequest.Locator{
		{Provider: pullrequest.ProviderGitHub, Repository: "project/repo-id", ExternalID: "42"},
		{Provider: pullrequest.ProviderAzureDevOps, Repository: "other/repo-id", ExternalID: "42"},
		{Provider: pullrequest.ProviderAzureDevOps, Repository: "project/other", ExternalID: "42"},
		{Provider: pullrequest.ProviderAzureDevOps, Repository: "project/repo-id", ExternalID: "042"},
		{Provider: pullrequest.ProviderAzureDevOps, Repository: "project/repo-id", ExternalID: "2147483648"},
	} {
		if _, err := observer.Observe(context.Background(), locator, ""); err == nil {
			t.Fatalf("unsafe locator accepted: %#v", locator)
		}
	}
	for _, cursor := range []pullrequest.Cursor{"not-base64", "e30", pullrequest.Cursor(strings.Repeat("x", 4097))} {
		if _, err := observer.Observe(context.Background(), testLocator(), cursor); err == nil {
			t.Fatalf("malformed cursor accepted: %q", cursor)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testLocator() pullrequest.Locator {
	return pullrequest.Locator{Provider: pullrequest.ProviderAzureDevOps, Repository: testProject + "/" + testRepositoryID, ExternalID: testExternalID}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return normalizeNumbers(value).(map[string]any)
}

func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		number, _ := typed.Float64()
		return number
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeNumbers(item)
		}
	case []any:
		for index, item := range typed {
			typed[index] = normalizeNumbers(item)
		}
	}
	return value
}

func encodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func voteThread(id int, published, reviewer string, vote int) map[string]any {
	return map[string]any{
		"id":              float64(id),
		"publishedDate":   published,
		"lastUpdatedDate": published,
		"comments": []any{map[string]any{
			"id": float64(1), "parentCommentId": float64(0), "author": map[string]any{"id": "service-account"},
			"content": "vote update", "publishedDate": published, "lastUpdatedDate": published, "commentType": "system", "isDeleted": false,
		}},
		"properties": map[string]any{
			"CodeReviewThreadType":  map[string]any{"$type": "System.String", "$value": "VoteUpdate"},
			"CodeReviewVotedByTfId": map[string]any{"$type": "System.String", "$value": reviewer},
			"CodeReviewVoteResult":  map[string]any{"$type": "System.String", "$value": strconv.Itoa(vote)},
		},
		"isDeleted": false,
	}
}

func feedbackThread(id int, published string) map[string]any {
	return map[string]any{
		"id":              float64(id),
		"publishedDate":   published,
		"lastUpdatedDate": published,
		"comments": []any{map[string]any{
			"id": float64(1), "parentCommentId": float64(0), "author": map[string]any{"id": "reviewer-id"},
			"content": "later feedback", "publishedDate": published, "lastUpdatedDate": published, "commentType": "text", "isDeleted": false,
		}},
		"status": "active", "threadContext": nil, "properties": map[string]any{}, "isDeleted": false,
	}
}

func mutateVoteProperty(name string, value any) func(*fixtureService) {
	return func(service *fixtureService) {
		page := decodeObject(service.t, service.snapshotBody("/threads"))
		properties := page["value"].([]any)[1].(map[string]any)["properties"].(map[string]any)
		if value == nil {
			delete(properties, name)
		} else {
			properties[name] = value
		}
		service.setBody("/threads", encodeJSON(service.t, page))
	}
}

func mutatePull(path string, value any) func(*fixtureService) {
	return func(service *fixtureService) {
		body := decodeObject(service.t, service.snapshotBody(""))
		setPath(body, path, value)
		service.setBody("", encodeJSON(service.t, body))
	}
}

func mutateIteration(path string, value any) func(*fixtureService) {
	return func(service *fixtureService) {
		body := decodeObject(service.t, service.snapshotBody("/iterations"))
		latest := body["value"].([]any)[1].(map[string]any)
		setPath(latest, path, value)
		service.setBody("/iterations", encodeJSON(service.t, body))
	}
}

func setPath(object map[string]any, dotted string, value any) {
	parts := strings.Split(dotted, ".")
	for _, part := range parts[:len(parts)-1] {
		object = object[part].(map[string]any)
	}
	object[parts[len(parts)-1]] = value
}

func mustInt(t *testing.T, value string) int {
	t.Helper()
	number, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return number
}

func sha(character rune) string {
	return strings.Repeat(string(character), 40)
}
