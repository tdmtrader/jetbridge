package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	brokermcp "github.com/concourse/concourse/agent/broker/mcp"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestServerExposesOnlyTheTwoNeutralTools(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{mcpProfile()})
	server, err := brokermcp.NewServer(&fakeService{}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	response := rpc(t, server, "tools/list", nil)
	result := response["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", tools)
	}
	encoded, _ := json.Marshal(tools)
	text := string(encoded)
	for _, wanted := range []string{"request_review", "consult_agent", "balanced", "high"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("tool catalog does not contain %q: %s", wanted, text)
		}
	}
	for _, forbidden := range []string{"openai", "anthropic", "exact-model", "codex"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("agent-visible tool catalog disclosed %q: %s", forbidden, text)
		}
	}
}

func TestServerCallsConsultAgentSynchronously(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{mcpProfile()})
	service := &fakeService{}
	server, _ := brokermcp.NewServer(service, catalog)
	response := rpc(t, server, "tools/call", map[string]any{
		"name": "consult_agent",
		"arguments": map[string]any{
			"tier": "balanced", "effort": "high", "question": "What matters?",
		},
	})
	result := response["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("tool result = %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "child-1") || service.consult.Question != "What matters?" {
		t.Fatalf("content = %s, request = %#v", content, service.consult)
	}
	if service.consult.IdempotencyKey == "" {
		t.Fatal("server did not assign an idempotency key")
	}
}

func TestServerRejectsUnknownArgumentsBeforeService(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{mcpProfile()})
	service := &fakeService{}
	server, _ := brokermcp.NewServer(service, catalog)
	response := rpc(t, server, "tools/call", map[string]any{
		"name": "consult_agent",
		"arguments": map[string]any{
			"tier": "balanced", "effort": "high", "question": "q", "model": "exact-model",
		},
	})
	result := response["result"].(map[string]any)
	if result["isError"] != true || service.consult.Question != "" {
		t.Fatalf("result = %#v, request = %#v", result, service.consult)
	}
}

type fakeService struct {
	consult broker.ConsultRequest
}

func (service *fakeService) RequestReview(context.Context, broker.ReviewRequest) (broker.Result, error) {
	return broker.Result{ExecutionID: "review-1"}, nil
}
func (service *fakeService) ConsultAgent(_ context.Context, request broker.ConsultRequest) (broker.Result, error) {
	service.consult = request
	return broker.Result{
		ExecutionID: "child-1",
		Snapshot: snapshot.SnapshotRef{
			ID: 1, Type: "consultation/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		},
	}, nil
}

func rpc(t *testing.T, handler http.Handler, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response map[string]any
	if err := json.NewDecoder(recorder.Result().Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func mcpProfile() broker.Profile {
	profile := validMCPProfile()
	return profile
}

func validMCPProfile() broker.Profile {
	return broker.Profile{
		ID: "profile", Revision: 1,
		Selector:     broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:        []broker.Tool{broker.ToolRequestReview, broker.ToolConsultAgent},
		Purpose:      "careful general review",
		WorkerImage:  "registry/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:      broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:     broker.ProviderSpec{Name: "provider", Model: "exact-model"},
		NativeEffort: "high", InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot: "shared",
		Limits:         broker.Limits{Timeout: 1, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeOutputSchema: true, IgnoresUserConfig: true,
		},
	}
}
