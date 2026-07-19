// Package contracttest validates any platform-mcp endpoint against the §3.2
// contract: seven tools, exact schema names, park/resume behavior. Run it
// in-process (this repo) or against a container (CI image job, §8.5).
package contracttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

// StubToken is the principal token the stub accepts.
const StubToken = "cap1.0.contract-test-token"

// questionStore is an in-memory question store serving the Slice B wire
// shape. It stands in for agent/api/questions.MemoryStore (server-side §1.9 —
// not landed in this slice; the kit must stay importable without it).
type questionStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]*platformmcp.Question
}

// ask is FIND-OR-CREATE, mirroring the §E DB-enforced idempotency-by-question
// (migration 1773106072): rows dedup on (pipeline_run_id, step_name, kind,
// question_hash) — the build id is deliberately NOT part of the key, and rows
// without a pipeline_run_id never dedup. An answered row is returned as-is
// (the resume fast path); an open row is joined.
func (s *questionStore) ask(q *platformmcp.Question) *platformmcp.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.PipelineRunID != nil {
		for _, row := range s.rows {
			if row.PipelineRunID != nil && *row.PipelineRunID == *q.PipelineRunID &&
				row.StepName == q.StepName && row.Kind == q.Kind &&
				dedupKey(row) == dedupKey(q) {
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

// dedupKey stands in for the server-side question_hash
// (sha256(question || '\x00' || options-joined-by-'\x00')).
func dedupKey(q *platformmcp.Question) string {
	return q.Question + "\x00" + strings.Join(q.Options, "\x00")
}

func (s *questionStore) get(id int) (platformmcp.Question, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.rows[id]
	if !ok {
		return platformmcp.Question{}, false
	}
	return *q, true
}

func (s *questionStore) answer(ticketID, id int, answer, answeredBy string) error {
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

func (s *questionStore) openForTicket(ticketID int) []platformmcp.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []platformmcp.Question
	for _, q := range s.rows {
		if q.TicketID == ticketID && q.AnsweredAt == 0 {
			open = append(open, *q)
		}
	}
	return open
}

// StubATC is a minimal ATC: ticket read/spec/plan/task routes plus the
// question routes over a memory store — the shape the real ATC exposes after
// the Slice B route registration. Any packaging can point at it (env
// ATC_EXTERNAL_URL).
type StubATC struct {
	t        *testing.T
	ticketID int
	store    *questionStore
	server   *httptest.Server

	mu           sync.Mutex
	specVersions int
	planVersions int
	taskUpdates  []map[string]any
}

func NewStubATC(t *testing.T, ticketID int) *StubATC {
	s := &StubATC{
		t: t, ticketID: ticketID,
		store: &questionStore{rows: map[int]*platformmcp.Question{}},
	}

	mux := http.NewServeMux()
	base := fmt.Sprintf("/api/v1/agent/tickets/%d", ticketID)
	withTicket := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+StubToken {
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

	mux.HandleFunc("GET "+base, withTicket(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ticket":{"id":%d,"title":"contract ticket","state":"running"},`+
			`"spec":{"title":"contract spec","acceptance_criteria":["ok"],"body_md":"body"},`+
			`"tasks":[{"ordering":1,"title":"first","status":"pending","detail_md":"do the thing"}]}`, ticketID)
	}))
	mux.HandleFunc("POST "+base+"/spec", withTicket(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.specVersions++
		version := s.specVersions
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":%d}`, version)
	}))
	mux.HandleFunc("POST "+base+"/plan", withTicket(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.planVersions++
		version := s.planVersions
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"plan_version":%d}`, version)
	}))
	mux.HandleFunc("PUT "+base+"/tasks/{ordering}", withTicket(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body == nil {
			body = map[string]any{}
		}
		body["ordering"] = r.PathValue("ordering")
		s.mu.Lock()
		s.taskUpdates = append(s.taskUpdates, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("POST "+base+"/questions", withTicket(func(w http.ResponseWriter, r *http.Request) {
		var q platformmcp.Question
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q.TicketID = ticketID
		if q.TimeoutPolicy == "" {
			q.TimeoutPolicy = "park"
		}
		created := s.store.ask(&q)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, created)
	}))
	mux.HandleFunc("GET "+base+"/questions/{question_id}", withTicket(func(w http.ResponseWriter, r *http.Request) {
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
			q, found := s.store.get(id)
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
	mux.HandleFunc("PUT "+base+"/questions/{question_id}/answer", withTicket(func(w http.ResponseWriter, r *http.Request) {
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
		if err := s.store.answer(ticketID, id, body.Answer, body.AnsweredBy); err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		q, _ := s.store.get(id)
		writeJSON(w, q)
	}))

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *StubATC) URL() string { return s.server.URL }

// SpecVersions reports how many spec submissions the stub has accepted.
func (s *StubATC) SpecVersions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specVersions
}

// PlanVersions reports how many plan submissions the stub has accepted.
func (s *StubATC) PlanVersions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planVersions
}

// TaskUpdates returns the task-status updates the stub has recorded.
func (s *StubATC) TaskUpdates() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.taskUpdates...)
}

// AutoAnswer answers every open question with the given answer after delay —
// the contract kit's stand-in human. The goroutine stops when the stub server
// closes (test cleanup).
func (s *StubATC) AutoAnswer(answer, by string, delay time.Duration) {
	go func() {
		for {
			time.Sleep(delay)
			for _, q := range s.store.openForTicket(s.ticketID) {
				_ = s.store.answer(s.ticketID, q.ID, answer, by)
			}
		}
	}()
}
