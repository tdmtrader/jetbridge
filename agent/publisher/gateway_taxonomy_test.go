package publisher_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

const gatewayGitPolicy = `{
	"schema_version":1,
	"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
}`

// gatewayFault describes one injected failure for a single gateway path.
type gatewayFault struct {
	status     int           // non-zero: answer with this status instead of the protocol
	retryAfter string        // optional Retry-After header
	body       string        // raw body written with Content-Type application/json
	delay      time.Duration // block before answering, to drive the request deadline
	truncate   bool          // promise more bytes than are sent, then close the connection
}

// newGatewayFaultServer serves the happy-path gateway protocol and injects at
// most one fault per path. It is deliberately stateless: durable idempotency
// is the reference implementation's job (agent/publisher/contracttest).
//
// httptest.NewTLSServer negotiates HTTP/1.1 unless EnableHTTP2 is set, so the
// ResponseWriter implements http.Hijacker and the truncation fault works. Do
// not enable HTTP/2 here.
func newGatewayFaultServer(t *testing.T, faults map[string]gatewayFault) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		// Drain multipart uploads before answering so a fault response is
		// deterministic instead of racing the client's streaming body.
		if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
			_, _ = io.Copy(io.Discard, request.Body)
		}
		fault := faults[request.URL.Path]
		if fault.delay > 0 {
			// Drain the request body before blocking. net/http only starts the
			// background read that cancels request.Context() on a client
			// disconnect once the body hits EOF, so without this the handler
			// sleeps the entire delay after the client has already given up —
			// and httptest.Server.Close waits for it, making the deadline test
			// cost its full delay in wall-clock time.
			_, _ = io.Copy(io.Discard, request.Body)
			select {
			case <-request.Context().Done():
				return
			case <-time.After(fault.delay):
			}
		}
		if fault.truncate {
			hijacker, ok := response.(http.Hijacker)
			if !ok {
				t.Errorf("%s: response is not hijackable", request.URL.Path)
				return
			}
			connection, buffered, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("%s: hijack: %v", request.URL.Path, err)
				return
			}
			defer connection.Close()
			_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" +
				"Content-Length: 4096\r\n\r\n{\"found\":fal")
			_ = buffered.Flush()
			return
		}
		if fault.retryAfter != "" {
			response.Header().Set("Retry-After", fault.retryAfter)
		}
		if fault.status != 0 {
			body := fault.body
			if body == "" {
				body = `{"error":"injected"}`
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(fault.status)
			_, _ = response.Write([]byte(body))
			return
		}
		if fault.body != "" {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(fault.body))
			return
		}
		switch request.URL.Path {
		case "/v1/publications/lookup":
			writeGatewayJSON(t, response, map[string]any{"found": false})
		case "/v1/git/current-base":
			writeGatewayJSON(t, response, map[string]string{"base_sha": testBaseSHA})
		case "/v1/git/publish":
			writeGatewayJSON(t, response, map[string]string{
				"external_id": "pull-request/1", "url": "https://git.example/pull/1", "head_sha": testResultSHA,
			})
		case "/v1/work-items/publish":
			writeGatewayJSON(t, response, map[string]string{
				"external_id": "comment/1", "url": "https://work.example/ENG-42#comment-1",
			})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

// executeGatewayGitPublication runs one Git pull-request publication against
// server and returns the executor's answer, the durable row it left behind,
// and the execution error.
func executeGatewayGitPublication(
	t *testing.T,
	server *httptest.Server,
	mutate func(*publisher.GatewayConfig),
) (publisher.Publication, publisher.Publication, error) {
	t.Helper()
	fixture := newGatewaySnapshotFixture(t)
	config := gatewayTestConfig(t, server, gatewayGitPolicy)
	if mutate != nil {
		mutate(&config)
	}
	store := publisher.NewMemoryStore(time.Now)
	executor, err := publisher.NewGatewayExecutor(store, fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	publication, executeErr := executor.Execute(context.Background(), request)
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	durable, found, err := store.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("durable publication = (%+v, %t, %v)", durable, found, err)
	}
	return publication, durable, executeErr
}

func TestGatewayReturnsClassifiedErrorForRetryableStatus(t *testing.T) {
	server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusServiceUnavailable},
	})
	_, durable, err := executeGatewayGitPublication(t, server, nil)
	var gatewayErr *publisher.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %v, want a *publisher.GatewayError", err)
	}
	if gatewayErr.Status != http.StatusServiceUnavailable || !gatewayErr.Retryable ||
		gatewayErr.Path != "/v1/publications/lookup" {
		t.Fatalf("classification = %+v", gatewayErr)
	}
	if durable.Status != publisher.StatusPending {
		t.Fatalf("retryable failure must leave the publication pending, got %s", durable.Status)
	}
}

// TestGatewaySuppressedActionsCarryNoGatewayClassification proves the boundary
// of the taxonomy on the real Execute path. The action switch refuses BEFORE
// the gateway is contacted (agent/publisher/git.go), so its sentinel must reach
// the caller unclassified: Task 2 completes a terminal *GatewayError as
// StatusFailed, and a terminal row is never reclaimed by Acquire, so a
// suppressed attempt misread as terminal would make this operation key
// permanently unresumable.
func TestGatewaySuppressedActionsCarryNoGatewayClassification(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusForbidden},
	})
	_, durable, err := executeGatewayGitPublication(t, server, func(config *publisher.GatewayConfig) {
		config.ActionsMode = &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	})
	if !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("error = %v, want ErrActionsSuppressed", err)
	}
	var gatewayErr *publisher.GatewayError
	if errors.As(err, &gatewayErr) {
		t.Fatalf("a suppression refusal must never carry a gateway classification, got %+v", gatewayErr)
	}
	if !publisher.Retryable(err) {
		t.Fatal("a suppressed attempt must stay retryable so a later lease can resume it")
	}
	if durable.Status != publisher.StatusPending {
		t.Fatalf("suppressed publication = %s, want pending", durable.Status)
	}
	// The terminal 403 the fault server is holding must never be observed: the
	// switch refuses before any gateway request exists to classify.
	if got := calls(); len(got) != 0 {
		t.Fatalf("a suppressed publisher must make no gateway request, got %v", got)
	}
}

// newGatewayGitExecutorOnClock builds a Git-publisher executor over a memory
// store whose clock the test owns. Advancing that clock past the lease is
// exactly the condition that used to re-run a rejected publication — every
// lease expiry, forever — so it is what the terminal/retryable pair below has
// to reproduce.
func newGatewayGitExecutorOnClock(
	t *testing.T,
	server *httptest.Server,
	now *time.Time,
	mutate func(*publisher.GatewayConfig),
) (publisher.Executor, *publisher.MemoryStore, publisher.Request) {
	t.Helper()
	fixture := newGatewaySnapshotFixture(t)
	config := gatewayTestConfig(t, server, gatewayGitPolicy)
	if mutate != nil {
		mutate(&config)
	}
	store := publisher.NewMemoryStore(func() time.Time { return *now })
	executor, err := publisher.NewGatewayExecutor(store, fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	return executor, store, gatewayGitRequest(fixture.ref)
}

// gatewayDurableRow reads the durable publication behind request. The row —
// not the value Execute returned — is what a later lease reclaim consults, so
// it is the completion-side truth these tests assert on.
func gatewayDurableRow(
	t *testing.T,
	store *publisher.MemoryStore,
	request publisher.Request,
) publisher.Publication {
	t.Helper()
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	durable, found, err := store.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("durable publication = (%+v, %t, %v)", durable, found, err)
	}
	return durable
}

// TestGatewayTerminalStatusFailsPublicationInsteadOfRetryingForever pins the
// user-visible change: a 403 from the gateway now lands the publication in
// failed on the first attempt — the publish_snapshot step reports "publication
// ended with status failed" — instead of leaving the row pending to be
// re-attempted on every lease expiry, forever, each time re-reading the
// snapshot and re-hitting the gateway.
func TestGatewayTerminalStatusFailsPublicationInsteadOfRetryingForever(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusForbidden},
	})
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	executor, store, request := newGatewayGitExecutorOnClock(t, server, &now, nil)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("a terminal gateway status must complete the publication, not error: %v", err)
	}
	// The same shape the stale-base gate already returns: a durable terminal
	// answer handed back as (publication, nil), not an error the caller retries.
	if publication.Status != publisher.StatusFailed {
		t.Fatalf("publication = %+v, want status failed", publication)
	}
	if !strings.Contains(publication.Result.Detail, "403") ||
		!strings.Contains(publication.Result.Detail, "/v1/publications/lookup") {
		t.Fatalf("failure detail must name the endpoint and status, got %q", publication.Result.Detail)
	}
	durable := gatewayDurableRow(t, store, request)
	if durable.Status != publisher.StatusFailed || !durable.LeaseUntil.IsZero() {
		t.Fatalf("durable = %+v, want a failed row holding no lease to expire", durable)
	}

	// The old behavior — retry on every lease expiry, forever — must be gone.
	// The clock moves past the lease, which is precisely when the old code
	// reclaimed the row and re-hit the gateway; the new code makes no gateway
	// request at all and answers with the same terminal result.
	before := calls()
	now = now.Add(2 * time.Minute)
	replayed, err := executor.Execute(context.Background(), request)
	if err != nil || replayed.Status != publisher.StatusFailed {
		t.Fatalf("replay = (%+v, %v)", replayed, err)
	}
	if replayed.Result != publication.Result || replayed.Attempt != publication.Attempt {
		t.Fatalf("replay = (%+v, attempt %d), want the identical terminal result of (%+v, attempt %d)",
			replayed.Result, replayed.Attempt, publication.Result, publication.Attempt)
	}
	if after := calls(); !slices.Equal(before, after) {
		t.Fatalf("terminal publication was retried: calls before=%v after=%v", before, after)
	}
}

// TestGatewayRetryableStatusStaysPendingAndReclaimable is the other half of the
// pair. A 503 is unchanged by this task: the row stays pending holding a lease,
// and the attempt after that lease expires reaches the gateway again.
func TestGatewayRetryableStatusStaysPendingAndReclaimable(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusServiceUnavailable},
	})
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	executor, store, request := newGatewayGitExecutorOnClock(t, server, &now, nil)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("a retryable gateway failure must surface as an error")
	}
	durable := gatewayDurableRow(t, store, request)
	if durable.Status != publisher.StatusPending || durable.LeaseUntil.IsZero() {
		t.Fatalf("durable = %+v, want a pending row holding a lease", durable)
	}

	now = now.Add(2 * time.Minute)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("the reclaimed attempt must surface the repeated retryable failure")
	}
	if want := []string{"/v1/publications/lookup", "/v1/publications/lookup"}; !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v: a retryable failure must still reach the backend again", calls(), want)
	}
	reclaimed := gatewayDurableRow(t, store, request)
	if reclaimed.Status != publisher.StatusPending || reclaimed.Attempt != durable.Attempt+1 {
		t.Fatalf("reclaimed = %+v, want a second pending attempt after attempt %d", reclaimed, durable.Attempt)
	}
}

// TestGatewayTerminalStatusOnPublishFailsAfterTheEarlierCallsSucceeded covers
// the ambiguous case: the rejection lands on the write endpoint, after lookup
// and the base check already answered. The publication still ends failed, and
// the detail names the endpoint so the effect can be reconciled by hand.
func TestGatewayTerminalStatusOnPublishFailsAfterTheEarlierCallsSucceeded(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/git/publish": {status: http.StatusUnprocessableEntity},
	})
	publication, durable, err := executeGatewayGitPublication(t, server, nil)
	if err != nil || publication.Status != publisher.StatusFailed || durable.Status != publisher.StatusFailed {
		t.Fatalf("Execute = (%+v, %v), durable=%+v", publication, err, durable)
	}
	if want := []string{"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish"}; !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v", calls(), want)
	}
	if !strings.Contains(durable.Result.Detail, "/v1/git/publish") {
		t.Fatalf("detail = %q, want the publish endpoint named for manual reconciliation", durable.Result.Detail)
	}
}

// TestGatewayTerminalStatusOnBaseReadFailsPublication covers the third routed
// Git call. Each of the three has its own error branch, so each needs its own
// proof: a branch left unrouted — or routed with the wrong operation string or
// attempt — is invisible from the other two.
func TestGatewayTerminalStatusOnBaseReadFailsPublication(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/git/current-base": {status: http.StatusConflict},
	})
	publication, durable, err := executeGatewayGitPublication(t, server, nil)
	if err != nil || publication.Status != publisher.StatusFailed || durable.Status != publisher.StatusFailed {
		t.Fatalf("Execute = (%+v, %v), durable=%+v", publication, err, durable)
	}
	if !strings.Contains(durable.Result.Detail, "read destination base") ||
		!strings.Contains(durable.Result.Detail, "/v1/git/current-base") ||
		!strings.Contains(durable.Result.Detail, "409") {
		t.Fatalf("detail = %q, want the base read and its status named", durable.Result.Detail)
	}
	if want := []string{"/v1/publications/lookup", "/v1/git/current-base"}; !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v: nothing may be published after a terminal base read", calls(), want)
	}
}

// TestGatewayWorkItemTerminalStatusFailsPublication proves the same routing
// exists in the work-item service, which has its own copy of the external-call
// sequence — both of its calls, for the same reason as above.
func TestGatewayWorkItemTerminalStatusFailsPublication(t *testing.T) {
	for name, expectation := range map[string]struct {
		path      string
		status    int
		operation string
	}{
		"lookup": {
			path: "/v1/publications/lookup", status: http.StatusBadRequest,
			operation: "reconcile work-item publication",
		},
		"publish": {
			path: "/v1/work-items/publish", status: http.StatusConflict,
			operation: "publish work-item update",
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
				expectation.path: {status: expectation.status},
			})
			fixture := newGatewaySnapshotFixture(t)
			config := gatewayTestConfig(t, server, `{
				"schema_version":1,
				"rules":[{"team":"engineering","publisher":"work-item-publisher/v1","modes":["comment"],"approval_policy_versions":["engineering/v1"],"destination_prefixes":["ENG-"]}]
			}`)
			store := publisher.NewMemoryStore(time.Now)
			executor, err := publisher.NewGatewayExecutor(store, fixture.metadata, fixture.content, config)
			if err != nil {
				t.Fatal(err)
			}
			request := publisher.Request{
				Publisher: publisher.WorkItemPublisher, Input: fixture.ref,
				Destination: "ENG-42", Mode: publisher.ModeComment,
				Parameters: map[string]string{"body": "agent result"}, ApprovalPolicyVersion: "engineering/v1",
				Authority: publisher.Authority{TeamID: 9, TeamName: "engineering", BuildID: 12, WorkflowRunID: 17, Actor: "build/12"},
			}
			publication, err := executor.Execute(context.Background(), request)
			if err != nil || publication.Status != publisher.StatusFailed {
				t.Fatalf("work-item terminal failure = (%+v, %v)", publication, err)
			}
			durable := gatewayDurableRow(t, store, request)
			if durable.Status != publisher.StatusFailed ||
				!strings.Contains(durable.Result.Detail, expectation.operation) ||
				!strings.Contains(durable.Result.Detail, expectation.path) {
				t.Fatalf("durable = %+v, want a terminal completion naming %q", durable, expectation.operation)
			}
		})
	}
}

// TestGatewaySuppressedPublishIsNeverCompletedAsFailed is the completion-side
// half of the suppression guard. Task 1's
// TestGatewaySuppressedActionsCarryNoGatewayClassification proves the refusal
// carries no gateway classification; this proves the durable consequence, on
// the one code path that can now write StatusFailed.
//
// The fault server is primed with the terminal 403 that WOULD fail this
// publication, so nothing but the ordering inside Retryable keeps the
// suppressed attempt out of Complete(StatusFailed). That completion is
// permanent — Acquire never reclaims a terminal row — so a suppressed attempt
// misrouted onto it would poison this operation key forever, defeating the
// entire point of refusing before any provider call: the identical semantic
// operation must execute exactly once after an admin resumes actions.
func TestGatewaySuppressedPublishIsNeverCompletedAsFailed(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusForbidden},
	})
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	actions := &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	executor, store, request := newGatewayGitExecutorOnClock(t, server, &now, func(config *publisher.GatewayConfig) {
		config.ActionsMode = actions
	})
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("error = %v, want ErrActionsSuppressed", err)
	}
	suppressed := gatewayDurableRow(t, store, request)
	if suppressed.Status != publisher.StatusPending || suppressed.Result.Status != "" {
		t.Fatalf("suppressed row = %+v, want pending with no recorded result — never failed", suppressed)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("a suppressed publisher must make no gateway request, got %v", got)
	}

	// The operation key is not poisoned: once an admin resumes actions and the
	// lease expires, the identical semantic operation still executes. (That
	// resumed attempt is the one allowed to observe the primed 403 and end
	// terminally — which is also what makes the assertion above load-bearing.)
	actions.mode = publisher.ActionsModeActive
	now = now.Add(2 * time.Minute)
	resumed, err := executor.Execute(context.Background(), request)
	if err != nil || resumed.Status != publisher.StatusFailed {
		t.Fatalf("resumed = (%+v, %v), want the reclaimed attempt to run", resumed, err)
	}
	if want := []string{"/v1/publications/lookup"}; !slices.Equal(calls(), want) {
		t.Fatalf("calls = %v, want %v: only the resumed attempt may reach the gateway", calls(), want)
	}
}

// TestGatewayClassifiesAdversarialResponses is the suite the coverage audit
// found missing: before it, every HTTP response the publisher tests ever
// observed was a 200. Each row asserts BOTH halves of one answer — the
// classification the client draws (*publisher.GatewayError, terminal or
// retryable) and the durable consequence that classification has (a pending row
// a later lease reclaims, or a failed row that is never re-attempted).
func TestGatewayClassifiesAdversarialResponses(t *testing.T) {
	for name, testCase := range map[string]struct {
		fault      gatewayFault
		mutate     func(*publisher.GatewayConfig)
		wantStatus int
		wantClass  publisher.Status // pending (retryable) or failed (terminal)
	}{
		"throttled":                  {fault: gatewayFault{status: 429, retryAfter: "30"}, wantStatus: 429, wantClass: publisher.StatusPending},
		"request timeout":            {fault: gatewayFault{status: 408}, wantStatus: 408, wantClass: publisher.StatusPending},
		"internal server error":      {fault: gatewayFault{status: 500}, wantStatus: 500, wantClass: publisher.StatusPending},
		"bad gateway":                {fault: gatewayFault{status: 502}, wantStatus: 502, wantClass: publisher.StatusPending},
		"service unavailable":        {fault: gatewayFault{status: 503}, wantStatus: 503, wantClass: publisher.StatusPending},
		"unlisted status stays open": {fault: gatewayFault{status: 418}, wantStatus: 418, wantClass: publisher.StatusPending},
		"bad request":                {fault: gatewayFault{status: 400}, wantStatus: 400, wantClass: publisher.StatusFailed},
		"unauthorized":               {fault: gatewayFault{status: 401}, wantStatus: 401, wantClass: publisher.StatusFailed},
		"forbidden":                  {fault: gatewayFault{status: 403}, wantStatus: 403, wantClass: publisher.StatusFailed},
		"not found":                  {fault: gatewayFault{status: 404}, wantStatus: 404, wantClass: publisher.StatusFailed},
		"conflict":                   {fault: gatewayFault{status: 409}, wantStatus: 409, wantClass: publisher.StatusFailed},
		"unprocessable":              {fault: gatewayFault{status: 422}, wantStatus: 422, wantClass: publisher.StatusFailed},
		// Regression guard for the classify-before-size ordering: a rejected
		// request may answer with a huge provider error page, and that must
		// not be reported as a size-bound failure.
		"oversized error page": {
			fault:      gatewayFault{status: 503, body: `{"error":"` + strings.Repeat("x", 4096) + `"}`},
			mutate:     func(config *publisher.GatewayConfig) { config.MaxResponseBytes = 128 },
			wantStatus: 503, wantClass: publisher.StatusPending,
		},
		// A truncated 200 is an unknown answer, never a terminal one.
		"truncated response body": {fault: gatewayFault{truncate: true}, wantStatus: 200, wantClass: publisher.StatusPending},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := newGatewayFaultServer(t, map[string]gatewayFault{"/v1/publications/lookup": testCase.fault})
			publication, durable, err := executeGatewayGitPublication(t, server, testCase.mutate)
			if durable.Status != testCase.wantClass {
				t.Fatalf("durable status = %s, want %s (publication=%+v, err=%v)",
					durable.Status, testCase.wantClass, publication, err)
			}
			if testCase.wantClass == publisher.StatusFailed {
				if err != nil {
					t.Fatalf("a terminal classification must complete the operation, not error: %v", err)
				}
				// A terminal *GatewayError never reaches the caller — it is
				// converted into this failed completion — so the row it wrote
				// is the only place the classified status is still observable.
				if !strings.Contains(durable.Result.Detail, strconv.Itoa(testCase.wantStatus)) ||
					!strings.Contains(durable.Result.Detail, "/v1/publications/lookup") {
					t.Fatalf("failure detail = %q, want status %d and the endpoint named for manual reconciliation",
						durable.Result.Detail, testCase.wantStatus)
				}
				return
			}
			var gatewayErr *publisher.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("error = %v, want a *publisher.GatewayError", err)
			}
			if gatewayErr.Status != testCase.wantStatus || !gatewayErr.Retryable {
				t.Fatalf("classification = %+v, want status %d retryable", gatewayErr, testCase.wantStatus)
			}
			// Retry-After is deliberately unread: retries happen at lease
			// expiry, not in an in-process loop, so there is no backoff to
			// honor. Assert we never claim otherwise.
			if strings.Contains(strings.ToLower(err.Error()), "retry-after") {
				t.Fatalf("error claims to honor Retry-After: %v", err)
			}
		})
	}
}

// TestGatewayClassifiesTransportFailureAsRetryable covers the arm with no
// status at all: the request never reached a server, so Status is 0 and the
// underlying transport error must survive for operators to read.
func TestGatewayClassifiesTransportFailureAsRetryable(t *testing.T) {
	live, _ := newGatewayFaultServer(t, nil) // only for its certificate
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, durable, err := executeGatewayGitPublication(t, live, func(config *publisher.GatewayConfig) {
		config.Endpoint = "https://" + address
	})
	var gatewayErr *publisher.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %v, want a *publisher.GatewayError", err)
	}
	if gatewayErr.Status != 0 || !gatewayErr.Retryable || gatewayErr.Unwrap() == nil {
		t.Fatalf("transport classification = %+v", gatewayErr)
	}
	if durable.Status != publisher.StatusPending {
		t.Fatalf("durable status = %s, want pending", durable.Status)
	}
}

// TestGatewaySlowResponseHitsTheRequestDeadlineAndStaysRetryable pins the
// timeout-shaped arm. The transport also carries a ResponseHeaderTimeout equal
// to the request timeout, but the per-request context deadline always wins
// (both are armed from RequestTimeout, and the context is armed first, before
// the credential resolve and the snapshot re-read), so the deadline is the
// behavior that is actually observable — and a deadline is retryable, never
// terminal.
func TestGatewaySlowResponseHitsTheRequestDeadlineAndStaysRetryable(t *testing.T) {
	server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {delay: 5 * time.Second},
	})
	_, durable, err := executeGatewayGitPublication(t, server, func(config *publisher.GatewayConfig) {
		// The deadline is armed before the credential resolve and the
		// snapshot re-capture (the context.WithTimeout in GitService.Execute),
		// so leave real margin for those: half a second is ~10x the capture
		// of this two-file fixture.
		config.RequestTimeout = 500 * time.Millisecond
		config.LeaseDuration = 5 * time.Second
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow gateway error = %v, want a context deadline", err)
	}
	if durable.Status != publisher.StatusPending {
		t.Fatalf("durable status = %s, want pending", durable.Status)
	}
}

// TestGatewayRejectsMalformedResponseBodies covers the malformed-body cases the
// old TestGatewayRejectsOversizedAndMalformedResponses never reached: with
// MaxResponseBytes at its default the size guard no longer fires first, so
// decodeGatewayJSON, DisallowUnknownFields and requireJSONEOF are actually
// exercised. Each row asserts the decoder's own words reach the caller — that
// is the proof the body was parsed rather than rejected by its length — and
// that a body we cannot understand leaves the operation reclaimable.
func TestGatewayRejectsMalformedResponseBodies(t *testing.T) {
	for name, testCase := range map[string]struct {
		body     string
		wantText string
	}{
		"unknown field":     {body: `{"found":false,"padding":"x"}`, wantText: `unknown field "padding"`},
		"trailing value":    {body: `{"found":false}{"found":true}`, wantText: "multiple JSON values"},
		"truncated object":  {body: `{"found":`, wantText: "unexpected EOF"},
		"wrong shape":       {body: `["found"]`, wantText: "cannot unmarshal"},
		"result without id": {body: `{"found":true,"result":{"url":"https://git.example/pull/7"}}`, wantText: "external_id is invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
				"/v1/publications/lookup": {body: testCase.body},
			})
			_, durable, err := executeGatewayGitPublication(t, server, nil)
			if err == nil || !strings.Contains(err.Error(), testCase.wantText) {
				t.Fatalf("malformed response error = %v, want it to contain %q", err, testCase.wantText)
			}
			if durable.Status != publisher.StatusPending {
				t.Fatalf("a malformed response must stay retryable, got %s", durable.Status)
			}
		})
	}
}
