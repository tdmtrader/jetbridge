package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listTools connects to server in memory and returns the tools it lists.
func listTools(t *testing.T, server *mcp.Server) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "review-tools-test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return listed.Tools
}

// The review_* tools are what existing local MCP configurations call. Their
// names, descriptions and schemas were recorded before the tools could be
// registered beside another workload's, and must not change.
func TestReviewToolsAreUnchanged(t *testing.T) {
	c, err := New("https://jetbridge.example.test", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "jetbridge-review", Version: "1"}, nil)
	RegisterTools(server, c, MCPOptions{Team: "main", Template: "review", AuthFile: "auth.json"})
	got, err := json.MarshalIndent(listTools(t, server), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "review-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatalf("review MCP tools changed:\n%s", got)
	}
}
