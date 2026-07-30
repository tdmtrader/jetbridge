package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		_, _ = w.Write([]byte(`{"execution_id":"child-1"}`))
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
