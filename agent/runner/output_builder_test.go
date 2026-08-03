package runner

import (
	"context"
	"encoding/json"
	"github.com/concourse/concourse/agent/provider"
	schema "github.com/concourse/concourse/agent/schema"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type outputBuilderSession func(context.Context) (provider.Result, error)

func (s outputBuilderSession) Wait(ctx context.Context) (provider.Result, error) { return s(ctx) }

func TestRunManagedOutputBuilderPreflightBlocksBrokenServerBeforeClaude(t *testing.T) {
	listener, err := net.Listen("tcp", DefaultMCPAddressForTest())
	if err != nil {
		t.Skipf("managed test port unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "broken", http.StatusNotImplemented)
	})}
	defer server.Close()
	go server.Serve(listener)
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	starts := 0
	adapter := &provider.FakeAdapter{IdentityValue: provider.Identity{Name: "test", Version: "1"}, StartFunc: func(context.Context, provider.StartRequest, provider.BoundaryControl) (provider.RunningSession, error) {
		starts++
		return nil, nil
	}}
	exit, err := Run(context.Background(), Config{Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", OutputBuilderMarker: "1", Adapter: adapter})
	if err != nil || exit != 2 {
		t.Fatalf("run=%d,%v", exit, err)
	}
	if starts != 0 {
		t.Fatalf("provider starts=%d", starts)
	}
	var results schema.Results
	if err := json.Unmarshal(mustRead(t, filepath.Join(flight, "results.json")), &results); err != nil || results.Status != schema.StatusError || results.Validate() != nil || !strings.Contains(results.Summary, "managed output builder protocol preflight failed") || strings.Contains(results.Summary, "broken") {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	events := readOutputEvents(t, flight)
	if len(events) != 3 || events[0].Type != schema.EventStepStart || events[1].Type != schema.EventError || events[2].Type != schema.EventStepEnd {
		t.Fatalf("events=%#v", events)
	}
	var end schema.StepEndData
	if json.Unmarshal(events[2].Data, &end) != nil || end.Status != schema.RunStatusError || strings.Contains(end.Summary, "broken") {
		t.Fatalf("end=%#v", end)
	}
}

func TestRunManagedOutputBuilderDoesNotEmitReadyForRunnerOnlyPreflight(t *testing.T) {
	var methods []string
	listener, err := net.Listen("tcp", DefaultMCPAddressForTest())
	if err != nil {
		t.Skipf("managed test port unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		var q struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		methods = append(methods, q.Method)
		if q.Method == "notifications/initialized" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "initialize" {
			w.Write([]byte(`{"result":{"protocolVersion":"2024-11-05"}}`))
			return
		}
		w.Write([]byte(`{"result":{"tools":[{"name":"describe_output","inputSchema":{"type":"object"}},{"name":"validate_output","inputSchema":{"type":"object"}},{"name":"write_output","inputSchema":{"type":"object"}}]}}`))
	})}
	defer server.Close()
	go server.Serve(listener)
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	claude := filepath.Join(dir, "claude")
	claudeBody := "#!/bin/sh\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"init\",\"mcp_servers\":[{\"name\":\"output-builder\",\"status\":\"pending\"}]}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"model\":\"m\",\"cost_usd\":0,\"num_turns\":1,\"usage\":{}}'\n"
	if err := os.WriteFile(claude, []byte(claudeBody), 0o755); err != nil {
		t.Fatal(err)
	}
	exit, err := Run(context.Background(), Config{Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude, OutputBuilderMarker: "1"})
	if err != nil || exit != 0 {
		t.Fatalf("run=%d,%v", exit, err)
	}
	if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" {
		t.Fatalf("methods=%v", methods)
	}
	events := string(mustRead(t, filepath.Join(flight, "events.ndjson")))
	if strings.Contains(events, "mcp.ready") {
		t.Fatalf("runner-only preflight claimed provider readiness: %s", events)
	}
}

func TestRunManagedOutputBuilderEmitsReadyAfterHyphenatedProviderToolUse(t *testing.T) {
	var methods []string
	listener, err := net.Listen("tcp", DefaultMCPAddressForTest())
	if err != nil {
		t.Skip(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		var q struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		methods = append(methods, q.Method)
		if q.Method == "notifications/initialized" {
			w.WriteHeader(204)
			return
		}
		if q.Method == "initialize" {
			w.Write([]byte(`{"result":{"protocolVersion":"2024-11-05"}}`))
			return
		}
		w.Write([]byte(`{"result":{"tools":[{"name":"describe_output","inputSchema":{"type":"object"}},{"name":"validate_output","inputSchema":{"type":"object"}},{"name":"write_output","inputSchema":{"type":"object"}}]}}`))
	})}
	defer server.Close()
	go server.Serve(listener)
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	adapter := &provider.FakeAdapter{IdentityValue: provider.Identity{Name: "test", Version: "1"}, StartFunc: func(context.Context, provider.StartRequest, provider.BoundaryControl) (provider.RunningSession, error) {
		return outputBuilderSession(func(context.Context) (provider.Result, error) {
			return provider.Result{Stream: []byte(
				`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__describe_output"}]}}` + "\n" +
					`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__validate_output"}]}}` + "\n" +
					`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__write_output"}]}}` + "\n" +
					`{"type":"result","subtype":"success","result":"done","is_error":false}` + "\n",
			)}, nil
		}), nil
	}}
	exit, err := Run(context.Background(), Config{Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", OutputBuilderMarker: "1", Adapter: adapter})
	if err != nil || exit != 0 {
		t.Fatalf("run=%d,%v", exit, err)
	}
	if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" {
		t.Fatal(methods)
	}
	events := string(mustRead(t, filepath.Join(flight, "events.ndjson")))
	if strings.Count(events, "mcp.ready") != 1 {
		t.Fatalf("provider-visible readiness count is not one: %s", events)
	}
}

func TestManagedMCPReadyFromProviderStream(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   bool
	}{
		{name: "malformed line", stream: "{\n", want: false},
		{name: "another server", stream: `{"type":"system","subtype":"init","mcp_servers":[{"name":"other","status":"connected"}]}` + "\n", want: false},
		{name: "pending", stream: `{"type":"system","subtype":"init","mcp_servers":[{"name":"output-builder","status":"pending"}]}` + "\n", want: false},
		{name: "failed", stream: `{"type":"system","subtype":"init","mcp_servers":[{"name":"output-builder","status":"failed"}]}` + "\n", want: false},
		{name: "oversized line", stream: strings.Repeat(" ", int(managedOutputBuilderResponseLimit)+1) + "\n", want: false},
		{name: "connected", stream: `{"type":"system","subtype":"init","mcp_servers":[{"name":"output-builder","status":"connected"}]}` + "\n", want: true},
		{name: "managed describe tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output_builder__describe_output"}]}}` + "\n", want: true},
		{name: "managed validate tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output_builder__validate_output"}]}}` + "\n", want: true},
		{name: "managed write tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output_builder__write_output"}]}}` + "\n", want: true},
		{name: "hyphenated managed describe tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__describe_output"}]}}` + "\n", want: true},
		{name: "hyphenated managed validate tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__validate_output"}]}}` + "\n", want: true},
		{name: "hyphenated managed write tool use", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__write_output"}]}}` + "\n", want: true},
		{name: "forged managed tool name", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output_builder__forged"}]}}` + "\n", want: false},
		{name: "forged hyphenated managed tool name", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__output-builder__forged"}]}}` + "\n", want: false},
		{name: "other server tool name", stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__other__describe_output"}]}}` + "\n", want: false},
		{name: "tool name without tool use", stream: `{"type":"assistant","message":{"content":[{"type":"text","name":"mcp__output_builder__write_output"}]}}` + "\n", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := managedMCPReadyFromProviderStream([]byte(test.stream), "output-builder"); got != test.want {
				t.Fatalf("ready=%v, want %v", got, test.want)
			}
		})
	}
}

func readOutputEvents(t *testing.T, flight string) []*schema.Event {
	f, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := schema.NewEventReader(f)
	var events []*schema.Event
	for {
		e, err := r.Read()
		if err != nil {
			break
		}
		events = append(events, e)
	}
	return events
}

func DefaultMCPAddressForTest() string { return "127.0.0.1:7783" }
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPreflightManagedOutputBuilderRejectsHealthOnlyServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "no initialize", http.StatusNotImplemented)
	}))
	defer server.Close()
	if err := preflightManagedOutputBuilder(context.Background(), server.Client(), server.URL+"/mcp"); err == nil {
		t.Fatal("health-only MCP server passed protocol preflight")
	}
}

func TestPreflightManagedOutputBuilderRequiresExactLifecycle(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		methods = append(methods, request.Method)
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"describe_output","inputSchema":{"type":"object"}},{"name":"validate_output","inputSchema":{"type":"object"}},{"name":"write_output","inputSchema":{"type":"object"}}]}}`))
	}))
	defer server.Close()
	if err := preflightManagedOutputBuilder(context.Background(), server.Client(), server.URL+"/mcp"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(methods, ","); got != "initialize,notifications/initialized,tools/list" {
		t.Fatalf("methods = %q", got)
	}
}

// This catches the runner reverting to the server's old protocol version and
// thereby failing to exercise the pinned Claude client's negotiation path.
func TestPreflightManagedOutputBuilderNegotiatesPinnedClaudeProtocol(t *testing.T) {
	var requestedVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			requestedVersion = request.Params.ProtocolVersion
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"describe_output","inputSchema":{"type":"object"}},{"name":"validate_output","inputSchema":{"type":"object"}},{"name":"write_output","inputSchema":{"type":"object"}}]}}`))
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	if err := preflightManagedOutputBuilder(context.Background(), server.Client(), server.URL+"/mcp"); err != nil {
		t.Fatal(err)
	}
	if requestedVersion != "2025-11-25" {
		t.Fatalf("requested protocol = %q", requestedVersion)
	}
}

func TestPreflightManagedOutputBuilderBoundsInitializedNotificationBody(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(managedOutputBuilderResponseLimit)+1))), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"result":{"protocolVersion":"2024-11-05"}}`)), Request: request}, nil
	})}
	if err := preflightManagedOutputBuilder(context.Background(), client, "http://example.invalid/mcp"); err == nil {
		t.Fatal("oversized initialized response was accepted")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAdmittedMCPServersAddsOnlyServerOwnedOutputBuilder(t *testing.T) {
	servers, enabled, err := admittedMCPServers("1", map[string]string{"review": "http://127.0.0.1:7781/mcp"})
	if err != nil || !enabled {
		t.Fatalf("admit = (%#v, %t, %v)", servers, enabled, err)
	}
	if servers[outputBuilderMCPName] != outputBuilderMCPURL || servers["review"] != "http://127.0.0.1:7781/mcp" {
		t.Fatalf("servers = %#v", servers)
	}
	for _, authored := range []map[string]string{{outputBuilderMCPName: "http://127.0.0.1:7781/mcp"}, {"forged": outputBuilderMCPURL}} {
		if _, _, err := admittedMCPServers("", authored); err == nil {
			t.Fatalf("authored managed collision accepted: %#v", authored)
		}
	}
}

func TestOutputBuilderPromptRetainsSealedRecordAuthorityFallback(t *testing.T) {
	prompt := decorateOutputContractPrompt(
		"do it",
		true,
		map[string]SnapshotAuthority{
			"AGENT_INPUT_LOGS": {Type: "log-bundle/v1", Digest: "sha256:" + strings.Repeat("a", 64)},
		},
		map[string]RecordAuthority{
			"AGENT_OUTPUT_DIAGNOSIS": {Type: "diagnosis/v1", Schema: "sha256:" + strings.Repeat("b", 64)},
		},
	)
	for _, want := range []string{
		"# Structured output builder (platform-managed MCP)",
		"# Sealed record authority (platform-resolved)",
		"$AGENT_INPUT_LOGS_SNAPSHOT_DIGEST = sha256:" + strings.Repeat("a", 64),
		"$AGENT_OUTPUT_DIAGNOSIS_RECORD_SCHEMA = sha256:" + strings.Repeat("b", 64),
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestAdmitBrokerMCPRejectsImpersonationAndInjectsOnlyServerMarker(t *testing.T) {
	for _, authored := range []map[string]string{{"agent-broker": "http://127.0.0.1:7784/mcp"}, {"other": "http://127.0.0.1:7784/mcp"}} {
		if _, _, err := admittedMCPServers("", authored); err == nil {
			t.Fatalf("authored broker MCP = %#v was admitted", authored)
		}
	}
	servers, enabled, err := admitBrokerMCP("1", map[string]string{"review": "http://127.0.0.1:7781/mcp"})
	if err != nil || !enabled || servers["agent-broker"] != "http://127.0.0.1:7784/mcp" {
		t.Fatalf("broker MCP = %#v, enabled = %v, err = %v", servers, enabled, err)
	}
}

func TestWriteMCPConfigCleansPrivateDirectory(t *testing.T) {
	path, cleanup, err := writeMCPConfig(map[string]string{
		brokerMCPName: brokerMCPURL,
	}, "parent-access-token")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), brokerMCPURL) ||
		!strings.Contains(string(contents), `"Authorization":"Bearer parent-access-token"`) {
		t.Fatalf("private config = %q, err = %v", contents, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("private config remains after cleanup: %v", err)
	}
}
