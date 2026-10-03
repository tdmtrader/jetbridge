package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectMCP(t *testing.T, parse func(*flag.FlagSet, []string) (*mcp.Server, error), args ...string) *mcp.ClientSession {
	t.Helper()
	flags := flag.NewFlagSet("jb-test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server, err := parse(flags, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "jb-test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// `jb mcp` is one server for every workload; `jb review mcp` is the same
// server with only the review tools, so existing configurations keep working.
func TestMCPServesEachWorkloadFromItsOwnTemplate(t *testing.T) {
	p := newCLIPlatform(t)
	p.Schedule(t, p.team, "implement", "change")
	root := t.TempDir()
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":"owner-auth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	number, runID := submit(t, "implement", "--input", capturedSnapshot(t, root), "--receipt", filepath.Join(root, "request.json"), "--auth-file", auth)

	both := connectMCP(t, workloadMCPServer, "--target", "jb-test")
	if got := toolNames(t, both); got != "implement_result implement_status implement_submit review_result review_status review_submit" {
		t.Fatalf("jb mcp serves %s", got)
	}
	review := connectMCP(t, reviewMCPServer, "--target", "jb-test", "--template", "review")
	if got := toolNames(t, review); got != "review_result review_status review_submit" {
		t.Fatalf("jb review mcp serves %s", got)
	}

	// Each family reads its own installed template: the platform has the Run
	// under the implement template only.
	status := func(session *mcp.ClientSession, tool string) (*mcp.CallToolResult, error) {
		return session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"run": number}})
	}
	result, err := status(both, "implement_status")
	if err != nil || result.IsError {
		t.Fatalf("implement_status: %v %+v", err, result)
	}
	var observed struct {
		ID     int           `json:"id"`
		Status atc.RunStatus `json:"status"`
	}
	data, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(data, &observed); err != nil || observed.ID != runID || observed.Status != atc.RunStatusRunning {
		t.Fatalf("implement_status did not observe the implement Run: %s %v", data, err)
	}
	for _, session := range []*mcp.ClientSession{both, review} {
		if result, err := status(session, "review_status"); err != nil || !result.IsError || !strings.Contains(toolText(result), "HTTP 404") {
			t.Fatalf("review_status read the implement template: %v %+v", err, result)
		}
	}
	moved := connectMCP(t, workloadMCPServer, "--target", "jb-test", "--review-template", "implement", "--implement-template", "elsewhere")
	if result, err := status(moved, "review_status"); err != nil || result.IsError {
		t.Fatalf("--review-template did not select the review template: %v %+v", err, result)
	}
	if result, err := status(moved, "implement_status"); err != nil || !result.IsError {
		t.Fatalf("--implement-template did not select the implement template: %v %+v", err, result)
	}

	// Flags are fixed per command; nothing is served without a target.
	for _, args := range [][]string{
		{"mcp"},
		{"mcp", "--target", "jb-test", "extra"},
		{"mcp", "--target", "jb-test", "--template", "review"},
		{"mcp", "--target", "jb-test", "--implement-template", ""},
		{"review", "mcp", "--target", "jb-test", "--implement-template", "implement"},
	} {
		var out, stderr bytes.Buffer
		if err := run(context.Background(), args, &out, &stderr); err == nil {
			t.Fatalf("jb %s started a server", strings.Join(args, " "))
		}
	}
}

func toolText(result *mcp.CallToolResult) string {
	var text []string
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	return strings.Join(text, "\n")
}
