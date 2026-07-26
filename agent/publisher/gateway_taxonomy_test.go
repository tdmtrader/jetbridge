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
