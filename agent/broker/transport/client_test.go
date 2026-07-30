package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/transport"
)

func TestClientPostsAdmissionWithBootstrapCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transport.AdmitPath || r.Header.Get("Authorization") != "Bearer bootstrap-token" {
			t.Fatalf("request = %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body transport.AdmitRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ProfileID != "profile" || body.IdempotencyKey != "call" {
			t.Fatalf("admission body = %#v, %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"execution_id":"child-1","execution_capability":"execution-token"}`))
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Admit(context.Background(), broker.AdmissionRequest{IdempotencyKey: "call", Tool: broker.ToolConsultAgent, Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh}, ProfileID: "profile", ProfileDigest: "sha256:" + strings.Repeat("a", 64), InputDigest: "sha256:" + strings.Repeat("b", 64), Attachments: []string{"design"}})
	if err != nil || id.ExecutionID != "child-1" {
		t.Fatalf("Admit() = %#v, %v", id, err)
	}
}

func TestClientUsesExecutionCapabilityAfterAdmission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == transport.AdmitPath {
			if r.Header.Get("Authorization") != "Bearer bootstrap-token" {
				t.Fatalf("admit authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"execution_id":"child-1","execution_capability":"execution-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer execution-token" {
			t.Fatalf("lifecycle authorization = %q", r.Header.Get("Authorization"))
		}
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Admit(context.Background(), broker.AdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Phase(context.Background(), id.ExecutionID, "running"); err != nil {
		t.Fatal(err)
	}
}

func TestClientCapturesWorkspaceAndAtomicallyReplacesExecutionCapability(t *testing.T) {
	const executionID = "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98"
	var lifecycleAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case transport.AdmitPath:
			_, _ = w.Write([]byte(`{"execution_id":"` + executionID + `","execution_capability":"phase-token"}`))
		case transport.PhasePath(executionID):
			if r.Header.Get("Authorization") == "Bearer phase-token" {
				_, _ = w.Write([]byte(`{"execution_capability":"capture-token"}`))
				return
			}
			lifecycleAuthorization = r.Header.Get("Authorization")
		case transport.WorkspaceCapturePath(executionID):
			if r.Header.Get("Authorization") != "Bearer capture-token" {
				t.Fatalf("capture authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"snapshot":{"id":"77","type":"repository-change/v1","digest":"sha256:` + strings.Repeat("7", 64) + `"},"execution_capability":"lifecycle-token"}`))
		default:
			lifecycleAuthorization = r.Header.Get("Authorization")
		}
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := client.Admit(context.Background(), broker.AdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Phase(context.Background(), admitted.ExecutionID, "capturing"); err != nil {
		t.Fatal(err)
	}
	capture := broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree: strings.Repeat("3", 40), PatchDigest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		EntryCount: 1, PolicyRevision: "git-workspace-capture/v2",
	}
	sealed, err := client.CaptureWorkspace(context.Background(), admitted.ExecutionID, capture)
	if err != nil || sealed.ID != 77 {
		t.Fatalf("CaptureWorkspace() = %#v, %v", sealed, err)
	}
	if err := client.Phase(context.Background(), admitted.ExecutionID, "running"); err != nil {
		t.Fatal(err)
	}
	if lifecycleAuthorization != "Bearer lifecycle-token" {
		t.Fatalf("lifecycle authorization = %q", lifecycleAuthorization)
	}
}

func TestClientRejectsMalformedWorkspaceCaptureResponseWithoutReplacingCapability(t *testing.T) {
	const executionID = "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == transport.AdmitPath {
			_, _ = w.Write([]byte(`{"execution_id":"` + executionID + `","execution_capability":"capture-token"}`))
			return
		}
		if r.URL.Path == transport.WorkspaceCapturePath(executionID) {
			_, _ = w.Write([]byte(`{"snapshot":{"id":0},"execution_capability":"replacement"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer capture-token" {
			t.Fatalf("capability changed after malformed response: %q", r.Header.Get("Authorization"))
		}
	}))
	defer server.Close()
	client, _ := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	admitted, _ := client.Admit(context.Background(), broker.AdmissionRequest{})
	_, err := client.CaptureWorkspace(context.Background(), admitted.ExecutionID, broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree:  strings.Repeat("3", 40),
		PatchDigest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		EntryCount:  1, PolicyRevision: "git-workspace-capture/v2",
	})
	if err == nil {
		t.Fatal("CaptureWorkspace() accepted malformed response")
	}
	if err := client.FailWorkspaceCapture(context.Background(), admitted.ExecutionID); err != nil {
		t.Fatalf("FailWorkspaceCapture(): %v", err)
	}
}

func TestClientUsesExecutionCapabilitiesConcurrently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == transport.AdmitPath {
			_, _ = w.Write([]byte(`{"execution_id":"child-1","execution_capability":"execution-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer execution-token" {
			t.Fatalf("lifecycle authorization = %q", r.Header.Get("Authorization"))
		}
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Admit(context.Background(), broker.AdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errors := make(chan error, 16)
	for index := 0; index < cap(errors); index++ {
		group.Add(1)
		go func() { defer group.Done(); errors <- client.Phase(context.Background(), id.ExecutionID, "running") }()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Phase(): %v", err)
		}
	}
}

func TestClientRetainsExecutionCapabilityForTerminalReplay(t *testing.T) {
	var terminalCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == transport.AdmitPath {
			_, _ = w.Write([]byte(`{"execution_id":"child-1","execution_capability":"execution-token"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer execution-token" {
			t.Fatalf("terminal authorization = %q", r.Header.Get("Authorization"))
		}
		terminalCalls.Add(1)
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Admit(context.Background(), broker.AdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	terminal := broker.Terminal{State: broker.ExecutionErrored, Code: "provider_rejected", Retryable: true}
	if err := client.Terminal(context.Background(), id.ExecutionID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := client.Terminal(context.Background(), id.ExecutionID, terminal); err != nil {
		t.Fatal(err)
	}
	if terminalCalls.Load() != 2 {
		t.Fatalf("terminal calls = %d", terminalCalls.Load())
	}
}

func TestClientDoesNotDiscloseBootstrapCredentialInErrors(t *testing.T) {
	client, err := transport.NewClient(transport.Config{Endpoint: "http://127.0.0.1:1", BootstrapCapability: "top-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Admit(context.Background(), broker.AdmissionRequest{})
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestClientRejectsRedirectBeforeForwardingBootstrapCredential(t *testing.T) {
	var redirected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == transport.AdmitPath {
			http.Redirect(w, r, "/redirected", http.StatusFound)
			return
		}
		if r.URL.Path == "/redirected" {
			redirected.Store(true)
			if r.Header.Get("Authorization") != "" {
				t.Fatalf("redirect received bootstrap credential")
			}
		}
	}))
	defer server.Close()
	client, err := transport.NewClient(transport.Config{Endpoint: server.URL, BootstrapCapability: "bootstrap-token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Admit(context.Background(), broker.AdmissionRequest{})
	if err == nil {
		t.Fatal("Admit() accepted redirect")
	}
	if redirected.Load() {
		t.Fatal("client followed redirect")
	}
}
