package outputbuilder

import (
	"bytes"
	"context"
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
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := new(bytes.Buffer)
	_, _ = buffer.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(buffer.String(), "write_output") || strings.Contains(buffer.String(), "seal") {
		t.Fatalf("MCP tools response = status %d body %q", response.StatusCode, buffer.String())
	}
	request, err = http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write_output","arguments":{"output":"review","subjects":[{"id":"repository","role":"primary","input":"base"}],"body":{"conclusion":"accept","summary":"ready","findings":[]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer.Reset()
	_, _ = buffer.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(buffer.String(), `\"valid\":true`) {
		t.Fatalf("MCP write response = status %d body %q", response.StatusCode, buffer.String())
	}
}
