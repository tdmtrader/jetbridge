package platformmcp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

// OpenForTicket returns the ticket's still-open rows, oldest first — the stub
// stand-in for agent/api/questions.MemoryStore.OpenForTicket (Slice B — not
// landed yet; this ticket may not touch agent/api).
func (s *stubQuestionStore) OpenForTicket(ticketID int) []platformmcp.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []platformmcp.Question
	for _, q := range s.rows {
		if q.TicketID == ticketID && q.AnsweredAt == 0 {
			open = append(open, *q)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	return open
}

// ListForTicket returns up to limit of the ticket's rows, newest first.
func (s *stubQuestionStore) ListForTicket(ticketID, limit int) []platformmcp.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []platformmcp.Question
	for _, q := range s.rows {
		if q.TicketID == ticketID {
			all = append(all, *q)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// fullStubATC = a ticket read route + the real question routes on one server.
func fullStubATC(t *testing.T, store *stubQuestionStore) *httptest.Server {
	t.Helper()
	qmux := stubQuestionMux(t, store)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/agent/tickets/42/questions", qmux)
	mux.Handle("/api/v1/agent/tickets/42/questions/", qmux)
	mux.HandleFunc("GET /api/v1/agent/tickets/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"fix flaky test","state":"running"},"spec":null,"tasks":[]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newAskServer(t *testing.T, atcURL, policy string, timeoutSeconds int) *platformmcp.Server {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  policy,
		TimeoutSeconds: timeoutSeconds,
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)
	return srv
}

func TestAskHumanParksUntilAnswered(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	// Answer the (only) question shortly after it appears.
	go func() {
		for i := 0; i < 100; i++ {
			open := store.OpenForTicket(42)
			if len(open) == 1 {
				_ = store.Answer(42, open[0].ID, "oidc", "tdm")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Which auth flow?",
		"options":  []string{"legacy", "oidc"},
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"oidc"`) ||
		!strings.Contains(string(result), `"answered_by":"tdm"`) ||
		!strings.Contains(string(result), `"timed_out":false`) {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestAskHumanTimeoutDefaultPolicy(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "default", 1)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Proceed?",
		"options":  []string{"yes", "no"},
		"default":  "yes",
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"yes"`) ||
		!strings.Contains(string(result), `"timed_out":true`) {
		t.Fatalf("expected timed-out default answer, got: %s", result)
	}
	// The sidecar resolved the row so it never stays open (§3.2).
	open := store.OpenForTicket(42)
	if len(open) != 0 {
		t.Fatalf("timed-out question left open: %+v", open)
	}
	all := store.ListForTicket(42, 10)
	if all[0].AnsweredBy != "platform-mcp" {
		t.Fatalf("expected sidecar resolution, got %+v", all[0])
	}
}

func TestAskHumanTimeoutFailPolicy(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "fail", 1)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "Proceed?"})
	if !isErr {
		t.Fatalf("expected MCP-level failure, got: %s", result)
	}
	open := store.OpenForTicket(42)
	if len(open) != 0 {
		t.Fatalf("timed-out question left open: %+v", open)
	}
}

func TestAskHumanInputErrors(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)

	// policy=default requires the call's default field (§3.2).
	srv := newAskServer(t, atc.URL, "default", 60)
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "Proceed?"}); !isErr {
		t.Fatalf("expected input error without default, got %s", result)
	}
	// default must be one of options when options given.
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "Proceed?", "options": []string{"yes", "no"}, "default": "maybe",
	}); !isErr {
		t.Fatalf("expected default-not-in-options error, got %s", result)
	}
	// question required.
	if result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{}); !isErr {
		t.Fatalf("expected question-required error, got %s", result)
	}
}

// TestAskHumanPrincipalRejectedFailsLoudly (D6/F31 leg 3): once the per-run
// principal is revoked or expired the ATC 401s every poll; after the frozen
// 12 consecutive auth failures the tool call must fail LOUDLY with a
// "principal rejected:"-prefixed MCP tool error (isError=true) — never park
// forever on a dead principal.
func TestAskHumanPrincipalRejectedFailsLoudly(t *testing.T) {
	store := newStubQuestionStore()
	qmux := stubQuestionMux(t, store)
	mux := http.NewServeMux()
	// Asking succeeds; every subsequent poll is rejected (revoked principal).
	mux.Handle("POST /api/v1/agent/tickets/42/questions", qmux)
	mux.HandleFunc("GET /api/v1/agent/tickets/42/questions/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	atc := httptest.NewServer(mux)
	t.Cleanup(atc.Close)

	srv := newAskServer(t, atc.URL, "park", 0)

	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{"question": "anyone?"})
	if !isErr {
		t.Fatalf("expected MCP tool error after consecutive 401s, got %s", result)
	}
	if !strings.Contains(string(result), "principal rejected:") {
		t.Fatalf("expected 'principal rejected:' prefix in the tool error, got %s", result)
	}
}
