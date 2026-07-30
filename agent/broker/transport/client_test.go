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
	if err != nil || id != "child-1" {
		t.Fatalf("Admit() = %q, %v", id, err)
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
	if err := client.Phase(context.Background(), id, "running"); err != nil {
		t.Fatal(err)
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
		go func() { defer group.Done(); errors <- client.Phase(context.Background(), id, "running") }()
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
	if err := client.Terminal(context.Background(), id, terminal); err != nil {
		t.Fatal(err)
	}
	if err := client.Terminal(context.Background(), id, terminal); err != nil {
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
