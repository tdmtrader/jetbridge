package contracttest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cannedServer answers every JSON-RPC method from a fixed response map,
// echoing the request id.
func cannedServer(responses map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  responses[req.Method],
		})
	}))
}

// toolText wraps a JSON payload string as an MCP tool result.
func toolText(payload string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": payload}},
	}
}

func TestCheckInitialize(t *testing.T) {
	good := cannedServer(map[string]any{
		"initialize": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
		},
	})
	defer good.Close()
	if err := checkInitialize(context.Background(), good.URL); err != nil {
		t.Fatalf("conforming server rejected: %v", err)
	}

	bad := cannedServer(map[string]any{
		"initialize": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
		},
	})
	defer bad.Close()
	err := checkInitialize(context.Background(), bad.URL)
	if err == nil || !strings.Contains(err.Error(), "tools capability") {
		t.Fatalf("expected tools-capability error, got %v", err)
	}
}

func TestCheckToolsListRequiresAllFiveTools(t *testing.T) {
	tool := func(name string) map[string]any {
		return map[string]any{
			"name":        name,
			"description": "d",
			"inputSchema": map[string]any{"type": "object"},
		}
	}
	missing := cannedServer(map[string]any{
		"tools/list": map[string]any{"tools": []any{
			tool("list_components"), tool("build"), tool("run_tests"), tool("lint"),
			// affected_components missing
		}},
	})
	defer missing.Close()
	err := checkToolsList(context.Background(), missing.URL)
	if err == nil || !strings.Contains(err.Error(), "affected_components") {
		t.Fatalf("expected missing-tool error, got %v", err)
	}
}

func TestCheckListComponentsValidatesFields(t *testing.T) {
	badKind := cannedServer(map[string]any{
		"tools/call": toolText(`{"components":[{"id":"a","description":"d","paths":["a/"],"kind":"banana"}]}`),
	})
	defer badKind.Close()
	err := checkListComponents(context.Background(), badKind.URL)
	if err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Fatalf("expected invalid-kind error, got %v", err)
	}
}

func TestCheckFailingLintDemandsFailedStatus(t *testing.T) {
	okServer := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"ok","summary":"clean","duration_seconds":0.1}`),
	})
	defer okServer.Close()
	err := checkFailingLint(context.Background(), okServer.URL, "app")
	if err == nil || !strings.Contains(err.Error(), `want failed`) {
		t.Fatalf("expected want-failed error, got %v", err)
	}

	failedServer := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"failed","summary":"1 finding","duration_seconds":0.1}`),
	})
	defer failedServer.Close()
	if err := checkFailingLint(context.Background(), failedServer.URL, "app"); err != nil {
		t.Fatalf("conforming server rejected: %v", err)
	}
}

func TestCheckUnknownComponentDemandsRPCError(t *testing.T) {
	// A server that answers unknown components with a payload instead of a
	// JSON-RPC error violates the taxonomy.
	lenient := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"error","summary":"unknown","duration_seconds":0}`),
	})
	defer lenient.Close()
	if err := checkUnknownComponent(context.Background(), lenient.URL); err == nil {
		t.Fatal("expected an error for a server answering unknown components with a payload")
	}
}
