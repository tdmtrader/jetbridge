# WS4 — Publisher Gateway: Adversarial Reality and Error Taxonomy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the publisher a real adversary. Today every HTTP response the publisher tests observe is `200`, `gatewayClient.do` collapses every non-200 into one flat string, and a permanently-rejected publication retries forever on every lease expiry. This plan adds a typed retry taxonomy that ends those loops, an adversarial suite that drives the client through throttling, server faults, truncated bodies, dead sockets and malformed JSON, a durable-idempotency reference gateway that the crash and stale-lookup tests bend, a work-item result gate matching the Git one, and a reusable conformance kit that runs against both the in-repo reference and — behind `//go:build live` — a real deployed gateway.

**Architecture:** `agent/publisher` keeps its shape. `gateway.go` gains an exported `GatewayError{Status, Path, Retryable, Err}` returned by `gatewayClient.do`; `git.go` and `workitem.go` route a *terminal* classification into `Store.Complete(…, Result{Status: StatusFailed, …})` and leave every other error on today's pending-and-reclaim path. A new non-test package `agent/publisher/contracttest` holds (a) `ReferenceServer`, a strict, durably-idempotent in-repo implementation of the four `/v1` endpoints with injectable fault modes, and (b) `Run(t, Config)`, the conformance kit that any gateway must pass. The durable store (`atc/db/agent_publications_factory.go`) already accepts the `failed` terminal status end to end — no migration and no factory change; this plan only proves it.

**Tech Stack:** Go standard `testing` (the `agent/` convention), `net/http/httptest` with TLS so the real pinned-TLS transport is exercised, Ginkgo/Gomega for the one `atc/db` spec, no new third-party dependencies.

## Global Constraints

- No new third-party test dependencies. `httptest`, `testing`, `errors`, `net` only.
- `agent/schema` must never import the main module; nothing here touches it.
- Production behavior changes in this plan are exactly two: **Task 2** (terminal gateway failures complete a publication as `failed`) and **Task 8** (work-item results must carry a usable external identity). Everything else is tests, a test-support package, and docs.
- Only a **terminal `*GatewayError`** may complete an operation as `failed`. Every other error — transport failures, decode failures, size-bound failures, credential failures, context deadlines — keeps today's behavior: return the error, leave the durable row `pending`, let a later lease reclaim retry.
- Never widen the terminal set by default. An unlisted status stays retryable, which is exactly the behavior every non-200 had before this plan.
- No migration is added. Do **not** touch `jetbridgeHeadMigration`.
- Do not weaken `newGatewayHTTPClient` (`agent/publisher/gateway.go:350-390`): the mounted CA bundle, `MinVersion: tls.VersionTLS12`, and the redirect ban are the deployment's only TLS pinning and must remain unreachable from configuration. See "Decision 1" below.
- Keep every existing test in `agent/publisher` green at every commit. The only existing test this plan edits is the misnamed `TestGatewayRejectsOversizedAndMalformedResponses`, which Task 5 splits into its two honest halves.

## Scouting facts (verified against `410d9b59f8`)

| Fact | Location |
|---|---|
| `GatewayConfig` has no `Transport` field; TLS/transport built in one place | `agent/publisher/gateway.go:44-53`, `:350-390` |
| `do` collapses every non-200 into a flat string, no `%w`, no status type | `agent/publisher/gateway.go:668-670` |
| Size guard runs *before* the status check, so an oversized error page masks the status | `agent/publisher/gateway.go:665-670` |
| `Idempotency-Key` is set from the operation key on lookup/publish | `agent/publisher/gateway.go:650-655` |
| `/v1/git/current-base` deliberately passes `""` as the operation key ⇒ **no** `Idempotency-Key` header (the "current-base exemption") | `agent/publisher/gateway.go:473` |
| Strict response decoding: `DisallowUnknownFields` + `requireJSONEOF` | `agent/publisher/gateway.go:689-710` |
| Four `/v1` endpoints: `publications/lookup`, `git/current-base`, `git/publish`, `work-items/publish` | `agent/publisher/gateway.go:440, 473, 522, 563` |
| Git Lookup-before-Publish, with the `HeadSHA` cross-check on the recovered result | `agent/publisher/git.go:180-191` |
| Git `CurrentBase` stale-base gate, completes `stale_base`/`rebase_required` **without** returning an error | `agent/publisher/git.go:193-207` |
| `preserveExternalError` drops the wrapped error when the external context expired | `agent/publisher/git.go:229-234` |
| Work-item reconcile trusts the recovered result verbatim — no cross-check at all | `agent/publisher/workitem.go:125-139` |
| `WorkItemResult` carries only `ExternalID` and `URL`; `GitResult` adds `HeadSHA` | `agent/publisher/workitem.go:22-25`, `git.go:88-92` |
| `StatusFailed` exists and is terminal in the shared status vocabulary | `agent/publisher/store.go:24-39` |
| `Result.Validate` requires a non-empty `Detail` for every non-succeeded status | `agent/publisher/store.go:65-67` |
| Durable store: `status` CHECK already allows `'failed'`; `Complete` writes any terminal status | `atc/db/migration/migrations/1773106110_create_agent_publications.up.sql:29-30`, `atc/db/agent_publications_factory.go:338-352` |
| Durable store: a terminal row is never reclaimed — `Acquire` returns `execute=false` | `atc/db/agent_publications_factory.go:172-180` |
| `linkSucceededPublicationOutcome` no-ops for any non-`succeeded` status | `atc/db/agent_publications_factory.go:410-413` |
| Eight inline `httptest` fakes, all answering `200` | `agent/publisher/gateway_test.go:42, 159, 198, 271, 314, 339, 361, 391` |
| `TestGatewayRejectsOversizedAndMalformedResponses` never reaches the decoder (size guard fires first) | `agent/publisher/gateway_test.go:337-357` |
| Crash-3 coverage that Task 7 extends to crash-4 | `agent/publisher/gateway_test.go:156-186`, `git_test.go:256-281` |
| Conformance-kit shape to mirror | `agent/devmcp/contracttest/kit.go:19-90` |
| `//go:build live` + env-gating pattern to copy | `atc/worker/jetbridge/live_test.go:1-25`, `agent/devmcp/e2e/e2e_test.go:182-190` |
| Documented gateway contract: durable `(publisher, operation_key)` → result | `docs/agentic/README.md:494-519` |
| Shared publisher test helpers already available | `store_test.go:140` (`branchRequest`), `types_test.go:130-152` (`publicationAuthority`, `changeRef`, `digest`, `approvalEvidence`), `gateway_test.go:508-644` (`newGatewaySnapshotFixture`, `gatewayGitRequest`, `gatewayTestConfig`, `tarBytes`, `writeGatewayJSON`) |

## Decisions taken before Task 1

**Decision 1 — no `GatewayConfig.Transport`; a fault-mode server instead.**
The WS4 design sketches an optional `Transport http.RoundTripper` on `GatewayConfig`. Scouting rejects it:

1. `newGatewayHTTPClient` (`gateway.go:350-390`) is the *only* place the mounted CA bundle becomes `RootCAs` and `MinVersion: tls.VersionTLS12` is applied. A `RoundTripper` supplied through the same public struct replaces all of it, and `ValidateGatewayConfig` (`gateway.go:57-60`) — the deployment's startup fail-closed check — cannot validate a func value. A security-relevant struct whose doc comment reads "contains only non-secret process configuration" would gain a field that silently voids the pinning.
2. Everything the adversarial suite needs is reachable *without* it, and reaching it through `httptest.NewTLSServer` is a strictly better test because it exercises the real pinned-TLS transport rather than bypassing it: statuses, `Retry-After`, malformed/unknown-field/trailing JSON, and truncated bodies all come from a handler (truncation via `http.Hijacker`); a real transport error comes from pointing `Endpoint` at a closed TCP port; deadline behavior comes from a blocking handler.
3. The one fault a `RoundTripper` would add — a bare `ResponseHeaderTimeout` firing *before* the per-call context deadline — cannot occur in production: `ResponseHeaderTimeout` is set to `config.RequestTimeout` (`gateway.go:376`) while the per-call deadline is *also* `RequestTimeout` but starts earlier (`git.go:152`, before credential resolution and the full snapshot re-capture). The context always wins. A test for it would assert a state the process cannot enter. (That the two bounds are the same value is a real finding; changing it is a production change WS4 does not authorize, so it is recorded here and left alone.)

The seam this plan adds instead is `contracttest.ReferenceServer` plus a test-local `newGatewayFaultServer`: injectable, safe, and reusable by the conformance kit.

**Decision 2 — a terminal failure completes the operation and returns no error.**
This mirrors the existing stale-base path (`git.go:204-207`), which completes `stale_base`/`rebase_required` and returns `(publication, nil)`. `PublishSnapshotStep` then reports `publish_snapshot: publication ended with status failed` (`atc/exec/publish_snapshot_step.go:157-158`) — an operator-actionable message — instead of the generic `publication execution failed` it prints for a returned error (`:146`). The gateway status is preserved in `Result.Detail`, which is persisted in the durable `result` JSONB.
Known tradeoff, to be stated in a code comment: a terminal status on `/v1/git/publish` (notably `409`) is *ambiguous* — the external write may have landed. Recording `failed` is safe (no further attempt runs, so no double-publish) but may under-report a landed effect. `Detail` therefore names the endpoint and the status so an operator can reconcile by hand. Widening the protocol so the gateway can disambiguate is out of scope for WS4.

**Decision 3 — the work-item cross-check is a result-identity gate, not a content cross-check.**
Git can cross-check because `GitResult.HeadSHA` has independently derived truth (`change.ResultSHA`, re-read from the snapshot). `WorkItemResult` carries only `ExternalID` and `URL`, and the wire response (`gatewayResult`, `gateway.go:411-415`) echoes nothing request-derived; requiring the destination to appear in an external ID or URL would couple us to one provider's formatting. The enforceable analogue is therefore: **a recovered or freshly published work-item result must be a usable external identity** — non-empty bounded `ExternalID`, and a URL that, when present, is `https` with a host and no userinfo — enforced at the *service* boundary (so it holds for every `WorkItemBackend`, not just the in-repo gateway one, which happens to enforce the same shape at `gateway.go:712-735`), with a mismatch producing a retryable refusal. Task 8 documents in code why no content cross-check exists.

---

### Task 1: Classify gateway responses with a typed error

**Files:**
- Modify: `agent/publisher/gateway.go`
- Create: `agent/publisher/gateway_error_internal_test.go`
- Create: `agent/publisher/gateway_taxonomy_test.go`

**User-visible consequence (part 1 of 2):** none yet. This task only makes the classification *available*; Task 2 acts on it. Landing them separately keeps the taxonomy reviewable on its own and keeps every existing test green.

- [ ] Create `agent/publisher/gateway_error_internal_test.go` (package `publisher`, an internal test in the style of `agent/snapshot/contracts/schema_parity_internal_test.go`) with the full status table and the error-shape assertions:

```go
package publisher

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestGatewayStatusRetryableSplitsTerminalFromRetryable(t *testing.T) {
	terminal := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
	}
	for _, status := range terminal {
		if gatewayStatusRetryable(status) {
			t.Errorf("status %d must be terminal: repeating identical request bytes cannot change the answer", status)
		}
	}
	retryable := []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		// Deliberately unlisted statuses stay retryable: that is the behavior
		// every non-200 had before this taxonomy existed, and widening the
		// terminal set by accident would silently fail live publications.
		http.StatusTeapot, http.StatusMethodNotAllowed, http.StatusCreated,
	}
	for _, status := range retryable {
		if !gatewayStatusRetryable(status) {
			t.Errorf("status %d must stay retryable", status)
		}
	}
}

func TestGatewayErrorReportsClassAndUnwrapsTransportFailures(t *testing.T) {
	terminal := &GatewayError{Status: http.StatusForbidden, Path: "/v1/git/publish"}
	if got := terminal.Error(); got != "publisher gateway: /v1/git/publish responded with status 403 (terminal)" {
		t.Fatalf("terminal error text = %q", got)
	}
	cause := fmt.Errorf("dial tcp 127.0.0.1:1: connect: connection refused")
	transport := &GatewayError{Path: "/v1/publications/lookup", Retryable: true, Err: cause}
	if !errors.Is(transport, cause) {
		t.Fatal("transport GatewayError must unwrap to its cause")
	}
	var found *GatewayError
	if !errors.As(fmt.Errorf("publisher: reconcile: %w", transport), &found) || !found.Retryable {
		t.Fatal("a wrapped GatewayError must remain discoverable through errors.As")
	}
}
```

- [ ] Create `agent/publisher/gateway_taxonomy_test.go` (package `publisher_test`) with the shared fault server and one end-to-end classification assertion. This file is the home for Tasks 1, 2 and 5's tests:

```go
package publisher_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
```

- [ ] Run `go test ./agent/publisher/ -run 'TestGateway(StatusRetryable|ErrorReports|ReturnsClassified)' -count=1` and confirm the compile failure:

```
# github.com/concourse/concourse/agent/publisher
agent/publisher/gateway_error_internal_test.go:14:6: undefined: gatewayStatusRetryable
agent/publisher/gateway_error_internal_test.go:28:14: undefined: GatewayError
FAIL	github.com/concourse/concourse/agent/publisher [build failed]
```

- [ ] In `agent/publisher/gateway.go`, immediately after `var ErrDestinationNotAllowed = …` (`:39`), add the type and the classifier:

```go
// GatewayError classifies a gateway response so the publisher services can
// tell "this can never work" from "try again after the lease expires".
// Status is the HTTP status code, or 0 when the request never produced one
// (transport failure, TLS failure). Retryable errors keep the durable
// publication pending for a later lease reclaim; a terminal error completes
// it as failed.
type GatewayError struct {
	Status    int
	Path      string
	Retryable bool
	Err       error
}

func (gatewayErr *GatewayError) Error() string {
	class := "terminal"
	if gatewayErr.Retryable {
		class = "retryable"
	}
	if gatewayErr.Status == 0 {
		return fmt.Sprintf("publisher gateway: %s request failed (%s): %v", gatewayErr.Path, class, gatewayErr.Err)
	}
	return fmt.Sprintf("publisher gateway: %s responded with status %d (%s)", gatewayErr.Path, gatewayErr.Status, class)
}

func (gatewayErr *GatewayError) Unwrap() error { return gatewayErr.Err }

// gatewayStatusRetryable is the deployment's retry taxonomy. The terminal set
// is exactly the statuses whose answer cannot change while the request bytes
// stay the same: malformed request, revoked or missing authorization, unknown
// route or operation, conflicting state, and unprocessable content. Every
// other status — 408, 429, all 5xx, and anything this list does not name —
// stays retryable, which is the behavior every non-200 had before the
// taxonomy existed. Never widen the terminal set by default: a wrong terminal
// classification permanently fails a publication that would have succeeded.
func gatewayStatusRetryable(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return false
	default:
		return true
	}
}
```

- [ ] In `gatewayClient.do` (`gateway.go:656-670`), replace the transport-error wrap and move the status check ahead of the body read. Exact replacement for those lines:

```go
	response, err := client.http.Do(request)
	if err != nil {
		return nil, &GatewayError{Path: path, Retryable: true, Err: err}
	}
	defer response.Body.Close()
	// Classify before touching the body. The status is the authoritative
	// answer, and a rejected request may reply with an arbitrarily large
	// provider error page that must not mask a terminal classification as a
	// size-bound failure.
	if response.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			Status: response.StatusCode, Path: path,
			Retryable: gatewayStatusRetryable(response.StatusCode),
		}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		// A truncated 200 is an unknown answer, never a terminal one.
		return nil, &GatewayError{Status: response.StatusCode, Path: path, Retryable: true, Err: err}
	}
	if int64(len(responseBody)) > client.maxResponseBytes {
		return nil, fmt.Errorf("publisher gateway: response exceeds %d bytes", client.maxResponseBytes)
	}
```

- [ ] Confirm the `mime.ParseMediaType` block that followed the old status check (`gateway.go:671-674`) is still present and now immediately follows the size check.
- [ ] Run `go test ./agent/publisher/ -count=1` and confirm every test passes, including the pre-existing `TestGatewayRejectsOversizedAndMalformedResponses` (its handler answers `200`, so the reorder does not change it):

```
ok  	github.com/concourse/concourse/agent/publisher	1.4s
```

- [ ] Run `gofmt -l agent/publisher` and confirm no output.
- [ ] Commit `feat(publisher): classify gateway responses as terminal or retryable`.

### Task 2: End the retry-forever loop for terminal gateway failures

**Files:**
- Modify: `agent/publisher/git.go`
- Modify: `agent/publisher/workitem.go`
- Modify: `agent/publisher/gateway_taxonomy_test.go`

**User-visible consequence:** a `403` (or `400`/`401`/`404`/`409`/`422`) from the gateway now lands the publication in `failed` on the first attempt, and the `publish_snapshot` step reports `publish_snapshot: publication ended with status failed`. Before this task the same response left the row `pending` and the operation was re-attempted on **every** lease expiry, forever, each time re-reading the snapshot and re-hitting the gateway. Retryable failures (`503`, `429`, transport) are unchanged: still `pending`, still reclaimed.

- [ ] Add the failing tests to `agent/publisher/gateway_taxonomy_test.go`:

```go
func TestGatewayTerminalStatusFailsPublicationInsteadOfRetryingForever(t *testing.T) {
	server, calls := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusForbidden},
	})
	fixture := newGatewaySnapshotFixture(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	executor, err := publisher.NewGatewayExecutor(
		store, fixture.metadata, fixture.content, gatewayTestConfig(t, server, gatewayGitPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("a terminal gateway status must complete the publication, not error: %v", err)
	}
	if publication.Status != publisher.StatusFailed {
		t.Fatalf("publication = %+v, want status failed", publication)
	}
	if !strings.Contains(publication.Result.Detail, "403") ||
		!strings.Contains(publication.Result.Detail, "/v1/publications/lookup") {
		t.Fatalf("failure detail must name the endpoint and status, got %q", publication.Result.Detail)
	}

	// The old behavior — retry on every lease expiry, forever — must be gone:
	// a later attempt makes no gateway request at all.
	before := calls()
	now = now.Add(2 * time.Minute)
	replayed, err := executor.Execute(context.Background(), request)
	if err != nil || replayed.Status != publisher.StatusFailed {
		t.Fatalf("replay = (%+v, %v)", replayed, err)
	}
	if after := calls(); !slices.Equal(before, after) {
		t.Fatalf("terminal publication was retried: calls before=%v after=%v", before, after)
	}
}

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

func TestGatewayRetryableStatusStaysPendingAndReclaimable(t *testing.T) {
	server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusServiceUnavailable},
	})
	_, durable, err := executeGatewayGitPublication(t, server, nil)
	if err == nil {
		t.Fatal("a retryable gateway failure must surface as an error")
	}
	if durable.Status != publisher.StatusPending || durable.LeaseUntil.IsZero() {
		t.Fatalf("durable = %+v, want a pending row holding a lease", durable)
	}
}

func TestGatewayWorkItemTerminalStatusFailsPublication(t *testing.T) {
	server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {status: http.StatusBadRequest},
	})
	fixture := newGatewaySnapshotFixture(t)
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"work-item-publisher/v1","modes":["comment"],"approval_policy_versions":["engineering/v1"],"destination_prefixes":["ENG-"]}]
	}`)
	executor, err := publisher.NewGatewayExecutor(
		publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config,
	)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := executor.Execute(context.Background(), publisher.Request{
		Publisher: publisher.WorkItemPublisher, Input: fixture.ref,
		Destination: "ENG-42", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "agent result"}, ApprovalPolicyVersion: "engineering/v1",
		Authority: publisher.Authority{TeamID: 9, TeamName: "engineering", BuildID: 12, WorkflowRunID: 17, Actor: "build/12"},
	})
	if err != nil || publication.Status != publisher.StatusFailed {
		t.Fatalf("work-item terminal failure = (%+v, %v)", publication, err)
	}
}
```

- [ ] Run `go test ./agent/publisher/ -run 'TestGateway(TerminalStatus|RetryableStatus|WorkItemTerminal)' -count=1` and confirm the failure:

```
--- FAIL: TestGatewayTerminalStatusFailsPublicationInsteadOfRetryingForever (0.03s)
    gateway_taxonomy_test.go:XX: a terminal gateway status must complete the publication, not error: publisher: reconcile repository publication: publisher gateway: /v1/publications/lookup responded with status 403 (terminal)
FAIL
```

- [ ] In `agent/publisher/git.go`, add the shared routing helper next to `preserveExternalError` (`:229`):

```go
// terminalGatewayFailure converts a classified terminal gateway response into
// a durable failed completion, which is what ends the retry-forever loop: a
// terminal operation is never reclaimed on lease expiry. It reports
// handled=false for every other error — retryable gateway errors, transport
// errors, decode errors, context deadlines — which keeps the caller's
// existing pending-and-reclaim behavior.
//
// A terminal status on a write endpoint is ambiguous: the external effect may
// have landed before the gateway rejected the request. Recording `failed` is
// still safe (no later attempt runs, so nothing can double-publish), so the
// detail names the endpoint and status for manual reconciliation instead of
// guessing.
func terminalGatewayFailure(
	ctx context.Context,
	store Store,
	operationKey string,
	attempt int,
	operation string,
	err error,
) (Publication, bool, error) {
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Retryable {
		return Publication{}, false, nil
	}
	publication, completeErr := store.Complete(ctx, operationKey, attempt, Result{
		Status: StatusFailed,
		Detail: fmt.Sprintf(
			"%s: gateway rejected %s with status %d; retrying the identical request cannot succeed",
			operation, gatewayErr.Path, gatewayErr.Status,
		),
	})
	if completeErr != nil {
		return Publication{}, true, completeErr
	}
	return publication, true, nil
}
```

- [ ] Route all three `GitService.Execute` external calls through it. Each of the three error branches gains the same four lines *before* the existing `preserveExternalError` return. For `Lookup` (`git.go:180-183`):

```go
	prior, found, err := service.backend.Lookup(externalContext, credential, publication.OperationKey)
	if err != nil {
		if failed, handled, completeErr := terminalGatewayFailure(
			ctx, service.store, publication.OperationKey, publication.Attempt,
			"reconcile repository publication", err,
		); handled {
			return failed, completeErr
		}
		return Publication{}, preserveExternalError(externalContext, "reconcile repository publication", err)
	}
```

Apply the identical shape to `CurrentBase` (`git.go:193-196`, operation string `"read destination base"`) and `Publish` (`git.go:217-219`, operation string `"publish repository change"`). Use `ctx`, not `externalContext`, for the completion — the durable write must not inherit the external call's deadline, matching every other `service.store.Complete` call in the file.

- [ ] Route both `WorkItemService.Execute` external calls the same way in `agent/publisher/workitem.go`: `Lookup` (`:125-128`, operation string `"reconcile work-item publication"`) and `Publish` (`:140-142`, operation string `"publish work-item update"`). Add `"errors"` to the import block only if the compiler asks — the helper lives in `git.go`, so `workitem.go` needs no new imports.
- [ ] Run `go test ./agent/publisher/ -count=1` and confirm all tests pass.
- [ ] Run `go build ./... && gofmt -l agent/publisher` and confirm both are silent.
- [ ] Commit `feat(publisher): fail publications terminally rejected by the gateway`.

### Task 3: Prove the failed terminal status on the durable store

**Files:**
- Modify: `atc/db/agent_publications_factory_test.go`
- Modify: `agent/publisher/store_test.go`

The taxonomy must behave identically on both `Store` implementations. Scouting confirms the durable store already supports it — the `status` CHECK allows `'failed'` (`1773106110_create_agent_publications.up.sql:29-30`), `Complete` writes any terminal status, `Acquire` refuses to reclaim a terminal row, and `linkSucceededPublicationOutcome` no-ops for non-`succeeded`. **No migration and no factory change.** This task adds the tests that pin it. They run under the `db-tests` CI job introduced by `01-ci-execution.md`; until that lands they run locally against PostgreSQL.

- [ ] Add to `agent/publisher/store_test.go` (memory store):

```go
func TestMemoryStoreCompletesTerminalFailureAndRefusesReclaim(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	request := branchRequest()
	publication, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || !execute {
		t.Fatalf("Acquire = (%+v, %t, %v)", publication, execute, err)
	}
	result := publisher.Result{
		Status: publisher.StatusFailed,
		Detail: "publish repository change: gateway rejected /v1/git/publish with status 403; retrying the identical request cannot succeed",
	}
	failed, err := store.Complete(context.Background(), publication.OperationKey, publication.Attempt, result)
	if err != nil || failed.Status != publisher.StatusFailed || failed.Result != result || !failed.LeaseUntil.IsZero() {
		t.Fatalf("Complete = (%+v, %v)", failed, err)
	}
	now = now.Add(time.Hour)
	reacquired, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || execute {
		t.Fatalf("a failed publication must never be reclaimed: (%+v, %t, %v)", reacquired, execute, err)
	}
	if reacquired.Status != publisher.StatusFailed {
		t.Fatalf("reacquired status = %s", reacquired.Status)
	}
}
```

- [ ] Add the durable twin as a new `It` inside the existing `Describe("AgentPublicationsFactory", …)` in `atc/db/agent_publications_factory_test.go`, using the file's existing `request()` closure and `workflowRunID`/`input` variables:

```go
	It("completes a terminally rejected publication as failed, publishes no outcome, and never reclaims it", func() {
		acquired, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())

		result := publisher.Result{
			Status: publisher.StatusFailed,
			Detail: "publish repository change: gateway rejected /v1/git/publish with status 403; retrying the identical request cannot succeed",
		}
		failed, err := factory.Complete(context.Background(), acquired.OperationKey, acquired.Attempt, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(failed.Status).To(Equal(publisher.StatusFailed))
		Expect(failed.Result).To(Equal(result))
		Expect(failed.LeaseUntil).To(BeZero())

		// Expiring the lease cannot resurrect a terminal operation: this is
		// what ends the retry-forever loop for a gateway rejection.
		_, err = dbConn.Exec(`UPDATE agent_publications SET lease_until = now() - interval '1 second' WHERE id = $1`, failed.ID)
		Expect(err).NotTo(HaveOccurred())
		reacquired, execute, err := db.NewAgentPublicationsFactory(dbConn).Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(reacquired.Status).To(Equal(publisher.StatusFailed))
		Expect(reacquired.Attempt).To(Equal(failed.Attempt))

		var outcomes int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(&outcomes)).To(Succeed())
		Expect(outcomes).To(Equal(0), "a failed publication must not claim a published workflow outcome")

		replayed, err := db.NewAgentPublicationsFactory(dbConn).Complete(
			context.Background(), acquired.OperationKey, acquired.Attempt, result,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(Equal(failed))
	})
```

- [ ] Run `go test ./agent/publisher/ -run TestMemoryStoreCompletesTerminalFailure -count=1` and confirm it passes (the memory store already supports this; the test is the regression pin).
- [ ] Run `pg_isready` and then `ginkgo --focus='AgentPublicationsFactory' ./atc/db/` and confirm the new spec passes:

```
Ran N of NNNN Specs in ...
SUCCESS! -- N Passed | 0 Failed | 0 Pending | NNNN Skipped
```

- [ ] Commit `test(publisher): pin the failed terminal status on both stores`.

### Task 4: Add the in-repo reference gateway with fault modes

**Files:**
- Create: `agent/publisher/contracttest/reference.go`
- Create: `agent/publisher/contracttest/reference_test.go`

The reference server is the honest implementation of the documented protocol (`docs/agentic/README.md:494-519`): bearer auth, `Idempotency-Key` on lookup and both publish endpoints, no key required on `current-base`, and a **durable** `(publisher, operation_key) → result` map so a repeated publish under the same key produces no second external effect. Fault modes let Tasks 6 and 7 bend exactly one rule at a time; the conformance kit (Task 9) uses the unfaulted server as its in-repo target.

- [ ] Create `agent/publisher/contracttest/reference.go`:

```go
// Package contracttest holds the publisher gateway conformance kit and the
// in-repo reference implementation of the gateway protocol documented in
// docs/agentic/README.md. It is a normal (non-test) package so both
// agent/publisher's tests and out-of-repo gateway implementations can import
// it.
package contracttest

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	gitPublisher      = "git-publisher/v1"
	workItemPublisher = "work-item-publisher/v1"

	maxReferenceOperationBytes = 1 << 20
)

// Fault injects exactly one protocol deviation. The zero Fault is a fully
// conforming gateway.
type Fault struct {
	// BlindLookup answers found:false forever, even after a publish landed.
	// This is the eventual-consistency adversary: a gateway whose read side
	// lags its write side.
	BlindLookup bool
	// FailAfterFirstPublish records the first publish durably and then answers
	// with this status. It reproduces "the write landed, the caller never
	// learned the answer".
	FailAfterFirstPublish int
}

// ReferenceOptions configures the reference gateway.
type ReferenceOptions struct {
	Token       string // required bearer token; defaults to "mounted-token"
	CurrentBase string // /v1/git/current-base answer; must be a full lowercase object ID
	Fault       Fault
}

type referenceResult struct {
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
}

// ReferenceServer is a strict, durably-idempotent gateway over TLS.
type ReferenceServer struct {
	Server *httptest.Server

	mu              sync.Mutex
	options         ReferenceOptions
	paths           []string
	keys            map[string][]string
	results         map[string]referenceResult
	publishRequests int
	effects         int
	failedOnce      bool
}

// NewReferenceServer starts the reference gateway and closes it on cleanup.
func NewReferenceServer(t *testing.T, options ReferenceOptions) *ReferenceServer {
	t.Helper()
	if options.Token == "" {
		options.Token = "mounted-token"
	}
	if options.CurrentBase == "" {
		options.CurrentBase = strings.Repeat("1", 40)
	}
	reference := &ReferenceServer{
		options: options,
		keys:    map[string][]string{},
		results: map[string]referenceResult{},
	}
	reference.Server = httptest.NewTLSServer(http.HandlerFunc(reference.serve))
	t.Cleanup(reference.Server.Close)
	return reference
}

// SetFault replaces the injected fault. It is safe to call between requests.
func (reference *ReferenceServer) SetFault(fault Fault) {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	reference.options.Fault = fault
}

// Paths returns the ordered gateway request log.
func (reference *ReferenceServer) Paths() []string {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return slices.Clone(reference.paths)
}

// IdempotencyKeys returns the ordered Idempotency-Key values seen at path.
func (reference *ReferenceServer) IdempotencyKeys(path string) []string {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return slices.Clone(reference.keys[path])
}

// PublishRequests counts publish requests received, deduplicated or not.
func (reference *ReferenceServer) PublishRequests() int {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return reference.publishRequests
}

// ExternalEffects counts distinct operation keys that produced a result —
// the number of side effects a real provider would have performed.
func (reference *ReferenceServer) ExternalEffects() int {
	reference.mu.Lock()
	defer reference.mu.Unlock()
	return reference.effects
}

func (reference *ReferenceServer) serve(response http.ResponseWriter, request *http.Request) {
	reference.mu.Lock()
	reference.paths = append(reference.paths, request.URL.Path)
	if key := request.Header.Get("Idempotency-Key"); key != "" {
		reference.keys[request.URL.Path] = append(reference.keys[request.URL.Path], key)
	}
	token := reference.options.Token
	reference.mu.Unlock()

	if request.Method != http.MethodPost {
		writeReferenceError(response, http.StatusMethodNotAllowed, "every gateway endpoint is POST")
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+token {
		writeReferenceError(response, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	switch request.URL.Path {
	case "/v1/publications/lookup":
		reference.lookup(response, request)
	case "/v1/git/current-base":
		reference.currentBase(response, request)
	case "/v1/git/publish":
		reference.publish(response, request, gitPublisher)
	case "/v1/work-items/publish":
		reference.publish(response, request, workItemPublisher)
	default:
		writeReferenceError(response, http.StatusNotFound, "unknown gateway endpoint")
	}
}

func (reference *ReferenceServer) lookup(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Publisher    string `json:"publisher"`
		OperationKey string `json:"operation_key"`
	}
	if !decodeReferenceJSON(response, request, &body) {
		return
	}
	if body.OperationKey == "" || request.Header.Get("Idempotency-Key") != body.OperationKey {
		writeReferenceError(response, http.StatusBadRequest, "Idempotency-Key must equal operation_key")
		return
	}
	reference.mu.Lock()
	defer reference.mu.Unlock()
	if reference.options.Fault.BlindLookup {
		writeReferenceJSON(response, map[string]any{"found": false})
		return
	}
	result, found := reference.results[body.Publisher+"\x00"+body.OperationKey]
	if !found {
		writeReferenceJSON(response, map[string]any{"found": false})
		return
	}
	writeReferenceJSON(response, map[string]any{"found": true, "result": result})
}

func (reference *ReferenceServer) currentBase(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Destination  string `json:"destination"`
		TargetBranch string `json:"target_branch"`
	}
	if !decodeReferenceJSON(response, request, &body) {
		return
	}
	if body.Destination == "" || body.TargetBranch == "" {
		writeReferenceError(response, http.StatusBadRequest, "destination and target_branch are required")
		return
	}
	// The current-base exemption: this endpoint identifies no operation, so
	// the client sends no Idempotency-Key and the gateway must not demand one.
	reference.mu.Lock()
	base := reference.options.CurrentBase
	reference.mu.Unlock()
	writeReferenceJSON(response, map[string]string{"base_sha": base})
}

func (reference *ReferenceServer) publish(response http.ResponseWriter, request *http.Request, publisherType string) {
	reader, err := request.MultipartReader()
	if err != nil {
		writeReferenceError(response, http.StatusBadRequest, "publish must be multipart/form-data")
		return
	}
	operationPart, err := reader.NextPart()
	if err != nil || operationPart.FormName() != "operation" {
		writeReferenceError(response, http.StatusBadRequest, "the first part must be named operation")
		return
	}
	var operation struct {
		OperationKey string `json:"operation_key"`
		Destination  string `json:"destination"`
		ResultSHA    string `json:"result_sha"`
	}
	if err := json.NewDecoder(io.LimitReader(operationPart, maxReferenceOperationBytes)).Decode(&operation); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "the operation part must be JSON")
		return
	}
	if operation.OperationKey == "" || request.Header.Get("Idempotency-Key") != operation.OperationKey {
		writeReferenceError(response, http.StatusBadRequest, "Idempotency-Key must equal operation_key")
		return
	}
	contentPart, err := reader.NextPart()
	if err != nil || contentPart.FormName() != "snapshot" {
		writeReferenceError(response, http.StatusBadRequest, "the second part must be named snapshot")
		return
	}
	if _, err := io.Copy(io.Discard, contentPart); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "the snapshot part is unreadable")
		return
	}

	reference.mu.Lock()
	defer reference.mu.Unlock()
	reference.publishRequests++
	key := publisherType + "\x00" + operation.OperationKey
	result, found := reference.results[key]
	if !found {
		reference.effects++
		result = referenceResult{
			ExternalID: fmt.Sprintf("publication/%d", reference.effects),
			URL:        fmt.Sprintf("https://gateway.example/publications/%d", reference.effects),
		}
		if publisherType == gitPublisher {
			result.HeadSHA = operation.ResultSHA
		}
		// Durability first: the result is recorded before the response is
		// written, so a caller that never sees the response can still recover
		// it through lookup. That ordering is the whole contract.
		reference.results[key] = result
	}
	if status := reference.options.Fault.FailAfterFirstPublish; status != 0 && !reference.failedOnce {
		reference.failedOnce = true
		writeReferenceError(response, status, "injected failure after the write landed")
		return
	}
	writeReferenceJSON(response, result)
}

func decodeReferenceJSON(response http.ResponseWriter, request *http.Request, value any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxReferenceOperationBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeReferenceError(response, http.StatusBadRequest, "request body is not the documented JSON object")
		return false
	}
	return true
}

func writeReferenceJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func writeReferenceError(response http.ResponseWriter, status int, detail string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": detail})
}

// ReferenceCAPEM is the reference gateway's certificate in the PEM form
// publisher.GatewayConfig.CACertificateFile expects.
func ReferenceCAPEM(reference *ReferenceServer) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: reference.Server.Certificate().Raw,
	})
}
```
- [ ] Create `agent/publisher/contracttest/reference_test.go` (package `contracttest_test`) proving the reference's own contract before anything depends on it:

```go
package contracttest_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func referenceClient(t *testing.T, reference *contracttest.ReferenceServer) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(reference.Server.Certificate())
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func referencePost(t *testing.T, reference *contracttest.ReferenceServer, path, token, key, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, reference.Server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := referenceClient(t, reference).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestReferenceServerEnforcesAuthKeysAndTheCurrentBaseExemption(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{})
	key := "sha256:" + strings.Repeat("a", 64)
	lookupBody := `{"publisher":"git-publisher/v1","operation_key":"` + key + `"}`

	if got := referencePost(t, reference, "/v1/publications/lookup", "", key, lookupBody).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated lookup = %d, want 401", got)
	}
	if got := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", "", lookupBody).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("lookup without Idempotency-Key = %d, want 400", got)
	}
	response := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", key, lookupBody)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d, want 200", response.StatusCode)
	}
	var found struct {
		Found bool `json:"found"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil || found.Found {
		t.Fatalf("unknown key must answer found:false (%+v, %v)", found, err)
	}
	// The current-base exemption is a rule, not an option: the client never
	// sends a key here (agent/publisher/gateway.go:473).
	base := referencePost(t, reference, "/v1/git/current-base", "mounted-token", "",
		`{"destination":"git.example/acme/widget","target_branch":"main"}`)
	if base.StatusCode != http.StatusOK {
		t.Fatalf("current-base without Idempotency-Key = %d, want 200", base.StatusCode)
	}
	if got := referencePost(t, reference, "/v1/nope", "mounted-token", key, `{}`).StatusCode; got != http.StatusNotFound {
		t.Fatalf("unknown endpoint = %d, want 404", got)
	}
	if got := referencePost(t, reference, "/v1/publications/lookup", "mounted-token", key,
		`{"publisher":"git-publisher/v1","operation_key":"`+key+`","surprise":1}`).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("unknown request field = %d, want 400", got)
	}
}

func TestReferenceServerDeduplicatesPublishByOperationKey(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{})
	key := "sha256:" + strings.Repeat("b", 64)
	for attempt := 0; attempt < 2; attempt++ {
		body, contentType := multipartPublish(t, key, strings.Repeat("2", 40))
		request, err := http.NewRequest(http.MethodPost, reference.Server.URL+"/v1/git/publish", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Authorization", "Bearer mounted-token")
		request.Header.Set("Idempotency-Key", key)
		response, err := referenceClient(t, reference).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ExternalID string `json:"external_id"`
			HeadSHA    string `json:"head_sha"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if result.ExternalID != "publication/1" || result.HeadSHA != strings.Repeat("2", 40) {
			t.Fatalf("attempt %d result = %+v", attempt, result)
		}
	}
	if reference.PublishRequests() != 2 || reference.ExternalEffects() != 1 {
		t.Fatalf("requests/effects = %d/%d, want 2/1", reference.PublishRequests(), reference.ExternalEffects())
	}
}
```

- [ ] Add the `multipartPublish` helper to the same file — it builds the two-part body the client sends (`gateway.go:577-621`), so the reference's parsing is tested against the real shape:

```go
func multipartPublish(t *testing.T, operationKey, resultSHA string) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	operationHeader := make(textproto.MIMEHeader)
	operationHeader.Set("Content-Disposition", `form-data; name="operation"`)
	operationHeader.Set("Content-Type", "application/json")
	operationPart, err := writer.CreatePart(operationHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationPart.Write([]byte(`{"operation_key":"` + operationKey + `","destination":"git.example/acme/widget","result_sha":"` + resultSHA + `"}`)); err != nil {
		t.Fatal(err)
	}
	contentHeader := make(textproto.MIMEHeader)
	contentHeader.Set("Content-Disposition", `form-data; name="snapshot"; filename="snapshot.tar"`)
	contentHeader.Set("Content-Type", "application/x-tar")
	contentPart, err := writer.CreatePart(contentHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contentPart.Write([]byte("snapshot bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}
```

- [ ] Run `go test ./agent/publisher/contracttest/ -count=1` and confirm the first failure is the missing package:

```
go: warning: "./agent/publisher/contracttest/" matched no packages
no packages to test
```

then, after the files exist, that both tests pass:

```
ok  	github.com/concourse/concourse/agent/publisher/contracttest	0.6s
```

- [ ] Add `"mime/multipart"` and `"net/textproto"` to the test file imports.
- [ ] Run `gofmt -l agent/publisher/contracttest` and confirm no output.
- [ ] Commit `test(publisher): add a durably idempotent reference gateway`.

### Task 5: Drive the client through an adversarial response table

**Files:**
- Modify: `agent/publisher/gateway_taxonomy_test.go`
- Modify: `agent/publisher/gateway_test.go`

This is the suite the audit found missing: before it, every HTTP response the publisher tests observed was `200`. It also splits `TestGatewayRejectsOversizedAndMalformedResponses` (`gateway_test.go:337`) into its two honest halves — the existing test never reached the JSON decoder because the size guard fired first, so `decodeGatewayJSON`, `DisallowUnknownFields` and `requireJSONEOF` were uncovered.

- [ ] Add the classification table to `agent/publisher/gateway_taxonomy_test.go`:

```go
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

func TestGatewaySlowResponseHitsTheRequestDeadlineAndStaysRetryable(t *testing.T) {
	server, _ := newGatewayFaultServer(t, map[string]gatewayFault{
		"/v1/publications/lookup": {delay: 5 * time.Second},
	})
	_, durable, err := executeGatewayGitPublication(t, server, func(config *publisher.GatewayConfig) {
		// The deadline starts before the snapshot re-capture (git.go:152),
		// so leave real margin for it: half a second is ~10x the capture of
		// this two-file fixture.
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
```

- [ ] Add `"net"` to the test file's imports.
- [ ] Split the misnamed test. In `agent/publisher/gateway_test.go`, rename `TestGatewayRejectsOversizedAndMalformedResponses` (`:337`) to `TestGatewayRejectsResponsesLargerThanTheConfiguredBound`, leaving its body unchanged (it is an honest size-bound test: `MaxResponseBytes = 128`, a 1 KiB `200` body, expecting `response exceeds`).
- [ ] Add the second, previously-absent half to `agent/publisher/gateway_taxonomy_test.go`:

```go
// The malformed-body cases the old TestGatewayRejectsOversizedAndMalformedResponses
// never reached: with MaxResponseBytes at its default the size guard no longer
// fires first, so decodeGatewayJSON, DisallowUnknownFields and requireJSONEOF
// are actually exercised.
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
```

- [ ] Run `go test ./agent/publisher/ -run 'TestGateway(Classifies|SlowResponse|RejectsMalformed|RejectsResponsesLarger)' -count=1 -v` and confirm every sub-test passes. If `truncated response body` reports a `*GatewayError` with `Status: 0` instead of `200`, the client failed before headers arrived — re-check that the hijacked reply writes the status line and a `Content-Length` larger than the bytes sent.
- [ ] Run `go test ./agent/publisher/ -count=1` and confirm the whole package is green.
- [ ] Commit `test(publisher): drive the gateway client through adversarial responses`.

### Task 6: Pin idempotency-key stability across a stale lookup

**Files:**
- Create: `agent/publisher/gateway_idempotency_test.go`

The eventual-consistency arm both existing fakes cannot express: the publish landed, but lookup keeps answering `found:false`. The client cannot detect this from inside, so the enforceable in-repo property is that the retried publish carries the **byte-identical** `Idempotency-Key`, which is what lets a contract-honoring gateway deduplicate. This test makes that regression-proof and documents that the dedupe — not our code — is what prevents the second pull request.

- [ ] Create `agent/publisher/gateway_idempotency_test.go` (package `publisher_test`):

```go
package publisher_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func TestGatewayRetriesCarryBytewiseIdenticalIdempotencyKeyWhenLookupLagsThePublish(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{
		CurrentBase: testBaseSHA,
		Fault: contracttest.Fault{
			// The write side has landed the publish; the read side has not
			// caught up, and the first attempt never learns its own answer.
			BlindLookup:           true,
			FailAfterFirstPublish: http.StatusServiceUnavailable,
		},
	})
	fixture := newGatewaySnapshotFixture(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	executor, err := publisher.NewGatewayExecutor(
		store, fixture.metadata, fixture.content,
		gatewayTestConfig(t, reference.Server, gatewayGitPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("a 503 after the write landed must surface as a retryable error")
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("first attempt = (%+v, %t, %v), want a pending row", pending, found, err)
	}

	now = now.Add(2 * time.Minute)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Attempt != 2 {
		t.Fatalf("reclaimed attempt = (%+v, %v)", publication, err)
	}

	keys := reference.IdempotencyKeys("/v1/git/publish")
	if len(keys) != 2 {
		t.Fatalf("publish idempotency keys = %v, want two attempts", keys)
	}
	if keys[0] != keys[1] || keys[0] != key {
		t.Fatalf("retried publish changed its idempotency key: %q then %q (operation key %q)", keys[0], keys[1], key)
	}
	if got := reference.PublishRequests(); got != 2 {
		t.Fatalf("publish requests = %d, want 2 — the blind lookup cannot prevent the second write", got)
	}
	// Exactly one external effect, and it came from the gateway's durable
	// (publisher, operation_key) map, not from anything ATC could see.
	if got := reference.ExternalEffects(); got != 1 {
		t.Fatalf("external effects = %d, want 1", got)
	}
	if want := []string{
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
	}; !slices.Equal(reference.Paths(), want) {
		t.Fatalf("gateway calls = %v, want %v", reference.Paths(), want)
	}
}
```

- [ ] Run `go test ./agent/publisher/ -run TestGatewayRetriesCarryBytewise -count=1` and confirm it passes. If the second attempt returns `stale_base`, the reference `CurrentBase` option was not set to `testBaseSHA` — the fixture's `repository-change/v1` base is `testBaseSHA` (`gateway_test.go:31`).
- [ ] Commit `test(publisher): pin idempotency-key stability across a stale lookup`.

### Task 7: Cover crash point 4 and pin the Lookup-first ordering

**Files:**
- Modify: `agent/publisher/gateway_idempotency_test.go`

Crash point 4 — the provider call succeeded, the durable completion failed — was structurally untestable: no `publisher.Store` fake existed, and nothing pinned the Lookup-before-write ordering. Reordering `git.go` so `Publish` runs before `Lookup` keeps every current test green while making crash-4 double-publish. This test fails loudly if that ordering ever moves.

- [ ] Add the store wrapper and the test to `agent/publisher/gateway_idempotency_test.go`:

```go
// failingStore delegates to a real store but fails the first Complete, which
// is crash point 4: the external write landed and the durable completion did
// not. Everything else must behave exactly like the wrapped store.
type failingStore struct {
	publisher.Store
	failures int
}

func (store *failingStore) Complete(
	ctx context.Context,
	operationKey string,
	attempt int,
	result publisher.Result,
) (publisher.Publication, error) {
	if store.failures > 0 {
		store.failures--
		return publisher.Publication{}, errors.New("durable completion failed")
	}
	return store.Store.Complete(ctx, operationKey, attempt, result)
}

func TestGatewayRecoversFromFailedCompletionWithoutRepeatingTheWrite(t *testing.T) {
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{CurrentBase: testBaseSHA})
	fixture := newGatewaySnapshotFixture(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	memory := publisher.NewMemoryStore(func() time.Time { return now })
	store := &failingStore{Store: memory, failures: 1}
	executor, err := publisher.NewGatewayExecutor(
		store, fixture.metadata, fixture.content,
		gatewayTestConfig(t, reference.Server, gatewayGitPolicy),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	if _, err := executor.Execute(context.Background(), request); err == nil {
		t.Fatal("a failed durable completion must not be reported as success")
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	pending, found, err := memory.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("after crash point 4 = (%+v, %t, %v), want a pending row", pending, found, err)
	}

	now = now.Add(2 * time.Minute)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Result.ExternalID != "publication/1" {
		t.Fatalf("recovery = (%+v, %v)", publication, err)
	}

	if got := reference.PublishRequests(); got != 1 {
		t.Fatalf("publish requests across both attempts = %d, want exactly 1", got)
	}
	if got := reference.ExternalEffects(); got != 1 {
		t.Fatalf("external effects = %d, want exactly 1", got)
	}
	// Pin the ordering, not just the count. If Lookup is moved after the
	// write, the retry becomes current-base → publish → lookup and this
	// sequence assertion fails before the counts do.
	if want := []string{
		"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish",
		"/v1/publications/lookup",
	}; !slices.Equal(reference.Paths(), want) {
		t.Fatalf("gateway calls = %v, want %v — Lookup must precede every write on a retry",
			reference.Paths(), want)
	}
}
```

- [ ] Add `"errors"` to the file's imports.
- [ ] Run `go test ./agent/publisher/ -run TestGatewayRecoversFromFailedCompletion -count=1` and confirm it passes.
- [ ] Prove the ordering assertion has teeth: temporarily move the `Lookup` block (`git.go:180-191`) below the `Publish` call, re-run the test, and confirm it fails with the call-sequence message. Revert the edit immediately — this is a manual mutation check, not a committed change.
- [ ] Run `go test ./agent/publisher/... -count=1` and confirm the package and `contracttest` are green.
- [ ] Commit `test(publisher): cover crash point 4 and pin lookup-before-write`.

### Task 8: Require work-item results to carry a usable external identity

**Files:**
- Modify: `agent/publisher/workitem.go`
- Modify: `agent/publisher/workitem_test.go`

**User-visible consequence:** a backend that answers `found:true` with an empty or malformed result — no `external_id`, or a non-HTTPS `url` — no longer completes the publication as `succeeded`. Before this task, `workitem.go:129-133` copied the recovered result verbatim into a terminal `succeeded` row, which also wrote an `agent_workflow_outcomes` row marked `published` pointing at nothing. Now it is a retryable refusal: the operation stays `pending`, the step fails with a named error, and a later attempt can succeed. The Git path has had the equivalent gate since it shipped (`git.go:185-187`, `:220-222`).

- [ ] Add the failing tests to `agent/publisher/workitem_test.go`:

```go
func TestWorkItemServiceRefusesRecoveredResultWithoutExternalIdentity(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{found: true, lookup: publisher.WorkItemResult{}}
	service, err := publisher.NewWorkItemService(
		store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}},
		validSnapshotValueInspector(), backend, time.Minute, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := commentRequest()
	if _, err := service.Execute(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "external identity") {
		t.Fatalf("empty recovered result = %v, want a named refusal", err)
	}
	key, _ := request.OperationKey()
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("refusal must stay retryable: (%+v, %t, %v)", pending, found, err)
	}

	// The refusal is retryable, not terminal: once the backend answers with a
	// real identity the same operation completes.
	now = now.Add(2 * time.Minute)
	backend.lookup = publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://work.example/9"}
	completed, err := service.Execute(context.Background(), request)
	if err != nil || completed.Status != publisher.StatusSucceeded || completed.Result.ExternalID != "comment-9" {
		t.Fatalf("retry after a usable identity = (%+v, %v)", completed, err)
	}
}

func TestWorkItemServiceRefusesResultsWithUnusableIdentities(t *testing.T) {
	for name, result := range map[string]publisher.WorkItemResult{
		"empty external id":     {ExternalID: ""},
		"blank external id":     {ExternalID: "   "},
		"untrimmed external id": {ExternalID: "comment-9 "},
		"control character":     {ExternalID: "comment\x009"},
		"plaintext url":         {ExternalID: "comment-9", URL: "http://work.example/9"},
		"url with credentials":  {ExternalID: "comment-9", URL: "https://user:pass@work.example/9"},
		"url without a host":    {ExternalID: "comment-9", URL: "https:///9"},
		// net/url rejects DEL and every byte below 0x20 outright.
		"unparseable url": {ExternalID: "comment-9", URL: "https://work.example/\x7f"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, recovered := range []bool{false, true} {
				store := publisher.NewMemoryStore(time.Now)
				backend := &workItemBackendStub{found: recovered, lookup: result, result: result}
				service, err := publisher.NewWorkItemService(
					store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}},
					validSnapshotValueInspector(), backend, time.Minute, time.Minute,
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.Execute(context.Background(), commentRequest()); err == nil ||
					!strings.Contains(err.Error(), "external identity") {
					t.Fatalf("recovered=%t result %+v = %v, want a named refusal", recovered, result, err)
				}
			}
		})
	}
}

func commentRequest() publisher.Request {
	return publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       snapshot.SnapshotRef{ID: 8, Type: "review/v1", Digest: digest("b")},
		Destination: "WORK-9", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "ready"}, ApprovalPolicyVersion: "comment/v1",
		Authority: publicationAuthority(),
	}
}
```

- [ ] Add `"strings"` to `workitem_test.go`'s imports.
- [ ] Run `go test ./agent/publisher/ -run TestWorkItemServiceRefuses -count=1` and confirm the failure:

```
--- FAIL: TestWorkItemServiceRefusesRecoveredResultWithoutExternalIdentity (0.00s)
    workitem_test.go:XX: empty recovered result = <nil>, want a named refusal
FAIL
```

- [ ] In `agent/publisher/workitem.go`, add the gate below the `WorkItemResult` declaration (`:22-25`):

```go
// Validate requires a work-item result to be a usable external identity.
//
// This is the work-item analogue of the Git service's head_sha cross-check
// (git.go:185-187): it is what a service may trust before recording a
// terminal success. It is deliberately weaker, because it can be. A Git
// result carries independently derived truth — the result commit of the exact
// snapshot being published — so a recovered result can be *cross-checked*
// against content. A work-item result carries only an external ID and a URL;
// the wire response echoes nothing request-derived (see gatewayResult in
// gateway.go), and demanding that a provider's external ID or URL embed the
// destination would couple this package to one provider's formatting. So the
// enforceable invariant is identity usability, applied to both a recovered
// result and a fresh one. A violation is a retryable refusal: the operation
// stays pending, because a backend that answers this way once may answer
// correctly on the next attempt.
func (result WorkItemResult) Validate() error {
	if !boundedText(result.ExternalID, 4096, false) {
		return fmt.Errorf("publisher: work-item result carries no usable external identity")
	}
	if result.URL != "" {
		parsed, err := url.Parse(result.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("publisher: work-item result carries no usable external identity: URL is invalid")
		}
	}
	return nil
}
```

- [ ] Add `"net/url"` to `workitem.go`'s imports.
- [ ] Apply the gate at both sites in `WorkItemService.Execute`. Replace the recovered-result branch (`workitem.go:129-133`) with:

```go
	if found {
		if err := prior.Validate(); err != nil {
			return Publication{}, err
		}
		return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: prior.ExternalID, URL: prior.URL,
		})
	}
```

and insert the same two-line check between the `Publish` error branch and the final `Complete` (after `workitem.go:142`):

```go
	if err := result.Validate(); err != nil {
		return Publication{}, err
	}
```

- [ ] Run `go test ./agent/publisher/ -count=1` and confirm every test passes, including the pre-existing `TestWorkItemServicePublishesExplicitCommentIdempotently` and `TestWorkItemServiceReconcilesCrashAfterProviderSuccessWithoutRepeatingWrite` (their stub results already carry usable identities).
- [ ] Run `gofmt -l agent/publisher` and confirm no output.
- [ ] Commit `feat(publisher): require work-item results to carry a usable identity`.

### Task 9: Extract the gateway conformance kit

**Files:**
- Create: `agent/publisher/contracttest/kit.go`
- Create: `agent/publisher/contracttest/fixture.go`
- Create: `agent/publisher/contracttest/kit_test.go`

The kit mirrors `agent/devmcp/contracttest/kit.go`: one exported `Run(t, Config)` that executes named sub-checks, opt-in extras behind `Config` fields, everything expressed as "what any implementation must do". Read-only checks are always safe to run against a real deployment; write checks are opt-in because they create an external effect.

- [ ] Create `agent/publisher/contracttest/fixture.go` — the repository-change fixture the write checks publish, plus the in-memory snapshot stores the executor needs:

```go
package contracttest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

// digestOf mirrors agent/publisher/gateway_test.go:633-636. There is no
// exported helper for this in agent/snapshot (verified: no MustDigestOf).
func digestOf(content []byte) snapshot.Digest {
	sum := sha256.Sum256(content)
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// ChangeFixture is an exact sealed repository-change/v1 value plus its
// canonical archive bytes. NewSyntheticChange builds one for the in-repo run;
// LoadChangeFixture wraps a real canonical tar for a live run against a
// gateway that will actually apply it.
type ChangeFixture struct {
	Manifest  snapshot.Snapshot
	Archive   []byte
	BaseSHA   string
	ResultSHA string
}

func (fixture ChangeFixture) stores() (snapshot.MetadataStore, snapshot.ContentStore) {
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedReturns(fixture.Manifest, true, nil)
	content := &snapshotfakes.FakeContentStore{}
	archive := fixture.Archive
	content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	return metadata, content
}

func (fixture ChangeFixture) ref() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: fixture.Manifest.ID, Type: fixture.Manifest.Type, Digest: fixture.Manifest.Digest,
	}
}

// NewSyntheticChange builds a valid repository-change/v1 whose payload is not
// a real Git bundle. It is enough for the reference gateway and for every
// read-only check; a live gateway that applies the change needs
// LoadChangeFixture instead.
func NewSyntheticChange(t *testing.T, baseSHA, resultSHA, resultTree string) ChangeFixture {
	t.Helper()
	payload := []byte("bundle bytes")
	body := contracts.RepositoryChangeBody{
		RepositoryID: digestOf([]byte("repository")).String(),
		BaseSHA:      baseSHA, ResultCommit: resultSHA, ResultTree: resultTree,
		Representation: "git-bundle",
		Payload: contracts.ContentRef{
			Path: "content/change.bundle", Digest: digestOf(payload),
			MediaType: "application/x-git-bundle",
		},
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("repository-change/v1"),
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "repository",
			Type: snapshot.TypeRef("repository/v1"), Digest: digestOf([]byte("base")),
		}},
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	raw := referenceTar(t, map[string][]byte{"content/change.bundle": payload, "record.json": document})
	return captureChange(t, raw, body, baseSHA, resultSHA, resultTree)
}

// LoadChangeFixture wraps a caller-supplied repository-change/v1 tar. Use it
// for a live gateway that will really apply the change.
func LoadChangeFixture(t *testing.T, tarPath, baseSHA, resultSHA, resultTree string) ChangeFixture {
	t.Helper()
	raw, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	var body contracts.RepositoryChangeBody
	return captureChange(t, raw, body, baseSHA, resultSHA, resultTree)
}
```

- [ ] Implement `captureChange` and `referenceTar` in the same file. `captureChange` runs the raw tar through `snapshot.Canonicalizer{}.Capture`, reads `tree.ArchivePath` into memory, closes the tree, and assembles the `snapshot.Snapshot` manifest with `ID: 41`, `Type: "repository-change/v1"`, `Representation: "application/x-tar"`, `ContentState: snapshot.ContentStateAvailable`, `CreatedAt: time.Now().UTC()`, and `IntrinsicMetadata` marshalled from `contracts.RepositoryChangeMetadata{RepositoryID: body.RepositoryID, BaseSHA: baseSHA, ResultCommit: resultSHA, ResultTree: resultTree, Representation: "git-bundle", ChangedFiles: []string{}}`. `referenceTar` is the same deterministic `archive/tar` writer as `agent/publisher/gateway_test.go:609-631`. Both are lifted verbatim from `newGatewaySnapshotFixture` (`gateway_test.go:508-570`) — read that function first and keep the two in agreement.
- [ ] Confirm the fixture's manifest reproduces what `newGatewaySnapshotFixture` produces: run `go test ./agent/publisher/contracttest/ -run TestReferenceGateway -count=1` after Task 9's kit test exists and check that `publish_then_lookup_is_visible` reports `head_sha` equal to the fixture's `ResultSHA`. A mismatch means `IntrinsicMetadata` and `record.json` disagree, which `SnapshotChangeInspector.Inspect` rejects with `repository-change intrinsic metadata does not match exact content` (`gateway.go:904-909`).
- [ ] Create `agent/publisher/contracttest/kit.go`:

```go
package contracttest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

// Config describes the gateway under test. Endpoint, TokenFile, TeamName and
// ApprovalPolicyVersion are always required; the Git fields are required for
// the current-base check and for writes.
type Config struct {
	Endpoint              string
	TokenFile             string
	CACertificateFile     string
	TeamName              string
	ApprovalPolicyVersion string
	GitDestination        string
	GitTargetBranch       string

	// Change enables the write checks. Leave it nil to run the read-only
	// protocol checks, which create no external effect.
	Change *ChangeFixture

	// Timeout bounds each individual check (default 2m).
	Timeout time.Duration
}

// Run executes the gateway protocol conformance checks. Every check names an
// obligation from docs/agentic/README.md; an implementation that fails any of
// them can duplicate or lose an external side effect.
func Run(t *testing.T, config Config) {
	t.Helper()
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	token := readToken(t, config.TokenFile)
	client := newKitClient(t, config)

	run := func(name string, check func(ctx context.Context) error) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
			defer cancel()
			if err := check(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	run("auth_is_required", func(ctx context.Context) error {
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", "", randomKey(), lookupBody(randomKey()))
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return fmt.Errorf("unauthenticated lookup answered %d; it must answer 401 or 403, never %d and never 200", status, status)
		}
		return nil
	})

	run("bad_token_is_terminal", func(ctx context.Context) error {
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token+"-wrong", randomKey(), lookupBody(randomKey()))
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return fmt.Errorf("a rejected token answered %d; a 5xx here makes the client retry a permanently bad token until its lease expires forever", status)
		}
		return nil
	})

	run("unknown_operation_key_is_not_found", func(ctx context.Context) error {
		key := randomKey()
		status, body, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token, key, lookupBody(key))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("lookup of an unknown key answered %d, want 200", status)
		}
		var response struct {
			Found  bool `json:"found"`
			Result struct {
				ExternalID string `json:"external_id"`
				URL        string `json:"url"`
				HeadSHA    string `json:"head_sha"`
			} `json:"result"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&response); err != nil {
			return fmt.Errorf("lookup response carries fields the client rejects (it decodes with DisallowUnknownFields): %w", err)
		}
		if response.Found || response.Result.ExternalID != "" {
			return fmt.Errorf("an unknown operation key answered %+v, want found:false with no result", response)
		}
		return nil
	})

	run("lookup_requires_the_idempotency_key", func(ctx context.Context) error {
		key := randomKey()
		status, _, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup", token, "", lookupBody(key))
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			return fmt.Errorf("lookup without an Idempotency-Key answered 200; the key is how the durable operation is identified")
		}
		return nil
	})

	// Registered only when a destination is configured: never call t.Skip from
	// inside a check closure — it holds the parent *testing.T and would skip
	// the whole kit.
	if config.GitDestination != "" && config.GitTargetBranch != "" {
		run("current_base_needs_no_idempotency_key", func(ctx context.Context) error {
			return checkCurrentBase(ctx, client, config, token)
		})
	}

	run("unknown_endpoint_is_terminal", func(ctx context.Context) error {
		status, _, err := post(ctx, client, config.Endpoint, "/v1/definitely-not-an-endpoint", token, randomKey(), `{}`)
		if err != nil {
			return err
		}
		if status < 400 || status >= 500 {
			return fmt.Errorf("an unknown endpoint answered %d; it must answer a terminal 4xx so the client stops instead of retrying forever", status)
		}
		return nil
	})

	if config.Change == nil {
		return
	}
	// The write checks share the first publication so the second can prove
	// byte-identical recovery. They run in order because t.Run is sequential.
	directory := t.TempDir()
	var published publisher.Result

	run("publish_then_lookup_is_visible", func(ctx context.Context) error {
		result, err := publishOnce(ctx, config, directory)
		if err != nil {
			return err
		}
		published = result
		status, body, err := post(ctx, client, config.Endpoint, "/v1/publications/lookup",
			token, operationKeyFor(config), lookupBody(operationKeyFor(config)))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("lookup after a landed publish answered %d, want 200", status)
		}
		var response struct {
			Found  bool `json:"found"`
			Result struct {
				ExternalID string `json:"external_id"`
				URL        string `json:"url"`
				HeadSHA    string `json:"head_sha"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return err
		}
		if !response.Found {
			return fmt.Errorf("a landed publish is invisible to lookup; every crash-recovery guarantee rests on this")
		}
		if response.Result.ExternalID != published.ExternalID || response.Result.HeadSHA != config.Change.ResultSHA {
			return fmt.Errorf("recovered result %+v does not match the published one %+v", response.Result, published)
		}
		return nil
	})

	run("publish_is_idempotent_under_one_key", func(ctx context.Context) error {
		result, err := publishOnce(ctx, config, directory)
		if err != nil {
			return err
		}
		if result.ExternalID != published.ExternalID || result.URL != published.URL {
			return fmt.Errorf("a second publish under one operation key produced %+v, want the first result %+v — the gateway created a second external effect",
				result, published)
		}
		return nil
	})
}
```

- [ ] Implement the remaining kit internals in the same file. None of them may call `t.Fatal`: they run inside sub-tests and must return errors, using the parent `t` only for `t.TempDir()`.
  - `readToken(t, path)` — read the mounted token file and `strings.TrimSpace` it.
  - `newKitClient(t, config)` — an `*http.Client` whose `RootCAs` come from `config.CACertificateFile` when set, otherwise the system pool.
  - `post(ctx, client, endpoint, path, token, key, body) (int, []byte, error)` — POST JSON with `Authorization`/`Idempotency-Key` set only when non-empty, returning status and a body bounded by `io.LimitReader(…, 1<<20)`.
  - `lookupBody(key)` — `{"publisher":"git-publisher/v1","operation_key":"<key>"}`.
  - `randomKey()` — `"sha256:" + hex.EncodeToString(<32 random bytes>)`, matching `operationKeyPattern` (`agent/publisher/store.go:286`).
  - `validObjectID(value)` — 40 or 64 lowercase hex characters, mirroring `validGitObjectID` (`gateway.go:737-747`).
  - `checkCurrentBase(ctx, client, config, token)` — POST `/v1/git/current-base` **without** an `Idempotency-Key`, require `200`, decode with `DisallowUnknownFields` into `struct{ BaseSHA string \`json:"base_sha"\` }`, and require `validObjectID(BaseSHA)`. Failing status message: `current-base answered %d without an Idempotency-Key; the client never sends one here (agent/publisher/gateway.go:473)`.
  - `gitRequest(config)` — the `publisher.Request` the write checks publish: `GitPublisher`, `config.Change.ref()`, `config.GitDestination`, `ModePullRequest`, parameters `source_branch: "agent/contracttest"` and `target_branch: config.GitTargetBranch`, `config.ApprovalPolicyVersion`, and an `Authority{TeamID: 9, TeamName: config.TeamName, BuildID: 12, WorkflowRunID: 17, Actor: "contracttest"}`.
  - `operationKeyFor(config)` — `gitRequest(config).OperationKey()`, discarding the error (the request is constructed valid).
  - `publishOnce(ctx, config, directory)` — write a policy file into `directory` allowing exactly `config.TeamName` / `git-publisher/v1` / `pull-request` / `config.ApprovalPolicyVersion` / `config.GitTargetBranch` / `config.GitDestination`, build a `publisher.GatewayConfig` from `Config` (`RequestTimeout: 30 * time.Second`, `LeaseDuration: 5 * time.Minute`, `MaxResponseBytes: 1 << 20`), compose `publisher.NewGatewayExecutor` over a **fresh** `publisher.NewMemoryStore(time.Now)` and `config.Change.stores()`, `Execute` `gitRequest(config)`, require `StatusSucceeded`, and return `publication.Result`. A fresh store per call is what makes the second call a real second request rather than a durable replay.
- [ ] Create `agent/publisher/contracttest/kit_test.go` running the kit against the reference implementation:

```go
package contracttest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

func TestReferenceGatewaySatisfiesTheConformanceKit(t *testing.T) {
	base := strings.Repeat("1", 40)
	result := strings.Repeat("2", 40)
	tree := strings.Repeat("3", 40)
	reference := contracttest.NewReferenceServer(t, contracttest.ReferenceOptions{CurrentBase: base})
	change := contracttest.NewSyntheticChange(t, base, result, tree)

	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(caPath, contracttest.ReferenceCAPEM(reference), 0600); err != nil {
		t.Fatal(err)
	}

	contracttest.Run(t, contracttest.Config{
		Endpoint: reference.Server.URL, TokenFile: tokenPath, CACertificateFile: caPath,
		TeamName: "engineering", ApprovalPolicyVersion: "engineering/v1",
		GitDestination: "git.example/acme/widget", GitTargetBranch: "main",
		Change: &change,
	})
	if reference.ExternalEffects() != 1 {
		t.Fatalf("the kit's write checks produced %d external effects, want exactly 1", reference.ExternalEffects())
	}
}
```

- [ ] Confirm `ReferenceCAPEM` from Task 4 is what the kit test writes to `ca.pem`; it is the same encoding `gatewayTestConfig` performs (`gateway_test.go:597`), which is what `publisher.GatewayConfig.CACertificateFile` expects.
- [ ] Run `go test ./agent/publisher/contracttest/ -count=1 -v` and confirm every named check passes and the effect count is exactly 1.
- [ ] Run `go test ./agent/publisher/... -count=1` and confirm both packages are green.
- [ ] Commit `test(publisher): add the gateway protocol conformance kit`.

### Task 10: Gate the kit against a real deployed gateway

**Files:**
- Create: `agent/publisher/contracttest/live_test.go`
- Modify: `docs/agentic/README.md`

The kit's value is that it can be pointed at the out-of-repo service everything rests on. This runs behind `//go:build live` so it never enters `go list ./...`, `make test-unit`, or any pipeline, and it skips cleanly when the environment is absent.

- [ ] Create `agent/publisher/contracttest/live_test.go`:

```go
//go:build live
// +build live

package contracttest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/publisher/contracttest"
)

// TestLiveGatewayContract runs the publisher gateway conformance kit against a
// real deployment. It is read-only unless a change fixture is supplied.
//
//	PUBLISHER_GATEWAY_URL=https://gateway.example \
//	PUBLISHER_GATEWAY_TOKEN=<bearer token> \
//	PUBLISHER_GATEWAY_DESTINATION=git.example/acme/scratch \
//	PUBLISHER_GATEWAY_TARGET_BRANCH=main \
//	PUBLISHER_GATEWAY_TEAM=engineering \
//	PUBLISHER_GATEWAY_POLICY_VERSION=engineering/v1 \
//	go test -tags live -run '^TestLiveGatewayContract$' -count=1 -v ./agent/publisher/contracttest/
//
// Optional:
//
//	PUBLISHER_GATEWAY_CA_FILE   absolute path to a private CA bundle
//	PUBLISHER_GATEWAY_CHANGE_TAR, _BASE_SHA, _RESULT_SHA, _RESULT_TREE
//	    enable the write checks against a SCRATCH destination. They create a
//	    real external effect. Never point them at a production repository.
func TestLiveGatewayContract(t *testing.T) {
	endpoint := os.Getenv("PUBLISHER_GATEWAY_URL")
	token := os.Getenv("PUBLISHER_GATEWAY_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("PUBLISHER_GATEWAY_URL and PUBLISHER_GATEWAY_TOKEN are not set")
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	config := contracttest.Config{
		Endpoint: endpoint, TokenFile: tokenPath,
		CACertificateFile:     os.Getenv("PUBLISHER_GATEWAY_CA_FILE"),
		TeamName:              envOrDefault("PUBLISHER_GATEWAY_TEAM", "engineering"),
		ApprovalPolicyVersion: envOrDefault("PUBLISHER_GATEWAY_POLICY_VERSION", "engineering/v1"),
		GitDestination:        os.Getenv("PUBLISHER_GATEWAY_DESTINATION"),
		GitTargetBranch:       envOrDefault("PUBLISHER_GATEWAY_TARGET_BRANCH", "main"),
	}
	if tarPath := os.Getenv("PUBLISHER_GATEWAY_CHANGE_TAR"); tarPath != "" {
		change := contracttest.LoadChangeFixture(t, tarPath,
			os.Getenv("PUBLISHER_GATEWAY_BASE_SHA"),
			os.Getenv("PUBLISHER_GATEWAY_RESULT_SHA"),
			os.Getenv("PUBLISHER_GATEWAY_RESULT_TREE"),
		)
		config.Change = &change
	}
	contracttest.Run(t, config)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
```

- [ ] Run `go build -tags live ./agent/publisher/...` and confirm it compiles.
- [ ] Run `go test -tags live -run '^TestLiveGatewayContract$' -count=1 ./agent/publisher/contracttest/` with no gateway environment set and confirm the clean skip:

```
--- SKIP: TestLiveGatewayContract (0.00s)
    live_test.go:XX: PUBLISHER_GATEWAY_URL and PUBLISHER_GATEWAY_TOKEN are not set
ok  	github.com/concourse/concourse/agent/publisher/contracttest
```

- [ ] Run `go test ./agent/publisher/... -count=1` (no tag) and confirm the live file is excluded and everything still passes.
- [ ] In `docs/agentic/README.md`, extend the gateway-contract paragraph that ends `…can duplicate an external side effect.` (`:511-516`) with the taxonomy and the kit:

> Gateway responses are classified: `400`, `401`, `403`, `404`, `409`, and `422`
> are terminal and complete the publication as `failed`, because retrying
> identical request bytes cannot change the answer; every other status —
> including `408`, `429`, all `5xx`, and any status not named here — leaves the
> operation `pending` for a later lease reclaim. A gateway must therefore never
> answer a permanently rejected request with a `5xx`. The obligations above are
> executable: `agent/publisher/contracttest` runs them against any endpoint,
> and `go test -tags live -run TestLiveGatewayContract ./agent/publisher/contracttest/`
> runs them against a deployment.

- [ ] Run `go test ./agent/publisher/... -count=1` and `gofmt -l agent/publisher` one final time.
- [ ] Commit `docs(agentic): document the gateway retry taxonomy and conformance kit`.

---

## Self-review against WS4 acceptance criteria

| WS4 acceptance criterion | Where it is satisfied |
|---|---|
| "A 403 from the fake lands the publication in `failed` (no infinite retry)" | Task 2 `TestGatewayTerminalStatusFailsPublicationInsteadOfRetryingForever` — asserts `StatusFailed` **and** that a post-lease replay makes zero gateway calls; Task 5 table rows `forbidden`/`bad request`/`unauthorized`/`not found`/`conflict`/`unprocessable` |
| "a 503 leaves it pending" | Task 2 `TestGatewayRetryableStatusStaysPendingAndReclaimable`; Task 5 rows `service unavailable`, `throttled`, `internal server error`, `bad gateway`, `request timeout`, `unlisted status stays open` |
| "The malformed-JSON decoder paths are covered" | Task 5 `TestGatewayRejectsMalformedResponseBodies` — `unknown field` reaches `DisallowUnknownFields`, `trailing value` reaches `requireJSONEOF`, `truncated object`/`wrong shape` reach `decodeGatewayJSON`, all with `MaxResponseBytes` at its default so the size guard no longer fires first |
| "The crash-4 test fails if `Lookup` is moved after the write" | Task 7 — the ordered path assertion plus the manual mutation check that proves the assertion has teeth |
| "The kit passes against the reference fake" | Task 9 `TestReferenceGatewaySatisfiesTheConformanceKit` |
| WS4 item 1 — typed `GatewayError` wrapped with `%w`, both classes tested on both stores | Tasks 1, 2 (memory store) and 3 (`atc/db`, under plan 01's `db-tests` job) |
| WS4 item 2 — transport seam + adversarial fake, misnamed test split | Task 5, with the seam decision recorded and justified in "Decision 1" |
| WS4 item 3 — stale-lookup arm, byte-identical idempotency key | Task 6 |
| WS4 item 5 — work-item recovered-result cross-check | Task 8, with the scope of the available analogue recorded in "Decision 3" and in the code comment |
| WS4 item 6 — conformance kit + live gate | Tasks 4, 9, 10 |

## Deliberately out of scope

- **`Retry-After` is parsed by nobody.** Retries happen at lease expiry, not in an in-process loop, so there is no delay to apply. Task 5 asserts we never claim otherwise; honoring it needs a scheduling change, not an error-handling change.
- **`ResponseHeaderTimeout` equals the per-call deadline** (`gateway.go:376` vs `git.go:152`), so the transport's own header bound can never fire first. Recorded in "Decision 1"; changing it is a production change WS4 does not authorize.
- **Disambiguating a terminal status on a write endpoint.** A `409` on `/v1/git/publish` may mean the effect landed. `Detail` names the endpoint and status for manual reconciliation; a protocol extension that lets the gateway say so is a separate conversation ("Decision 2").
- **The `CurrentBase` → `Publish` TOCTOU window** (`git.go:193` → `:208`). Unmitigated in repo and unchanged here; the gateway is the only party that can close it.
