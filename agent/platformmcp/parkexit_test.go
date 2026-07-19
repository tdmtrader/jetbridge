package platformmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

// newParkExitServer is newAskServer plus a pipeline run id (the §E dedup key
// scope), a tiny short-park threshold, and a sentinel path in a temp dir.
func newParkExitServer(t *testing.T, atcURL string, threshold time.Duration) (*platformmcp.Server, string) {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		PipelineRunID:  7,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)
	parkPath := filepath.Join(t.TempDir(), "park.json")
	srv.TuneShortPark(threshold, parkPath)
	return srv, parkPath
}

// TestAskHumanWritesParkSentinelAtThreshold (PARK-V2 §B1/§B3): when a park
// crosses the threshold the sidecar atomically writes flight/park.json with
// the frozen payload and KEEPS the question row open; the parked call is then
// unblocked by the CLIENT DISCONNECT (the runner SIGTERMs claude) — and that
// disconnect must NOT resolve the row: it is the durable representation of
// the wait.
func TestAskHumanWritesParkSentinelAtThreshold(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv, parkPath := newParkExitServer(t, atc.URL, 200*time.Millisecond)

	sidecar := httptest.NewServer(srv.Mux())
	t.Cleanup(sidecar.Close)

	// Park via a real connection so cancelling the request context actually
	// severs it (the disconnect leg of §B3).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"long wait?"}}}`
	req, err := http.NewRequestWithContext(ctx, "POST", sidecar.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	// The sentinel appears once the threshold crosses.
	var raw []byte
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err = os.ReadFile(parkPath)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("park sentinel never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var sentinel struct {
		QuestionID       int    `json:"question_id"`
		Kind             string `json:"kind"`
		StepName         string `json:"step_name"`
		AskedAt          string `json:"asked_at"`
		ThresholdSeconds int    `json:"threshold_seconds"`
		CrossedAt        string `json:"crossed_at"`
	}
	if err := json.Unmarshal(raw, &sentinel); err != nil {
		t.Fatalf("park.json is not valid JSON (atomic write violated?): %v: %q", err, raw)
	}
	open := store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("expected the question to STAY OPEN at park-exit, got %d open", len(open))
	}
	if sentinel.QuestionID != open[0].ID || sentinel.Kind != "question" || sentinel.StepName != "implement" {
		t.Fatalf("unexpected sentinel: %+v", sentinel)
	}
	for _, ts := range []string{sentinel.AskedAt, sentinel.CrossedAt} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("sentinel timestamp %q is not RFC3339: %v", ts, err)
		}
	}
	if _, err := os.Stat(parkPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind — the write must be tmp + rename")
	}

	// The runner now kills claude; the connection drops. The parked call must
	// return WITHOUT resolving the row.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parked ask_human did not return on client disconnect")
	}
	open = store.OpenForTicket(42)
	if len(open) != 1 {
		t.Fatalf("client disconnect must NOT resolve the row; got %d open", len(open))
	}
}

// TestAskHumanAnsweredRowFastPath (PARK-V2 §E): the continuation build's
// re-issued ask_human hits find-or-create, gets the already-answered row, and
// returns IMMEDIATELY — no park, no SSE wait, no threshold timer.
func TestAskHumanAnsweredRowFastPath(t *testing.T) {
	store := newStubQuestionStore()
	atc := fullStubATC(t, store)
	srv, parkPath := newParkExitServer(t, atc.URL, time.Hour)

	// The row the PREVIOUS execution filed and a human answered while the
	// step was exited: same (pipeline_run_id, step_name, kind, hash) — the
	// build id differs and is deliberately NOT part of the key.
	runID := 7
	seeded := store.Ask(&platformmcp.Question{
		TicketID: 42, PipelineRunID: &runID, BuildID: 900, StepName: "implement",
		Kind: "question", Question: "resume me?",
		Options: []string{"go", "stop"},
	})
	if err := store.Answer(42, seeded.ID, "go", "tdm"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result, isErr := callTool(t, srv.Mux(), "ask_human", map[string]any{
		"question": "resume me?",
		"options":  []string{"go", "stop"},
	})
	if isErr {
		t.Fatalf("ask_human errored: %s", result)
	}
	if !strings.Contains(string(result), `"answer":"go"`) ||
		!strings.Contains(string(result), `"answered_by":"tdm"`) {
		t.Fatalf("expected the stored answer immediately, got: %s", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fast path took %s — it must not park", elapsed)
	}
	if _, err := os.Stat(parkPath); !os.IsNotExist(err) {
		t.Fatal("fast path must not arm the park-exit timer / write a sentinel")
	}
}
