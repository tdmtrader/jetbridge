package outputbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// This catches transport drift: the CLI must be only an adapter around the
// same closed-over builder and must not accept authority in its request.
func TestCLIUsesBuilderWithoutAuthorityOverride(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	request := `{"output":"review","subjects":[{"id":"repository","role":"primary","input":"base"}],"body":{"conclusion":"accept","summary":"ready","findings":[]}}`
	var stdout, stderr bytes.Buffer
	exit := NewCLI(builder, strings.NewReader(request), &stdout, &stderr).Run(context.Background(), []string{"write"})
	if exit != 0 || !strings.Contains(stdout.String(), `"valid":true`) {
		t.Fatalf("CLI write exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exit = NewCLI(builder, strings.NewReader(`{"authority":"forged"}`), &stdout, &stderr).Run(context.Background(), []string{"write"})
	if exit == 0 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("CLI accepted authority override: exit=%d stderr=%q", exit, stderr.String())
	}
}

// This catches an endpoint escape or an MCP tool that can author outside the
// same limited builder authority used by the CLI.
func TestMCPLoopbackOnlyServesBuilderTools(t *testing.T) {
	if err := ValidateMCPListenAddress("127.0.0.1:7784"); err == nil {
		t.Fatal("ValidateMCPListenAddress accepted a caller-selected endpoint")
	}
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewMCPServer(builder))
	defer server.Close()
	post := func(raw string) (int, string) {
		response, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		buffer := new(bytes.Buffer)
		_, _ = buffer.ReadFrom(response.Body)
		return response.StatusCode, buffer.String()
	}
	status, body := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`)
	var initialized struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &initialized) != nil || initialized.JSONRPC != "2.0" || initialized.ID != 1 || initialized.Result.ProtocolVersion != "2024-11-05" || initialized.Result.ServerInfo.Name != "concourse-output-builder" || initialized.Result.ServerInfo.Version != "1" || len(initialized.Result.Capabilities) != 1 || initialized.Result.Capabilities["tools"] == nil {
		t.Fatalf("initialize = %d %q", status, body)
	}
	if status, body := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusNoContent || body != "" {
		t.Fatalf("initialized = %d %q", status, body)
	}
	status, body = post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var listed struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				InputSchema struct {
					Type       string   `json:"type"`
					Required   []string `json:"required"`
					Properties map[string]struct {
						Type string `json:"type"`
					} `json:"properties"`
					AdditionalProperties bool `json:"additionalProperties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &listed) != nil || listed.JSONRPC != "2.0" || listed.ID != 2 || len(listed.Result.Tools) != 3 {
		t.Fatalf("tools=%d %q", status, body)
	}
	wants := [][]string{{"output"}, {"output"}, {"output", "subjects", "body"}}
	names := []string{"describe_output", "validate_output", "write_output"}
	for i, tool := range listed.Result.Tools {
		if tool.Name != names[i] || tool.Description == "" || tool.InputSchema.Type != "object" || tool.InputSchema.AdditionalProperties || strings.Join(tool.InputSchema.Required, ",") != strings.Join(wants[i], ",") {
			t.Fatalf("tool=%#v", tool)
		}
		wantProperties := []map[string]string{{"output": "string"}, {"output": "string"}, {"output": "string", "subjects": "array", "body": "object", "content": "array"}}[i]
		if len(tool.InputSchema.Properties) != len(wantProperties) {
			t.Fatalf("properties=%#v", tool.InputSchema.Properties)
		}
		for name, want := range wantProperties {
			if tool.InputSchema.Properties[name].Type != want {
				t.Fatalf("property %s = %#v", name, tool.InputSchema.Properties[name])
			}
		}
	}
	type rpcError struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	for _, tc := range []struct {
		raw, id string
		code    int
	}{{`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"bad"}}`, "3", -32602}, {`{"jsonrpc":"2.0","id":4,"method":"notifications/initialized"}`, "4", -32600}, {`{"jsonrpc":"2.0","method":"notifications/ready"}`, "null", -32601}, {`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"write_output","arguments":{"authority":"forged"}}}`, "5", -32602}} {
		status, body := post(tc.raw)
		var got rpcError
		if status != 200 || json.Unmarshal([]byte(body), &got) != nil || got.JSONRPC != "2.0" || string(got.ID) != tc.id || got.Error.Code != tc.code {
			t.Fatalf("negative=%d %q", status, body)
		}
	}
	nilServer := httptest.NewServer(NewMCPServer(nil))
	defer nilServer.Close()
	response, err := http.Post(nilServer.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"write_output","arguments":{"authority":"forged"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var nilResult rpcError
	if json.NewDecoder(response.Body).Decode(&nilResult) != nil || nilResult.Error.Code != -32603 {
		t.Fatalf("nil builder unavailable = %#v", nilResult)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"write_output","arguments":{"output":"review","subjects":[{"id":"repository","role":"primary","input":"base"}],"body":{"conclusion":"accept","summary":"ready","findings":[]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := new(bytes.Buffer)
	buffer.Reset()
	_, _ = buffer.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(buffer.String(), `\"valid\":true`) {
		t.Fatalf("MCP write response = status %d body %q", response.StatusCode, buffer.String())
	}
}

// This catches a pinned Claude client becoming unable to discover the managed
// output tools when its descriptive initialize metadata evolves.
func TestMCPInitializeNegotiatesClaudeCode212(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewMCPServer(builder))
	defer server.Close()
	post := func(raw string) (int, string) {
		response, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}

	initialize := `{
  "jsonrpc":"2.0",
  "id":21,
  "method":"initialize",
  "params":{
    "protocolVersion":"2025-11-25",
    "capabilities":{},
    "clientInfo":{
      "name":"claude-code",
      "title":"Claude Code",
      "version":"2.1.212",
      "description":"Anthropic coding agent",
      "websiteUrl":"https://claude.com/claude-code"
    }
  }
}`
	status, body := post(initialize)
	var initialized struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *mcpError `json:"error"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &initialized) != nil || initialized.JSONRPC != "2.0" || initialized.ID != 21 || initialized.Error != nil || initialized.Result.ProtocolVersion != "2024-11-05" {
		t.Fatalf("initialize = %d %q", status, body)
	}
	if status, body := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusNoContent || body != "" {
		t.Fatalf("initialized = %d %q", status, body)
	}
	status, body = post(`{"jsonrpc":"2.0","id":22,"method":"tools/list"}`)
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Type string `json:"type"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &listed) != nil || len(listed.Result.Tools) != 3 {
		t.Fatalf("tools/list = %d %q", status, body)
	}
	for i, name := range []string{"describe_output", "validate_output", "write_output"} {
		if listed.Result.Tools[i].Name != name || listed.Result.Tools[i].InputSchema.Type != "object" {
			t.Fatalf("tool[%d] = %#v", i, listed.Result.Tools[i])
		}
	}

	for _, tc := range []struct {
		name, raw string
	}{
		{name: "unknown protocol", raw: `{"jsonrpc":"2.0","id":23,"method":"initialize","params":{"protocolVersion":"bad"}}`},
		{name: "malformed JSON", raw: `{"jsonrpc":"2.0",`},
		{name: "authority argument", raw: `{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"write_output","arguments":{"authority":"forged"}}}`},
		{name: "unknown tool", raw: `{"jsonrpc":"2.0","id":25,"method":"tools/call","params":{"name":"forged","arguments":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(tc.raw)
			var response struct {
				Error *mcpError `json:"error"`
			}
			if status != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil || response.Error == nil || response.Error.Code != -32602 && response.Error.Code != -32600 {
				t.Fatalf("response = %d %q", status, body)
			}
		})
	}
}

// This catches a regression where standards-compatible MCP request metadata is
// mistaken for builder authority before the real tool arguments are decoded.
func TestMCPClaudeToolEnvelopeMetadataDoesNotBypassStrictArguments(t *testing.T) {
	authority := validAuthority(t)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := New(authority, registry, snapshot.Canonicalizer{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewMCPServer(builder))
	defer server.Close()

	post := func(raw string) (int, string) {
		response, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}

	for _, tc := range []struct {
		name, raw string
		decode    func(t *testing.T, text string)
	}{
		{
			name: "describe output",
			raw:  `{"jsonrpc":"2.0","id":31,"method":"tools/call","params":{"name":"describe_output","arguments":{"output":"review"},"_meta":{"progressToken":"claude-2.1.212-describe"},"task":{"ttl":60000}}}`,
			decode: func(t *testing.T, text string) {
				t.Helper()
				var description Description
				if err := json.Unmarshal([]byte(text), &description); err != nil || description.Port.Name != "review" || description.Port.Type != "review/v1" {
					t.Fatalf("describe result = %q, %v", text, err)
				}
			},
		},
		{
			name: "write output",
			raw:  `{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"write_output","arguments":{"output":"review","subjects":[{"id":"repository","role":"primary","input":"base"}],"body":{"conclusion":"accept","summary":"ready","findings":[]}},"_meta":{"progressToken":"claude-2.1.212-write"},"task":{"ttl":60000}}}`,
			decode: func(t *testing.T, text string) {
				t.Helper()
				var report ValidationReport
				if err := json.Unmarshal([]byte(text), &report); err != nil || !report.Valid {
					t.Fatalf("write result = %q, %v", text, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(tc.raw)
			var response struct {
				Error  *mcpError `json:"error"`
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if status != http.StatusOK || json.Unmarshal([]byte(body), &response) != nil || response.Error != nil || len(response.Result.Content) != 1 {
				t.Fatalf("response = %d %q", status, body)
			}
			tc.decode(t, response.Result.Content[0].Text)
		})
	}

	status, body := post(`{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"describe_output","arguments":{"output":"review","unexpected":"rejected"},"_meta":{"progressToken":"claude-2.1.212-strict"},"task":{"ttl":60000}}}`)
	var invalid struct {
		Error *mcpError `json:"error"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &invalid) != nil || invalid.Error == nil || invalid.Error.Code != -32602 {
		t.Fatalf("unknown argument response = %d %q", status, body)
	}
}
