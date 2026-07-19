package platformmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/platformmcp"
)

// callTool posts a JSON-RPC tools/call and returns (resultJSON, isError).
// mcpserver wraps tool results as {content: [{type: "text", text: <json>}]}.
func callTool(t *testing.T, h http.Handler, tool string, args any) (json.RawMessage, bool) {
	t.Helper()
	argsRaw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		tool, argsRaw)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call %s: HTTP %d: %s", tool, w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc response: %v: %s", err, w.Body.String())
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("empty content for %s: %s", tool, w.Body.String())
	}
	return json.RawMessage(resp.Result.Content[0].Text), resp.Result.IsError
}

// stubTicketATC fakes ticket-core's routes for ticket 42, serving the LANDED
// wire shapes: TicketDetail{ticket, spec, tasks} where each task carries its
// markdown body under "detail" (the §2.1 tickets.Task JSON tag) and the spec
// body under "body".
func stubTicketATC(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	recorded := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agent/tickets/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"fix flaky test","state":"running"},`+
			`"spec":{"title":"Fix flake","acceptance_criteria":["green 10x"],"body":"## Rationale"},`+
			`"tasks":[`+
			`{"ordering":1,"title":"write failing test","status":"done","detail":"repro the flake"},`+
			`{"ordering":2,"title":"fix","status":"pending","detail":"see spec"}]}`)
	})
	mux.HandleFunc("POST /api/v1/agent/tickets/42/spec", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["spec"] = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"version":3}`)
	})
	mux.HandleFunc("POST /api/v1/agent/tickets/42/plan", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["plan"] = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"plan_version":2}`)
	})
	mux.HandleFunc("PUT /api/v1/agent/tickets/42/tasks/3", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		recorded["task"] = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &recorded
}

func newTestServer(t *testing.T, atcURL string) *platformmcp.Server {
	t.Helper()
	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         atcURL,
		PrincipalToken: "cap1.9.secret",
		TicketID:       42,
		BuildID:        1001,
		StepName:       "implement",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// Ticket #37 delta over plan 08 Task 10: ask_human is the NEXT ticket
// (T14-15) — exactly the six ticket/task tools are registered here.
func TestToolsListExposesExactlySixTools(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range resp.Result.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"read_ticket", "list_tasks", "get_task", "submit_spec", "submit_plan", "update_task_status"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestReadTicketReturnsEnvelopeAndSpecOnly(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "read_ticket", map[string]any{})
	if isErr {
		t.Fatalf("read_ticket errored: %s", result)
	}
	var out struct {
		Ticket struct {
			ID    int    `json:"id"`
			Title string `json:"title"`
		} `json:"ticket"`
		Spec *struct {
			Title string `json:"title"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ticket.ID != 42 || out.Ticket.Title != "fix flaky test" {
		t.Fatalf("unexpected ticket: %+v", out)
	}
	if out.Spec == nil || out.Spec.Title != "Fix flake" {
		t.Fatalf("expected spec embedded, got %+v", out.Spec)
	}
	// tasks MUST NOT appear in read_ticket's result (§3.2 read model).
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(result, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["tasks"]; ok {
		t.Fatalf("read_ticket must not include tasks, got %s", result)
	}
}

func TestListTasksReturnsSkeletonWithoutDetail(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "list_tasks", map[string]any{})
	if isErr {
		t.Fatalf("list_tasks errored: %s", result)
	}
	var out struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %+v", out.Tasks)
	}
	for _, task := range out.Tasks {
		if _, ok := task["detail_md"]; ok {
			t.Fatalf("list_tasks must omit detail_md, got %v", task)
		}
		if _, ok := task["detail"]; ok {
			t.Fatalf("list_tasks must omit detail, got %v", task)
		}
		for _, key := range []string{"ordering", "title", "status"} {
			if _, ok := task[key]; !ok {
				t.Fatalf("list_tasks task missing %q: %v", key, task)
			}
		}
	}
}

func TestGetTaskReturnsDetailAndErrorsOnUnknownOrdering(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "get_task", map[string]any{"ordering": 2})
	if isErr {
		t.Fatalf("get_task errored: %s", result)
	}
	var task struct {
		Ordering int    `json:"ordering"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		DetailMD string `json:"detail_md"`
	}
	if err := json.Unmarshal(result, &task); err != nil {
		t.Fatal(err)
	}
	if task.Ordering != 2 || task.Title != "fix" || task.Status != "pending" || task.DetailMD != "see spec" {
		t.Fatalf("unexpected task: %+v", task)
	}

	// Unknown ordering is an MCP tool error (isError=true) — the shared mcpserver
	// maps the handler's returned error to a tools/call result with isError=true,
	// NOT a JSON-RPC -32602 error object. Assert both the isError flag AND that the
	// tool-result content names the ordering, and that no top-level JSON-RPC error
	// (which -32602 would carry) is present.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_task","arguments":{"ordering":99}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unknown-ordering call: HTTP %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc response: %v: %s", err, w.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unknown ordering must be a tool error, not a JSON-RPC error object (got code %d)", resp.Error.Code)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected tool result isError=true for unknown ordering, got %s", w.Body.String())
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "99") {
		t.Fatalf("expected error content naming the unknown ordering, got %s", w.Body.String())
	}
}

// E5 (contracts §3.2, 2026-07-09 amendment): a ticket with no active plan is
// a SUCCESSFUL empty list from list_tasks, but get_task addressing it is an
// MCP tool error naming the absent plan.
func TestPlanlessTicketListsEmptyAndGetTaskErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agent/tickets/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ticket":{"id":42,"title":"plan-less","state":"running"},"spec":null,"tasks":[]}`)
	})
	atc := httptest.NewServer(mux)
	t.Cleanup(atc.Close)
	srv := newTestServer(t, atc.URL)

	result, isErr := callTool(t, srv.Mux(), "list_tasks", map[string]any{})
	if isErr {
		t.Fatalf("list_tasks on plan-less ticket must succeed, got error: %s", result)
	}
	var out struct {
		Tasks []any `json:"tasks"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Tasks == nil || len(out.Tasks) != 0 {
		t.Fatalf(`expected {"tasks": []}, got %s`, result)
	}

	errText, isErr := callTool(t, srv.Mux(), "get_task", map[string]any{"ordering": 1})
	if !isErr {
		t.Fatalf("get_task on plan-less ticket must be a tool error, got %s", errText)
	}
	if !strings.Contains(string(errText), "no active plan for ticket 42") {
		t.Fatalf("expected error naming the absent plan, got %s", errText)
	}
}

func TestSubmitSpecValidatesAndForwards(t *testing.T) {
	atc, recorded := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	if result, isErr := callTool(t, srv.Mux(), "submit_spec", map[string]any{"body": "no title"}); !isErr {
		t.Fatalf("expected input error, got %s", result)
	}

	result, isErr := callTool(t, srv.Mux(), "submit_spec", map[string]any{
		"title":               "Fix the flaky spec",
		"body":                "## Rationale\n...",
		"acceptance_criteria": []string{"suite green 10x"},
	})
	if isErr {
		t.Fatalf("submit_spec errored: %s", result)
	}
	var out struct {
		Version int `json:"version"`
	}
	json.Unmarshal(result, &out)
	if out.Version != 3 {
		t.Fatalf("expected version 3, got %+v", out)
	}
	spec := (*recorded)["spec"].(map[string]any)
	if spec["title"] != "Fix the flaky spec" {
		t.Fatalf("spec not forwarded: %v", spec)
	}
}

func TestSubmitPlanAndUpdateTaskStatus(t *testing.T) {
	atc, recorded := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)

	if result, isErr := callTool(t, srv.Mux(), "submit_plan", map[string]any{"tasks": []any{}}); !isErr {
		t.Fatalf("expected minItems error, got %s", result)
	}

	result, isErr := callTool(t, srv.Mux(), "submit_plan", map[string]any{
		"tasks": []map[string]string{{"title": "write failing test"}, {"title": "fix", "detail": "see spec"}},
	})
	if isErr {
		t.Fatalf("submit_plan errored: %s", result)
	}
	var planOut struct {
		PlanVersion int `json:"plan_version"`
	}
	json.Unmarshal(result, &planOut)
	if planOut.PlanVersion != 2 {
		t.Fatalf("expected plan_version 2, got %+v", planOut)
	}

	if result, isErr := callTool(t, srv.Mux(), "update_task_status",
		map[string]any{"ordering": 3, "status": "nope"}); !isErr {
		t.Fatalf("expected status enum error, got %s", result)
	}
	result, isErr = callTool(t, srv.Mux(), "update_task_status",
		map[string]any{"ordering": 3, "status": "done", "note": "all green"})
	if isErr {
		t.Fatalf("update_task_status errored: %s", result)
	}
	task := (*recorded)["task"].(map[string]any)
	if task["status"] != "done" || task["note"] != "all green" {
		t.Fatalf("task update not forwarded: %v", task)
	}
}

func TestHealthz(t *testing.T) {
	atc, _ := stubTicketATC(t)
	srv := newTestServer(t, atc.URL)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("healthz: %d", w.Code)
	}
}
