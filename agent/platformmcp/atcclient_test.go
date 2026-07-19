package platformmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

func testClient(atcURL string) *platformmcp.ATCClient {
	return platformmcp.NewATCClient(atcURL, "cap1.9.secret", 42)
}

// stubQuestionStore is an in-memory question store for the stub ATC. It
// stands in for agent/api/questions.MemoryStore (Slice B — not landed yet;
// this ticket may not touch agent/api), serving the same wire shape.
type stubQuestionStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]*platformmcp.Question
}

func newStubQuestionStore() *stubQuestionStore {
	return &stubQuestionStore{rows: map[int]*platformmcp.Question{}}
}

// Ask is FIND-OR-CREATE, mirroring the §E DB-enforced idempotency-by-question
// (migration 1773106072): rows dedup on (pipeline_run_id, step_name, kind,
// question_hash) — the build id is deliberately NOT part of the key, and rows
// without a pipeline_run_id never dedup. An answered row is returned as-is
// (the resume fast path); an open row is joined.
func (s *stubQuestionStore) Ask(q *platformmcp.Question) *platformmcp.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.PipelineRunID != nil {
		for _, row := range s.rows {
			if row.PipelineRunID != nil && *row.PipelineRunID == *q.PipelineRunID &&
				row.StepName == q.StepName && row.Kind == q.Kind &&
				questionDedupKey(row) == questionDedupKey(q) {
				copied := *row
				return &copied
			}
		}
	}
	s.nextID++
	q.ID = s.nextID
	q.AskedAt = time.Now().Unix()
	s.rows[q.ID] = q
	copied := *q
	return &copied
}

// questionDedupKey stands in for the server-side question_hash
// (sha256(question || '\x00' || options-joined-by-'\x00')).
func questionDedupKey(q *platformmcp.Question) string {
	return q.Question + "\x00" + strings.Join(q.Options, "\x00")
}

func (s *stubQuestionStore) Get(id int) (platformmcp.Question, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.rows[id]
	if !ok {
		return platformmcp.Question{}, false
	}
	return *q, true
}

func (s *stubQuestionStore) Answer(ticketID, id int, answer, answeredBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.rows[id]
	if !ok || q.TicketID != ticketID {
		return fmt.Errorf("question %d not found for ticket %d", id, ticketID)
	}
	if q.AnsweredAt != 0 {
		return fmt.Errorf("question %d already answered", id)
	}
	q.Answer = answer
	q.AnsweredBy = answeredBy
	q.AnsweredAt = time.Now().Unix()
	return nil
}

// stubQuestionMux serves the question routes against the stub store, checking
// the bearer token — the shape the real ATC exposes after the Slice B route
// registration. GET honors ?wait= with a 20ms poll (long-poll long enough for
// AwaitAnswer's park loop to be exercised).
func stubQuestionMux(t *testing.T, store *stubQuestionStore) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer cap1.9.secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/v1/agent/tickets/42/questions", auth(func(w http.ResponseWriter, r *http.Request) {
		var q platformmcp.Question
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q.TicketID = 42
		if q.TimeoutPolicy == "" {
			q.TimeoutPolicy = "park"
		}
		created := store.Ask(&q)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	}))
	mux.HandleFunc("GET /api/v1/agent/tickets/42/questions/{question_id}", auth(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("question_id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var wait time.Duration
		if raw := r.URL.Query().Get("wait"); raw != "" {
			wait, _ = time.ParseDuration(raw)
		}
		deadline := time.Now().Add(wait)
		for {
			q, found := store.Get(id)
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if q.AnsweredAt != 0 || time.Now().After(deadline) {
				writeJSON(w, q)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	mux.HandleFunc("PUT /api/v1/agent/tickets/42/questions/{question_id}/answer", auth(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("question_id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Answer     string `json:"answer"`
			AnsweredBy string `json:"answered_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := store.Answer(42, id, body.Answer, body.AnsweredBy); err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		q, _ := store.Get(id)
		writeJSON(w, q)
	}))
	return mux
}

func TestAskAndAwaitAnswer(t *testing.T) {
	store := newStubQuestionStore()
	srv := httptest.NewServer(stubQuestionMux(t, store))
	defer srv.Close()
	c := testClient(srv.URL)

	created, err := c.AskQuestion(context.Background(), &platformmcp.Question{
		Question: "proceed?", Options: []string{"yes", "no"}, TimeoutPolicy: "park",
	})
	if err != nil {
		t.Fatalf("AskQuestion: %v", err)
	}
	if created.ID == 0 || created.TicketID != 42 {
		t.Fatalf("unexpected created: %+v", created)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.Answer(42, created.ID, "yes", "tdm")
	}()

	answered, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v", timedOut, err)
	}
	if answered.Answer != "yes" || answered.AnsweredBy != "tdm" {
		t.Fatalf("unexpected answer: %+v", answered)
	}
}

func TestAwaitAnswerDeadline(t *testing.T) {
	store := newStubQuestionStore()
	srv := httptest.NewServer(stubQuestionMux(t, store))
	defer srv.Close()
	c := testClient(srv.URL)
	c.PollWait = 50 * time.Millisecond

	created, err := c.AskQuestion(context.Background(), &platformmcp.Question{Question: "anyone?"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(120 * time.Millisecond)
	_, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, &deadline)
	if err != nil {
		t.Fatalf("AwaitAnswer: %v", err)
	}
	if !timedOut {
		t.Fatal("expected timedOut=true")
	}
}

// TestAwaitAnswerSurvivesATCRestart is the unit-level proof of "survives a
// web-node restart while parked": the stub ATC is killed mid-long-poll and
// re-listened on the same address, and AwaitAnswer keeps polling through it.
func TestAwaitAnswerSurvivesATCRestart(t *testing.T) {
	store := newStubQuestionStore()
	mux := stubQuestionMux(t, store)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv1 := &http.Server{Handler: mux}
	go srv1.Serve(ln)

	c := testClient("http://" + addr)
	c.PollWait = 50 * time.Millisecond
	c.RetryInterval = 20 * time.Millisecond

	created, err := c.AskQuestion(context.Background(), &platformmcp.Question{Question: "still there?"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var answered atomic.Pointer[platformmcp.Question]
	go func() {
		defer close(done)
		q, timedOut, err := c.AwaitAnswer(context.Background(), created.ID, nil)
		if err != nil || timedOut {
			t.Errorf("AwaitAnswer after restart: timedOut=%v err=%v", timedOut, err)
			return
		}
		answered.Store(q)
	}()

	// Kill the "web node" mid-park.
	time.Sleep(80 * time.Millisecond)
	srv1.Close()

	// Restart on the same address (retry the bind while the OS releases it).
	var ln2 net.Listener
	for i := 0; i < 50; i++ {
		ln2, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("rebind %s: %v", addr, err)
	}
	srv2 := &http.Server{Handler: mux}
	defer srv2.Close()
	go srv2.Serve(ln2)

	// Answer via the restarted web node.
	time.Sleep(80 * time.Millisecond)
	if err := store.Answer(42, created.ID, "yes", "tdm"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitAnswer did not resume after ATC restart")
	}
	if q := answered.Load(); q == nil || q.Answer != "yes" {
		t.Fatalf("unexpected resumed answer: %+v", q)
	}
}

const pendingQuestionBody = `{"id":7,"ticket_id":42,"kind":"question","question":"q","asked_at":1}`
const answeredQuestionBody = `{"id":7,"ticket_id":42,"kind":"question","question":"q","asked_at":1,"answered_at":2,"answer":"yes","answered_by":"tdm"}`

// scriptedStub serves GET question polls with per-attempt scripted responses —
// it drives AwaitAnswer's error classification (D6, 2026-07-09 SSE seam delta).
func scriptedStub(t *testing.T, respond func(attempt int) (status int, body string)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(attempts.Add(1))
		status, body := respond(n)
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

// D6 t1: a principal the ATC rejects forever becomes fatal after EXACTLY
// AuthFailureLimit (frozen 12) consecutive 401s — never a silent forever-park.
func TestAwaitAnswerFatalAfterConsecutiveAuthFailures(t *testing.T) {
	srv, attempts := scriptedStub(t, func(int) (int, string) {
		return http.StatusUnauthorized, ""
	})
	c := testClient(srv.URL)
	c.RetryInterval = time.Millisecond

	_, _, err := c.AwaitAnswer(context.Background(), 7, nil)
	if !errors.Is(err, platformmcp.ErrPrincipalRejected) {
		t.Fatalf("expected ErrPrincipalRejected, got %v", err)
	}
	if got := attempts.Load(); got != 12 {
		t.Fatalf("expected exactly 12 attempts (frozen AuthFailureLimit), got %d", got)
	}
}

// D6 t2: 5xx is NEVER fatal — a web restart may 500 for far more than the
// auth limit's worth of polls and the park must ride through it.
func TestAwaitAnswerRetries5xxForever(t *testing.T) {
	srv, _ := scriptedStub(t, func(attempt int) (int, string) {
		if attempt <= 20 { // > AuthFailureLimit
			return http.StatusInternalServerError, ""
		}
		return http.StatusOK, answeredQuestionBody
	})
	c := testClient(srv.URL)
	c.RetryInterval = time.Millisecond

	q, timedOut, err := c.AwaitAnswer(context.Background(), 7, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v", timedOut, err)
	}
	if q.Answer != "yes" {
		t.Fatalf("unexpected answer: %+v", q)
	}
}

// D6 t3: the counter is CONSECUTIVE — any success (even a still-pending poll)
// resets it, so alternating 401/success-pending never trips the fatal path.
func TestAwaitAnswerAuthFailureCounterResets(t *testing.T) {
	srv, _ := scriptedStub(t, func(attempt int) (int, string) {
		switch {
		case attempt <= 40 && attempt%2 == 1:
			return http.StatusUnauthorized, "" // 20 total 401s, never consecutive
		case attempt <= 40:
			return http.StatusOK, pendingQuestionBody
		default:
			return http.StatusOK, answeredQuestionBody
		}
	})
	c := testClient(srv.URL)
	c.PollWait = time.Millisecond
	c.RetryInterval = time.Millisecond

	q, timedOut, err := c.AwaitAnswer(context.Background(), 7, nil)
	if err != nil || timedOut {
		t.Fatalf("AwaitAnswer: timedOut=%v err=%v (counter must reset on success)", timedOut, err)
	}
	if q.Answer != "yes" {
		t.Fatalf("unexpected answer: %+v", q)
	}
}

func TestAnswerQuestionSendsBody(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	if err := c.AnswerQuestion(context.Background(), 1, "yes", "platform-mcp"); err != nil {
		t.Fatal(err)
	}
	if gotBody["answer"] != "yes" || gotBody["answered_by"] != "platform-mcp" {
		t.Fatalf("unexpected body: %v", gotBody)
	}
}
