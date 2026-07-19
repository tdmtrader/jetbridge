//go:build live
// +build live

package mcpserver_test

// Transport-level precursor of plan 08 Task 18b's TestLiveCLIParkPin (the
// wave-3 entry gate). The full pin drives the REAL platform-mcp sidecar;
// that sidecar is not built yet, but the gate's empirical question — does
// the real claude CLI hold a >5-minute SSE tools/call with ~15s progress
// heartbeats instead of abandoning at 60s (F13) — is a property of the CLI
// against THIS package's transport, which is merged (ticket #26). This test
// pins that property now; Task 18b re-pins it through the sidecar when
// agent/platformmcp exists, using the same stub model and assertions.
//
// Run (any host with the claude CLI on PATH; hermetic, no cluster, no API):
//
//	go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m \
//	  ./atc/api/mcpserver/
//
// claude CLI pinned during F13: >= 2.1.77. This run: see the version log line.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

// sseRecorder tees the server's responses and timestamps every
// notifications/progress frame — the CLI consumes the stream, so cadence is
// observed at the wire, between CLI and server.
type sseRecorder struct {
	mu    sync.Mutex
	times []time.Time
	next  http.Handler
}

func (rec *sseRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.next.ServeHTTP(&recordingWriter{ResponseWriter: w, rec: rec}, r)
}

type recordingWriter struct {
	http.ResponseWriter
	rec *sseRecorder
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("notifications/progress")) {
		w.rec.mu.Lock()
		w.rec.times = append(w.rec.times, time.Now())
		w.rec.mu.Unlock()
	}
	return w.ResponseWriter.Write(p)
}

func (w *recordingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// anthropicStub is the hermetic model: turn 1 (no tool_result in the request)
// scripts one ask_human tool_use; turn 2 echoes the tool result as text.
func anthropicStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if !strings.Contains(body, "tool_result") {
			writeAnthropicEvents(w,
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"stub","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__platform__ask_human","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"question\":\"Live park pin: proceed?\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
				`{"type":"message_stop"}`,
			)
			return
		}

		echoJSON, _ := json.Marshal("TOOL RESULT: " + extractToolResult(body))
		writeAnthropicEvents(w,
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"stub","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, echoJSON),
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeAnthropicEvents(w http.ResponseWriter, events ...string) {
	f, _ := w.(http.Flusher)
	for _, data := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(data), &probe)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, data)
		if f != nil {
			f.Flush()
		}
	}
}

func extractToolResult(body string) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var blocks []map[string]any
		if err := json.Unmarshal(req.Messages[i].Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b["type"] == "tool_result" {
				out, _ := json.Marshal(b["content"])
				return string(out)
			}
		}
	}
	return ""
}

// TestLiveCLIParkPin: the REAL claude CLI parks on ask_human for > 5 minutes
// against the merged SSE transport and MUST receive the answer, with ~15s
// progress heartbeats observed at the wire throughout the park (F13/D7).
func TestLiveCLIParkPin(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not on PATH — the F13 pin requires the real CLI (>= 2.1.77)")
	}
	if out, verr := exec.Command(claudeBin, "--version").CombinedOutput(); verr == nil {
		t.Logf("claude CLI: %s (need >= 2.1.77 — the version the F13 experiment pinned)", strings.TrimSpace(string(out)))
	}

	const parkFor = 5*time.Minute + 30*time.Second // answered at t+5m30s: > 5 minutes parked

	// The merged SSE server with the frozen 15s default heartbeat, exposing
	// a stub ask_human whose handler BLOCKS parkFor — the transport-level
	// equivalent of the sidecar's parked long-poll.
	srv := mcpserver.NewServer()
	srv.AddTool("ask_human", "ask the human a question and wait for the answer",
		json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		func(ctx context.Context, args json.RawMessage, progress func(string)) (any, error) {
			progress("parked: waiting for a human answer")
			select {
			case <-time.After(parkFor):
				return "push on", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})

	recorder := &sseRecorder{next: srv}
	sidecar := httptest.NewServer(recorder)
	defer sidecar.Close()

	// Hermetic model + MCP config pointing ONLY at the transport under test.
	model := anthropicStub(t)
	mcpConfig := filepath.Join(t.TempDir(), "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"platform":{"type":"http","url":"%s"}}}`, sidecar.URL)
	if err := os.WriteFile(mcpConfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin,
		"-p", "Call the ask_human tool once, then repeat its answer verbatim.",
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		// Headless MCP tool calls hit the permission wall without this (the
		// F25 lesson; the in-pod agent-runner passes the same flag) — plan
		// 08 Task 18b's command text omits it, recorded as a plan delta.
		"--dangerously-skip-permissions",
		"--output-format", "text",
	)
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+model.URL,
		"ANTHROPIC_API_KEY=live-pin-dummy",
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	transcript := string(out)
	t.Logf("claude ran %s; transcript:\n%s", elapsed, transcript)
	if err != nil {
		t.Fatalf("claude CLI failed: %v", err)
	}

	// It really parked > 5 minutes.
	if elapsed < 5*time.Minute {
		t.Fatalf("CLI returned after %s — it cannot have parked > 5 minutes", elapsed)
	}
	// The answer made it back through the > 5-minute park.
	if !strings.Contains(transcript, "push on") {
		t.Fatalf("transcript missing the parked answer %q", "push on")
	}
	// The F13 failure signature must NOT appear.
	if strings.Contains(transcript, "(completed with no output)") {
		t.Fatal("F13 REGRESSION: the CLI silently abandoned the parked tools/call")
	}
	// ~15s heartbeat cadence throughout the park, observed at the wire.
	recorder.mu.Lock()
	times := append([]time.Time(nil), recorder.times...)
	recorder.mu.Unlock()
	if len(times) < 15 { // 5m30s / 15s ≈ 22 frames; allow scheduling slack
		t.Fatalf("expected >= 15 progress frames across the park, got %d", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap >= 30*time.Second {
			t.Fatalf("heartbeat gap %s >= 30s at frame %d (contracts §3.1)", gap, i)
		}
	}
	t.Logf("park pin: %d progress frames over %s, answer delivered", len(times), elapsed)
}
