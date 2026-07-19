package contracttest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Run validates a serving platform-mcp endpoint (base URL WITHOUT /mcp)
// against the §3.2 contract. The endpoint must be configured against a
// StubATC with AutoAnswer enabled.
func Run(t *testing.T, endpointURL string) {
	t.Helper()

	rpc := func(method string, params any) map[string]any {
		t.Helper()
		var paramsJSON []byte
		if params != nil {
			paramsJSON, _ = json.Marshal(params)
		} else {
			paramsJSON = []byte("{}")
		}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, paramsJSON)
		resp, err := http.Post(endpointURL+"/mcp", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
		return out
	}

	callTool := func(name string, args any) (string, bool) {
		t.Helper()
		out := rpc("tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := out["result"].(map[string]any)
		if result == nil {
			t.Fatalf("tools/call %s: no result: %v", name, out)
		}
		content, _ := result["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("tools/call %s: empty content", name)
		}
		text := content[0].(map[string]any)["text"].(string)
		isError, _ := result["isError"].(bool)
		return text, isError
	}

	// healthz (§8.5 readiness contract).
	resp, err := http.Get(endpointURL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v / %v", err, resp)
	}
	resp.Body.Close()

	// initialize.
	init := rpc("initialize", map[string]any{})
	if init["result"] == nil {
		t.Fatalf("initialize failed: %v", init)
	}

	// tools/list: exactly the seven §3.2 tools.
	list := rpc("tools/list", nil)
	tools := list["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"read_ticket", "list_tasks", "get_task", "submit_spec", "submit_plan", "update_task_status", "ask_human"} {
		if !names[want] {
			t.Fatalf("tools/list missing %q (got %v)", want, names)
		}
	}
	if len(names) != 7 {
		t.Fatalf("expected exactly 7 tools, got %v", names)
	}

	// read_ticket returns envelope + spec but NOT tasks (§3.2 read model).
	text, isErr := callTool("read_ticket", map[string]any{})
	if isErr || !bytes.Contains([]byte(text), []byte(`"ticket"`)) || !bytes.Contains([]byte(text), []byte(`"spec"`)) {
		t.Fatalf("read_ticket: isErr=%v %s", isErr, text)
	}
	if bytes.Contains([]byte(text), []byte(`"tasks"`)) {
		t.Fatalf("read_ticket must not include tasks: %s", text)
	}

	// list_tasks returns the skeleton; get_task returns detail; unknown ordering errors.
	text, isErr = callTool("list_tasks", map[string]any{})
	if isErr || !bytes.Contains([]byte(text), []byte(`"ordering"`)) {
		t.Fatalf("list_tasks: isErr=%v %s", isErr, text)
	}
	if bytes.Contains([]byte(text), []byte(`"detail_md"`)) {
		t.Fatalf("list_tasks must omit detail_md: %s", text)
	}
	text, isErr = callTool("get_task", map[string]any{"ordering": 1})
	if isErr || !bytes.Contains([]byte(text), []byte(`"detail_md"`)) {
		t.Fatalf("get_task: isErr=%v %s", isErr, text)
	}
	// Unknown ordering is an MCP tool error (isError=true), NOT a JSON-RPC -32602
	// error object — the shared mcpserver maps handler errors to isError results.
	unknownOrdering := rpc("tools/call", map[string]any{"name": "get_task", "arguments": map[string]any{"ordering": 999}})
	if unknownOrdering["error"] != nil {
		t.Fatalf("get_task unknown ordering must be a tool error, not a JSON-RPC error object: %v", unknownOrdering["error"])
	}
	if result, _ := unknownOrdering["result"].(map[string]any); result == nil || result["isError"] != true {
		t.Fatalf("get_task must reject an unknown ordering with a tool error (isError=true): %v", unknownOrdering)
	}

	// submit_spec: input error without title, version on success.
	if _, isErr := callTool("submit_spec", map[string]any{"body": "b"}); !isErr {
		t.Fatal("submit_spec accepted missing title")
	}
	text, isErr = callTool("submit_spec", map[string]any{"title": "t", "body": "b"})
	if isErr || !bytes.Contains([]byte(text), []byte(`"version"`)) {
		t.Fatalf("submit_spec: isErr=%v %s", isErr, text)
	}

	// submit_plan: minItems enforced, plan_version returned.
	if _, isErr := callTool("submit_plan", map[string]any{"tasks": []any{}}); !isErr {
		t.Fatal("submit_plan accepted empty tasks")
	}
	text, isErr = callTool("submit_plan", map[string]any{"tasks": []map[string]string{{"title": "a"}}})
	if isErr || !bytes.Contains([]byte(text), []byte(`"plan_version"`)) {
		t.Fatalf("submit_plan: isErr=%v %s", isErr, text)
	}

	// update_task_status: enum enforced, ok returned.
	if _, isErr := callTool("update_task_status", map[string]any{"ordering": 1, "status": "bogus"}); !isErr {
		t.Fatal("update_task_status accepted bogus status")
	}
	text, isErr = callTool("update_task_status", map[string]any{"ordering": 1, "status": "done"})
	if isErr {
		t.Fatalf("update_task_status: %s", text)
	}

	// ask_human parks then resumes via the stub's auto-answer.
	start := time.Now()
	text, isErr = callTool("ask_human", map[string]any{"question": "contract check?"})
	if isErr {
		t.Fatalf("ask_human: %s", text)
	}
	var answer struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
		TimedOut   bool   `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("ask_human result: %v: %s", err, text)
	}
	if answer.Answer == "" || answer.TimedOut {
		t.Fatalf("ask_human did not resume with a real answer: %+v (waited %s)", answer, time.Since(start))
	}
}

// RunSSEHeartbeats validates the D4 MUST-stream contract on ask_human
// (2026-07-09 SSE seam delta): a progress-opted tools/call that parks longer
// than the heartbeat interval must yield >= 2 notifications/progress frames
// spaced < 30s apart BEFORE the final result frame, with the progressToken
// echoed verbatim. The endpoint must be configured with a sub-second
// ProgressInterval and an AutoAnswer delay of at least ~5x that interval.
func RunSSEHeartbeats(t *testing.T, endpointURL string) {
	t.Helper()

	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_human","arguments":{"question":"sse heartbeat check?"},"_meta":{"progressToken":"hb-1"}}}`
	req, err := http.NewRequest(http.MethodPost, endpointURL+"/mcp", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ask_human SSE: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	var progressTimes []time.Time
	var finalSeen bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if strings.Contains(payload, `"notifications/progress"`) {
			if finalSeen {
				t.Fatal("progress frame AFTER the final result frame")
			}
			if !strings.Contains(payload, `"hb-1"`) {
				t.Fatalf("progress frame missing the echoed progressToken: %s", payload)
			}
			progressTimes = append(progressTimes, time.Now())
			continue
		}
		if strings.Contains(payload, `"id":9`) {
			finalSeen = true
			if !strings.Contains(payload, "answer") {
				t.Fatalf("final frame missing the tool result: %s", payload)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}
	if !finalSeen {
		t.Fatal("never saw the final JSON-RPC response frame (it must be the LAST SSE frame)")
	}
	if len(progressTimes) < 2 {
		t.Fatalf("expected >= 2 progress heartbeats before the answer, got %d", len(progressTimes))
	}
	for i := 1; i < len(progressTimes); i++ {
		if gap := progressTimes[i].Sub(progressTimes[i-1]); gap >= 30*time.Second {
			t.Fatalf("heartbeat gap %s >= 30s (contracts §3.1 progress mandate)", gap)
		}
	}
}
