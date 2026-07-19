package platformmcp_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

func checkpointRoundTrip(t *testing.T, answer string) (map[string]any, *stubQuestionStore) {
	t.Helper()
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	go func() {
		for i := 0; i < 100; i++ {
			open := store.OpenForTicket(42)
			if len(open) == 1 {
				if open[0].Kind != "checkpoint" {
					t.Errorf("expected checkpoint kind, got %q", open[0].Kind)
				}
				if len(open[0].Options) != 2 || open[0].Options[0] != "approve" || open[0].Options[1] != "reject" {
					t.Errorf("expected approve/reject options, got %v", open[0].Options)
				}
				_ = store.Answer(42, open[0].ID, answer, "tdm")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the submitted plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("checkpoint endpoint: %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, store
}

func TestCheckpointApproved(t *testing.T) {
	out, _ := checkpointRoundTrip(t, "approve")
	if out["approved"] != true || out["answered_by"] != "tdm" {
		t.Fatalf("unexpected checkpoint result: %v", out)
	}
}

func TestCheckpointRejected(t *testing.T) {
	out, _ := checkpointRoundTrip(t, "reject")
	if out["approved"] != false {
		t.Fatalf("unexpected checkpoint result: %v", out)
	}
}

// TestCheckpointConcurrentDedup asserts the per-name dedup guard: two
// simultaneous POSTs for the same checkpoint name (the client-restart-mid-park
// case) must file exactly ONE agent_run_questions row, and both POSTs must
// return the same resolved answer once a human answers that single row.
func TestCheckpointConcurrentDedup(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)

	// Answer the single open checkpoint once it appears, and record the peak
	// number of simultaneously-open rows — the guard must keep it at 1.
	maxOpen := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			open := store.OpenForTicket(42)
			if len(open) > maxOpen {
				maxOpen = len(open)
			}
			if len(open) >= 1 {
				_ = store.Answer(42, open[0].ID, "approve", "tdm")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	post := func() (int, map[string]any) {
		req := httptest.NewRequest("POST", "/checkpoint",
			strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
		w := httptest.NewRecorder()
		srv.Mux().ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	type result struct {
		code int
		out  map[string]any
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			code, out := post()
			results <- result{code, out}
		}()
	}

	for i := 0; i < 2; i++ {
		r := <-results
		if r.code != 200 {
			t.Fatalf("checkpoint POST %d: expected 200, got %d", i, r.code)
		}
		if r.out["approved"] != true || r.out["answered_by"] != "tdm" {
			t.Fatalf("checkpoint POST %d: unexpected result: %v", i, r.out)
		}
	}
	<-done

	// Exactly one row filed across both concurrent POSTs.
	all := store.ListForTicket(42, 50)
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 checkpoint row, got %d: %+v", len(all), all)
	}
	if maxOpen > 1 {
		t.Fatalf("dedup guard failed: %d rows open simultaneously", maxOpen)
	}
}

// TestCheckpointParksPastThreshold (PARK-V2 §B4): with no human answer, the
// blocked POST gets 202 {"parked": true} once the short-park threshold
// crosses, and the row STAYS OPEN — the checkpoint client turns this into
// frozen exit code 3.
func TestCheckpointParksPastThreshold(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)
	srv.TuneShortPark(200*time.Millisecond, "")

	start := time.Now()
	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("expected 202 at threshold, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out["parked"] {
		t.Fatalf("expected {\"parked\": true}, got %s (err %v)", w.Body.String(), err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("threshold response took %s", elapsed)
	}
	open := store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("the parked checkpoint row must STAY OPEN, got %d open", len(open))
	}
}

// TestCheckpointRePostAfterAnswerResolvesImmediately (PARK-V2 §E): the
// continuation pod's FRESH sidecar (empty ckOpen map) re-POSTs the same
// checkpoint; the DB-level find-or-create returns the answered row and the
// response is an immediate 200 — the client exits 0/1 with no park and no
// second row.
func TestCheckpointRePostAfterAnswerResolvesImmediately(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv, _ := newParkExitServer(t, atc.URL, time.Hour)

	// The row the PREVIOUS pod's client filed and a human approved while the
	// step was exited: same (pipeline_run_id, step_name, kind, hash).
	runID := 7
	seeded := store.Ask(&platformmcp.Question{
		TicketID: 42, PipelineRunID: &runID, StepName: "implement",
		Kind: "checkpoint", Question: "Approve the plan",
		Options: []string{"approve", "reject"}, TimeoutPolicy: "park",
	})
	if err := store.Answer(42, seeded.ID, "approve", "tdm"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	req := httptest.NewRequest("POST", "/checkpoint",
		strings.NewReader(`{"name": "plan-approval", "description": "Approve the plan"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected immediate 200 on the answered row, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["approved"] != true || out["answered_by"] != "tdm" {
		t.Fatalf("unexpected result: %v", out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fast path took %s — it must not park", elapsed)
	}
	all := store.ListForTicket(42, 50)
	if len(all) != 1 {
		t.Fatalf("re-POST must NOT file a second row, got %d", len(all))
	}
}

func TestCheckpointRequiresName(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv := newAskServer(t, atc.URL, "park", 0)
	req := httptest.NewRequest("POST", "/checkpoint", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 without name, got %d", w.Code)
	}
}
