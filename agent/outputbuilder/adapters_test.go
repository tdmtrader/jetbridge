package outputbuilder

import (
	"bytes"
	"context"
	"encoding/json"
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
	}{{`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"bad"}}`, "3", -32602}, {`{"jsonrpc":"2.0","id":4,"method":"notifications/initialized"}`, "4", -32600}, {`{"jsonrpc":"2.0","method":"notifications/ready"}`, "null", -32601}, {`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"authority":"forged","name":"write_output","arguments":{}}}`, "5", -32602}} {
		status, body := post(tc.raw)
		var got rpcError
		if status != 200 || json.Unmarshal([]byte(body), &got) != nil || got.JSONRPC != "2.0" || string(got.ID) != tc.id || got.Error.Code != tc.code {
			t.Fatalf("negative=%d %q", status, body)
		}
	}
	nilServer := httptest.NewServer(NewMCPServer(nil))
	defer nilServer.Close()
	response, err := http.Post(nilServer.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"authority":"forged","name":"write_output","arguments":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var nilResult rpcError
	if json.NewDecoder(response.Body).Decode(&nilResult) != nil || nilResult.Error.Code != -32602 {
		t.Fatalf("nil builder authority rejection = %#v", nilResult)
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
