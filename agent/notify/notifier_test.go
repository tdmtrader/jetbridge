package notify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/notify"
)

func TestWebhookNotifierPostsPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := notify.NewWebhookNotifier(srv.URL, srv.Client())
	err := n.Notify(context.Background(), notify.Notification{
		Kind:     "question",
		TicketID: 42,
		Title:    "Agent question on ticket 42",
		URL:      "https://concourse.home/agent/tickets/42",
		Body:     "Which auth flow should I extend?",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// Exact §8.4 payload keys.
	if got["kind"] != "question" || got["ticket_id"] != float64(42) ||
		got["title"] != "Agent question on ticket 42" ||
		got["url"] != "https://concourse.home/agent/tickets/42" ||
		got["body"] != "Which auth flow should I extend?" {
		t.Fatalf("unexpected payload: %v", got)
	}
}

func TestWebhookNotifierErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	n := notify.NewWebhookNotifier(srv.URL, srv.Client())
	if err := n.Notify(context.Background(), notify.Notification{Kind: "question", TicketID: 1}); err == nil {
		t.Fatal("expected error on 502, got nil")
	}
}
